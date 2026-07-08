// Package job discovers Nomad job specifications in a directory tree and turns
// them into structured, caravan-managed jobs.
package job

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

var jobExtensions = map[string]bool{
	".nomad": true,
	".hcl":   true,
}

// Discover walks root for job specification files and parses each into a
// structured job stamped with caravan's management metadata. HCL filesystem
// functions such as file() are resolved relative to baseDir, mirroring the
// working directory used with `nomad job run`. Parse errors are aggregated so a
// single bad file does not hide the rest.
func Discover(root, baseDir string) ([]Desired, error) {
	files, err := findJobFiles(root)
	if err != nil {
		return nil, err
	}

	var (
		desired []Desired
		errs    []string
		seen    = map[string]string{} // jobID -> source path
	)

	for _, f := range files {
		job, err := parseFile(f, baseDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		if job.ID == nil || *job.ID == "" {
			errs = append(errs, fmt.Sprintf("%s: job has no ID", f))
			continue
		}

		id := *job.ID
		if prev, ok := seen[id]; ok {
			errs = append(errs, fmt.Sprintf("%s: duplicate job ID %q (also defined in %s)", f, id, prev))
			continue
		}
		seen[id] = f

		stampManaged(job)
		desired = append(desired, Desired{Job: job, SourcePath: f})
	}

	sort.Slice(desired, func(i, j int) bool {
		return desired[i].ID() < desired[j].ID()
	})

	if len(errs) > 0 {
		return desired, fmt.Errorf("failed to parse %d job file(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return desired, nil
}

// parseFile parses a single Nomad job file locally, mirroring how the
// `nomad job run` CLI parses HCL: filesystem functions such as file() are
// enabled and resolved relative to baseDir (the analog of the shell's working
// directory), and process environment variables are exposed to the env()
// function.
func parseFile(path, baseDir string) (*api.Job, error) {
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

func findJobFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip the .git directory and other hidden dirs.
			if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if jobExtensions[strings.ToLower(filepath.Ext(path))] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

// stampManaged records that caravan owns this job so it can later identify and
// prune orphaned jobs.
func stampManaged(job *api.Job) {
	if job.Meta == nil {
		job.Meta = map[string]string{}
	}
	job.Meta[config.ManagedByLabel] = config.ManagedByValue
}

// IsManaged reports whether a job carries caravan's management metadata.
func IsManaged(meta map[string]string) bool {
	return meta[config.ManagedByLabel] == config.ManagedByValue
}
