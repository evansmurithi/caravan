// Command caravan is a GitOps controller for HashiCorp Nomad. It continuously
// reconciles a Nomad cluster with job specifications stored in a Git repo.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/evansmurithi/caravan/internal/config"
	"github.com/evansmurithi/caravan/internal/git"
	"github.com/evansmurithi/caravan/internal/nomad"
	"github.com/evansmurithi/caravan/internal/reconcile"
)

// version is overridden at build time via -ldflags.
var version = "dev"

const usage = `caravan is a GitOps controller for HashiCorp Nomad.

Usage:
  caravan [command] [flags]

Commands:
  run       Continuously reconcile the cluster with Git (default)
  diff      Show what would change without applying, then exit
  version   Print the version and exit

Run "caravan <command> --help" to see flags.`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "caravan: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "run", "diff":
		// handled below
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}

	cfg, err := config.Load(args)
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			return nil // usage already printed by the flag parser
		}
		return err
	}

	log := newLogger(cfg)
	reconciler, err := buildReconciler(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cmd == "diff" {
		return runDiff(ctx, reconciler)
	}
	return runLoop(ctx, cfg, reconciler, log)
}

func buildReconciler(cfg *config.Config, log *slog.Logger) (*reconcile.Reconciler, error) {
	checkoutDir := filepath.Join(os.TempDir(), "caravan", "repo")
	gitSrc, err := git.New(git.Options{
		URL:           cfg.GitURL,
		Branch:        cfg.GitBranch,
		Token:         cfg.GitToken,
		SSHKey:        cfg.GitSSHKey,
		SSHPassphrase: cfg.GitSSHPassphrase,
		Dir:           checkoutDir,
	})
	if err != nil {
		return nil, err
	}

	nomadClient, err := nomad.New(nomad.Config{
		Address:   cfg.NomadAddr,
		Token:     cfg.NomadToken,
		Region:    cfg.NomadRegion,
		Namespace: cfg.NomadNamespace,
	})
	if err != nil {
		return nil, err
	}

	return reconcile.New(cfg, gitSrc, nomadClient, log), nil
}

func runLoop(ctx context.Context, cfg *config.Config, reconciler *reconcile.Reconciler, log *slog.Logger) error {
	log.Info("starting caravan",
		"version", version,
		"git_url", cfg.GitURL,
		"git_branch", cfg.GitBranch,
		"git_path", cfg.GitPath,
		"nomad_addr", cfg.NomadAddr,
		"mode", cfg.Mode,
		"prune", cfg.Prune,
		"sync_interval", cfg.SyncInterval,
	)

	if cfg.HealthAddr != "" && !cfg.Once {
		srv := healthServer(cfg.HealthAddr, reconciler)
		go func() {
			log.Info("health server listening", "addr", cfg.HealthAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("health server error", "error", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
	}

	return reconciler.Run(ctx)
}

// runDiff performs a single read-only diff pass and prints the result. Any
// error is returned so main can report it once; per-job errors are printed
// inline alongside the jobs that parsed and planned successfully.
func runDiff(ctx context.Context, reconciler *reconcile.Reconciler) error {
	commit, plans, err := reconciler.Diff(ctx)

	color := isTerminal(os.Stdout)
	if commit != "" {
		fmt.Printf("caravan diff @ %s\n\n", shortCommit(commit))
	}

	changed := 0
	for _, p := range plans {
		switch {
		case p.Err != nil:
			changed++
			fmt.Printf("! %s (%s): plan error: %v\n\n", jobLabel(p.ID), p.Namespace, p.Err)
		case p.Orphan:
			changed++
			fmt.Printf("- Job %q (%s) would be pruned: managed by caravan but not in Git\n\n", p.ID, p.Namespace)
		case p.Changed():
			changed++
			fmt.Println(reconcile.RenderJobDiff(p.Diff, color))
			fmt.Println()
		}
	}

	if changed == 0 && err == nil {
		fmt.Println("No changes. The cluster matches Git.")
	}
	return err
}

func jobLabel(id string) string {
	if id == "" {
		return "<unknown>"
	}
	return id
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func healthServer(addr string, r *reconcile.Reconciler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		last := r.Last()
		if last.At.IsZero() {
			http.Error(w, "no reconcile yet", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		last := r.Last()
		payload := map[string]any{
			"last_run":  last.At,
			"commit":    last.Commit,
			"applied":   last.Applied,
			"pending":   last.Planned,
			"pruned":    last.Pruned,
			"unchanged": last.Unchanged,
		}
		if last.Err != nil {
			payload["error"] = last.Err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
