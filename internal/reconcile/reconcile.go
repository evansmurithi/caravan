// Package reconcile implements caravan's control loop: sync Git, render jobs,
// diff them against the cluster, and converge the cluster toward Git.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/config"
	"github.com/evansmurithi/caravan/internal/job"
)

// GitSource syncs a local checkout with a remote and reports its location.
type GitSource interface {
	Sync() (string, error)
	Dir() string
}

// NomadClient is the subset of Nomad operations the reconciler needs.
type NomadClient interface {
	Plan(job *api.Job) (*api.JobPlanResponse, error)
	Register(job *api.Job) (*api.JobRegisterResponse, error)
	List(namespace string) ([]*api.JobListStub, error)
	Stop(jobID, namespace string, purge bool) error
}

// Reconciler converges a Nomad cluster to the state described in Git.
type Reconciler struct {
	cfg    *config.Config
	git    GitSource
	nomad  NomadClient
	log    *slog.Logger
	filesR func(root, baseDir string) ([]job.Desired, error)

	mu   sync.Mutex
	last Result
}

// Result summarizes a single reconcile pass.
type Result struct {
	At        time.Time
	Commit    string
	Applied   []string
	Planned   []string // jobs with pending changes (plan mode or diff detected)
	Pruned    []string
	Unchanged int
	Err       error
}

// New builds a Reconciler.
func New(cfg *config.Config, gitSrc GitSource, nomad NomadClient, log *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:    cfg,
		git:    gitSrc,
		nomad:  nomad,
		log:    log,
		filesR: job.Discover,
	}
}

// Last returns the most recent reconcile result.
func (r *Reconciler) Last() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Run executes reconcile passes until the context is cancelled. When
// cfg.Once is set, it runs a single pass and returns.
func (r *Reconciler) Run(ctx context.Context) error {
	if r.cfg.Once {
		res := r.reconcile(ctx)
		r.store(res)
		return res.Err
	}

	ticker := time.NewTicker(r.cfg.SyncInterval)
	defer ticker.Stop()

	// Reconcile immediately, then on every tick.
	res := r.reconcile(ctx)
	r.store(res)

	for {
		select {
		case <-ctx.Done():
			r.log.Info("shutting down reconcile loop")
			return nil
		case <-ticker.C:
			res := r.reconcile(ctx)
			r.store(res)
		}
	}
}

func (r *Reconciler) store(res Result) {
	r.mu.Lock()
	r.last = res
	r.mu.Unlock()
}

func (r *Reconciler) reconcile(ctx context.Context) Result {
	res := Result{At: time.Now()}
	start := res.At

	commit, err := r.git.Sync()
	if err != nil {
		res.Err = fmt.Errorf("git sync: %w", err)
		r.log.Error("git sync failed", "error", err)
		return res
	}
	res.Commit = commit
	log := r.log.With("commit", short(commit))
	log.Info("synced git", "path", r.cfg.GitPath)

	root, baseDir := r.paths()
	desired, err := r.filesR(root, baseDir)
	if err != nil {
		// Discover returns partial results alongside parse errors; keep going
		// with what parsed successfully but record the failure.
		res.Err = err
		log.Error("job discovery reported errors", "error", err)
	}
	log.Info("discovered jobs", "count", len(desired))

	desiredIDs := make(map[nsKey]bool, len(desired))
	for _, d := range desired {
		ns := d.Namespace(r.cfg.NomadNamespace)
		desiredIDs[nsKey{ns, d.ID()}] = true

		if err := ctx.Err(); err != nil {
			return res
		}
		r.applyOne(log, d, &res)
	}

	if r.cfg.Prune {
		r.prune(ctx, log, desiredIDs, &res)
	}

	log.Info("reconcile complete",
		"applied", len(res.Applied),
		"pending", len(res.Planned),
		"pruned", len(res.Pruned),
		"unchanged", res.Unchanged,
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return res
}

// paths returns the directory to scan for job files and the base directory that
// HCL file() calls resolve against.
func (r *Reconciler) paths() (root, baseDir string) {
	repoDir := r.git.Dir()
	root = repoDir
	if r.cfg.GitPath != "" && r.cfg.GitPath != "." {
		root = joinPath(repoDir, r.cfg.GitPath)
	}

	// file() and friends resolve against the configured base dir (like a
	// working directory), defaulting to the scan root.
	baseDir = root
	if r.cfg.FileBaseDir != "" && r.cfg.FileBaseDir != "." {
		baseDir = joinPath(repoDir, r.cfg.FileBaseDir)
	}
	return root, baseDir
}

func (r *Reconciler) applyOne(log *slog.Logger, d job.Desired, res *Result) {
	jlog := log.With("job", d.ID(), "namespace", d.Namespace(r.cfg.NomadNamespace), "source", d.SourcePath)

	plan, err := r.nomad.Plan(d.Job)
	if err != nil {
		jlog.Error("plan failed", "error", err)
		if res.Err == nil {
			res.Err = err
		}
		return
	}

	changed := plan.Diff != nil && plan.Diff.Type != "None"
	if !changed {
		res.Unchanged++
		jlog.Debug("no changes")
		return
	}

	summary := summarizeDiff(plan.Diff)
	res.Planned = append(res.Planned, d.ID())

	if r.cfg.Mode == config.ModePlan {
		jlog.Info("changes detected (plan mode, not applying)", "diff", summary)
		return
	}

	resp, err := r.nomad.Register(d.Job)
	if err != nil {
		jlog.Error("apply failed", "error", err)
		if res.Err == nil {
			res.Err = err
		}
		return
	}
	res.Applied = append(res.Applied, d.ID())
	jlog.Info("applied changes", "diff", summary, "eval", resp.EvalID)
}

func (r *Reconciler) prune(ctx context.Context, log *slog.Logger, desired map[nsKey]bool, res *Result) {
	namespaces := map[string]bool{r.cfg.NomadNamespace: true}
	for k := range desired {
		namespaces[k.ns] = true
	}

	for ns := range namespaces {
		if err := ctx.Err(); err != nil {
			return
		}
		stubs, err := r.nomad.List(ns)
		if err != nil {
			log.Error("prune list failed", "namespace", ns, "error", err)
			if res.Err == nil {
				res.Err = err
			}
			continue
		}
		for _, stub := range stubs {
			if !job.IsManaged(stub.Meta) {
				continue
			}
			if stub.Stop {
				continue // already stopping/stopped
			}
			if desired[nsKey{ns, stub.ID}] {
				continue
			}

			plog := log.With("job", stub.ID, "namespace", ns)
			if r.cfg.Mode == config.ModePlan {
				plog.Info("orphaned job detected (plan mode, not pruning)")
				res.Pruned = append(res.Pruned, stub.ID)
				continue
			}
			if err := r.nomad.Stop(stub.ID, ns, false); err != nil {
				plog.Error("prune failed", "error", err)
				if res.Err == nil {
					res.Err = err
				}
				continue
			}
			plog.Info("pruned orphaned job")
			res.Pruned = append(res.Pruned, stub.ID)
		}
	}
}

type nsKey struct {
	ns string
	id string
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func joinPath(root, sub string) string {
	return filepath.Join(root, sub)
}
