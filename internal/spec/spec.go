// Package spec discovers Nomad specifications in a directory tree. Each file
// is classified by its content: a top-level job block is a job, and a
// top-level type = "host" attribute is a dynamic host volume. Anything else is
// an error.
package spec

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/evansmurithi/caravan/internal/job"
	"github.com/evansmurithi/caravan/internal/volume"
)

// Set is everything discovered under a root.
type Set struct {
	Jobs    []job.Desired
	Volumes []volume.Desired
}

var extensions = map[string]bool{
	".nomad": true,
	".hcl":   true,
}

// Discover walks root for .nomad and .hcl files and parses each into a
// caravan-managed job or host volume. HCL filesystem functions such as file()
// in jobs are resolved relative to baseDir, mirroring the working directory
// used with `nomad job run`; node() in volume specs is resolved through nodes.
// Errors are aggregated so a single bad file does not hide the rest.
func Discover(root, baseDir string, nodes volume.NodeResolver) (Set, error) {
	files, err := findFiles(root)
	if err != nil {
		return Set{}, err
	}

	var (
		set      Set
		errs     []string
		seenJobs = map[string]string{} // jobID -> source path
		seenVols = map[string]string{} // namespace/name/node -> source path
	)

	for _, f := range files {
		body, isVolume, err := classify(f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		}

		if isVolume {
			rel, err := filepath.Rel(root, f)
			if err != nil {
				rel = f
			}
			d, err := volume.Parse(f, body, rel, nodes)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", f, err))
				continue
			}
			key := d.Volume.Namespace + "/" + d.Volume.Name + "/" + d.Volume.NodeID
			if prev, ok := seenVols[key]; ok {
				errs = append(errs, fmt.Sprintf("%s: duplicate volume %q on node %s (also defined in %s)", f, d.Name(), d.Node(), prev))
				continue
			}
			seenVols[key] = f
			set.Volumes = append(set.Volumes, d)
			continue
		}

		j, err := job.ParseFile(f, baseDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		if j.ID == nil || *j.ID == "" {
			errs = append(errs, fmt.Sprintf("%s: job has no ID", f))
			continue
		}
		id := *j.ID
		if prev, ok := seenJobs[id]; ok {
			errs = append(errs, fmt.Sprintf("%s: duplicate job ID %q (also defined in %s)", f, id, prev))
			continue
		}
		seenJobs[id] = f
		job.StampManaged(j)
		set.Jobs = append(set.Jobs, job.Desired{Job: j, SourcePath: f})
	}

	sort.Slice(set.Jobs, func(i, j int) bool {
		return set.Jobs[i].ID() < set.Jobs[j].ID()
	})
	sort.SliceStable(set.Volumes, func(i, j int) bool {
		a, b := set.Volumes[i], set.Volumes[j]
		if a.Name() != b.Name() {
			return a.Name() < b.Name()
		}
		return a.Node() < b.Node()
	})

	if len(errs) > 0 {
		return set, fmt.Errorf("failed to parse %d spec file(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return set, nil
}

// classify parses path as native HCL and reports whether it is a host volume
// spec (returning its body for decoding) or a job.
func classify(path string) (hcl.Body, bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("reading file: %w", err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false, diags
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil, false, fmt.Errorf("unexpected HCL body type %T", f.Body)
	}

	hasJob := false
	for _, b := range body.Blocks {
		if b.Type == "job" {
			hasJob = true
			break
		}
	}
	isVolume, err := volume.IsVolumeSpec(body)
	if err != nil {
		return nil, false, err
	}

	switch {
	case hasJob && isVolume:
		return nil, false, fmt.Errorf("file has both a top-level job block and a volume type attribute")
	case hasJob:
		return body, false, nil
	case isVolume:
		return body, true, nil
	}
	return nil, false, fmt.Errorf("not a job (no top-level job block) or a host volume (no top-level type = \"host\")")
}

func findFiles(root string) ([]string, error) {
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
		if extensions[strings.ToLower(filepath.Ext(path))] {
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
