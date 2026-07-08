// Package config loads caravan's runtime configuration using Viper, layering
// command-line flags over environment variables and an optional config file.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// ErrHelp is returned by Load when the user requests help (-h/--help). Callers
// should treat it as a clean exit; usage has already been printed.
var ErrHelp = pflag.ErrHelp

// Mode controls what the reconciler does with detected changes.
type Mode string

const (
	// ModeSync applies changes to the Nomad cluster.
	ModeSync Mode = "sync"
	// ModePlan only reports the diff without applying it (dry-run).
	ModePlan Mode = "plan"
)

// Config holds all runtime settings for the controller.
type Config struct {
	// Git source.
	GitURL           string
	GitBranch        string
	GitPath          string
	GitToken         string // access token for HTTP(S) auth
	GitSSHKey        string // path to a private key for SSH auth
	GitSSHPassphrase string // passphrase protecting the SSH private key

	// FileBaseDir is the directory (relative to the repo root) that HCL
	// filesystem functions such as file() resolve against, mirroring the
	// working directory you would `cd` into before running `nomad job run`.
	// When empty it defaults to the GitPath scan root.
	FileBaseDir string

	// Nomad target.
	NomadAddr      string
	NomadToken     string
	NomadRegion    string
	NomadNamespace string

	// Reconciliation behaviour.
	Mode         Mode
	Prune        bool
	SyncInterval time.Duration
	Once         bool

	// Observability.
	LogLevel  string
	LogFormat string // "text" or "json"

	// HealthAddr is the address for the health/metrics HTTP server.
	// Empty disables it.
	HealthAddr string
}

// ManagedByLabel is the meta key stamped on every job caravan manages. It is
// used to identify orphaned jobs during pruning.
const ManagedByLabel = "caravan.gitops/managed-by"

// ManagedByValue is the value stored under ManagedByLabel.
const ManagedByValue = "caravan"

// Load parses configuration from the provided arguments, layering flags over
// environment variables (CARAVAN_* and, for Nomad, the standard NOMAD_*
// fallbacks) and an optional config file (--config).
func Load(args []string) (*Config, error) {
	fs := pflag.NewFlagSet("caravan", pflag.ContinueOnError)

	fs.String("config", "", "Path to a config file (yaml, toml, or json)")

	fs.String("git-url", "", "Git repository URL to sync from (required)")
	fs.String("git-branch", "main", "Git branch to track")
	fs.String("git-path", ".", "Path within the repository to scan for job specs")
	fs.String("git-token", "", "Access token for HTTP(S) Git auth")
	fs.String("git-ssh-key", "", "Path to a private key for SSH Git auth")
	fs.String("git-ssh-passphrase", "", "Passphrase for the SSH private key")
	fs.String("file-base-dir", "", "Directory (relative to repo root) that HCL file() calls resolve against; defaults to --git-path")

	fs.String("nomad-addr", "http://127.0.0.1:4646", "Nomad HTTP API address")
	fs.String("nomad-token", "", "Nomad ACL token")
	fs.String("nomad-region", "", "Nomad region")
	fs.String("nomad-namespace", "default", "Nomad namespace for managed jobs")

	fs.String("mode", string(ModeSync), "Reconcile mode: sync or plan")
	fs.Bool("prune", false, "Stop jobs previously managed by caravan that are no longer in Git")
	fs.Duration("sync-interval", time.Minute, "How often to reconcile")
	fs.Bool("once", false, "Run a single reconcile and exit")

	fs.String("log-level", "info", "Log level: debug, info, warn, error")
	fs.String("log-format", "text", "Log format: text or json")
	fs.String("health-addr", ":8080", "Address for the health server (empty to disable)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	v := viper.New()
	if err := v.BindPFlags(fs); err != nil {
		return nil, fmt.Errorf("binding flags: %w", err)
	}

	v.SetEnvPrefix("CARAVAN")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	// Fall back to the standard Nomad environment variables when the
	// CARAVAN_-prefixed ones are unset.
	_ = v.BindEnv("nomad-addr", "CARAVAN_NOMAD_ADDR", "NOMAD_ADDR")
	_ = v.BindEnv("nomad-token", "CARAVAN_NOMAD_TOKEN", "NOMAD_TOKEN")
	_ = v.BindEnv("nomad-region", "CARAVAN_NOMAD_REGION", "NOMAD_REGION")
	_ = v.BindEnv("nomad-namespace", "CARAVAN_NOMAD_NAMESPACE", "NOMAD_NAMESPACE")

	if cfgFile := v.GetString("config"); cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", cfgFile, err)
		}
	}

	c := &Config{
		GitURL:           v.GetString("git-url"),
		GitBranch:        v.GetString("git-branch"),
		GitPath:          v.GetString("git-path"),
		GitToken:         v.GetString("git-token"),
		GitSSHKey:        v.GetString("git-ssh-key"),
		GitSSHPassphrase: v.GetString("git-ssh-passphrase"),
		FileBaseDir:      v.GetString("file-base-dir"),

		NomadAddr:      v.GetString("nomad-addr"),
		NomadToken:     v.GetString("nomad-token"),
		NomadRegion:    v.GetString("nomad-region"),
		NomadNamespace: v.GetString("nomad-namespace"),

		Mode:         Mode(strings.ToLower(v.GetString("mode"))),
		Prune:        v.GetBool("prune"),
		SyncInterval: v.GetDuration("sync-interval"),
		Once:         v.GetBool("once"),

		LogLevel:   v.GetString("log-level"),
		LogFormat:  v.GetString("log-format"),
		HealthAddr: v.GetString("health-addr"),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.GitURL == "" {
		return fmt.Errorf("git-url is required (set --git-url or CARAVAN_GIT_URL)")
	}
	if c.Mode != ModeSync && c.Mode != ModePlan {
		return fmt.Errorf("invalid mode %q: must be %q or %q", c.Mode, ModeSync, ModePlan)
	}
	if c.SyncInterval <= 0 {
		return fmt.Errorf("sync-interval must be positive, got %s", c.SyncInterval)
	}
	return nil
}
