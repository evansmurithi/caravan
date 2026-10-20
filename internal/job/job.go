// Package job parses Nomad job specifications into structured, caravan-managed
// jobs.
package job

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec2"

	"github.com/evansmurithi/caravan/internal/config"
)

// Desired is a job rendered from Git together with its source path.
type Desired struct {
	Job        *api.Job
	SourcePath string
}

// ID returns the job's ID.
func (d Desired) ID() string {
	if d.Job != nil && d.Job.ID != nil {
		return *d.Job.ID
	}
	return ""
}

// Namespace returns the job's namespace, or the provided fallback.
func (d Desired) Namespace(fallback string) string {
	if d.Job != nil && d.Job.Namespace != nil && *d.Job.Namespace != "" {
		return *d.Job.Namespace
	}
	return fallback
}

// ParseFile parses a single Nomad job file locally, mirroring how the
// `nomad job run` CLI parses HCL: filesystem functions such as file() are
// enabled and resolved relative to baseDir (the analog of the shell's working
// directory), and process environment variables are exposed to the env()
// function.
func ParseFile(path, baseDir string) (*api.Job, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	if baseDir == "" {
		baseDir = filepath.Dir(path)
	}

	job, err := jobspec2.ParseWithConfig(&jobspec2.ParseConfig{
		Path:    path,
		BaseDir: baseDir,
		Body:    body,
		AllowFS: true,
		Strict:  true,
		Envs:    os.Environ(),
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// StampManaged records that caravan owns this job so it can later identify and
// prune orphaned jobs.
func StampManaged(job *api.Job) {
	if job.Meta == nil {
		job.Meta = map[string]string{}
	}
	job.Meta[config.ManagedByLabel] = config.ManagedByValue
}

// IsManaged reports whether a job carries caravan's management metadata.
func IsManaged(meta map[string]string) bool {
	return meta[config.ManagedByLabel] == config.ManagedByValue
}
