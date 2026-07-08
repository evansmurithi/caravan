package job

import (
	"os"
	"path/filepath"
	"testing"
)

const jobWithFileFunc = `job "example" {
  group "g" {
    task "t" {
      driver = "docker"

      config {
        image = "busybox"
      }

      template {
        data        = file("config.txt")
        destination = "local/config.txt"
      }
    }
  }
}
`

// TestDiscoverParsesFileFunction verifies caravan parses HCL locally with
// filesystem functions enabled, resolving file() relative to the job file.
func TestDiscoverParsesFileFunction(t *testing.T) {
	dir := t.TempDir()
	const contents = "hello from a sidecar config file"

	if err := os.WriteFile(filepath.Join(dir, "config.txt"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "example.nomad"), []byte(jobWithFileFunc), 0o644); err != nil {
		t.Fatal(err)
	}

	desired, err := Discover(dir, dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(desired) != 1 {
		t.Fatalf("expected 1 job, got %d", len(desired))
	}

	d := desired[0]
	if d.ID() != "example" {
		t.Fatalf("expected job ID 'example', got %q", d.ID())
	}
	if !IsManaged(d.Job.Meta) {
		t.Fatalf("expected job to be stamped as managed")
	}

	tmpl := d.Job.TaskGroups[0].Tasks[0].Templates[0]
	if tmpl.EmbeddedTmpl == nil || *tmpl.EmbeddedTmpl != contents {
		t.Fatalf("file() was not resolved; got template data %v", tmpl.EmbeddedTmpl)
	}
}

const prometheusJob = `job "prometheus" {
  group "g" {
    task "t" {
      driver = "docker"
      config { image = "prom/prometheus" }
      template {
        data        = file("prometheus/alerts.yaml")
        destination = "local/alerts.yaml"
      }
    }
  }
}
`

const alloyJob = `job "alloy" {
  group "g" {
    task "t" {
      driver = "docker"
      config { image = "grafana/alloy" }
      template {
        data        = file("alloy/configs/agent.alloy")
        destination = "local/agent.alloy"
      }
    }
  }
}
`

// TestDiscoverResolvesFilesAgainstBaseDir mirrors a real repo layout (the
// homelab-infra convention) where jobs live in per-app subdirectories and
// reference their config files with paths relative to the scan root — the
// directory you'd `cd` into before running `nomad job run`:
//
//	nomad/                       <- baseDir / scan root
//	├── prometheus/prometheus.nomad  # file("prometheus/alerts.yaml")
//	├── prometheus/alerts.yaml
//	├── alloy/alloy.nomad            # file("alloy/configs/agent.alloy")
//	└── alloy/configs/agent.alloy
func TestDiscoverResolvesFilesAgainstBaseDir(t *testing.T) {
	root := t.TempDir()

	alerts := "groups: [{name: example}]"
	alloyCfg := `logging { level = "info" }`

	mustWrite(t, filepath.Join(root, "prometheus", "prometheus.nomad"), prometheusJob)
	mustWrite(t, filepath.Join(root, "prometheus", "alerts.yaml"), alerts)
	mustWrite(t, filepath.Join(root, "alloy", "alloy.nomad"), alloyJob)
	mustWrite(t, filepath.Join(root, "alloy", "configs", "agent.alloy"), alloyCfg)

	// Scan the whole tree, resolving file() against the scan root.
	desired, err := Discover(root, root)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(desired) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(desired))
	}

	got := map[string]string{}
	for _, d := range desired {
		got[d.ID()] = *d.Job.TaskGroups[0].Tasks[0].Templates[0].EmbeddedTmpl
	}

	if got["prometheus"] != alerts {
		t.Fatalf("prometheus alerts.yaml not inlined: %q", got["prometheus"])
	}
	if got["alloy"] != alloyCfg {
		t.Fatalf("alloy configs/agent.alloy not inlined: %q", got["alloy"])
	}
}

const jobWithNamespace = `job "with-ns" {
  namespace = "monitoring"
  group "g" {
    task "t" {
      driver = "docker"
      config { image = "busybox" }
    }
  }
}
`

const jobWithoutNamespace = `job "no-ns" {
  group "g" {
    task "t" {
      driver = "docker"
      config { image = "busybox" }
    }
  }
}
`

// TestNamespaceComesFromJobSpec verifies that a job's own namespace field is
// honored, and that a job without one leaves Namespace unset so the configured
// fallback applies (the HCL parser must not force "default").
func TestNamespaceComesFromJobSpec(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "with-ns.nomad"), jobWithNamespace)
	mustWrite(t, filepath.Join(dir, "no-ns.nomad"), jobWithoutNamespace)

	desired, err := Discover(dir, dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	byID := map[string]Desired{}
	for _, d := range desired {
		byID[d.ID()] = d
	}

	withNS := byID["with-ns"]
	if withNS.Job.Namespace == nil || *withNS.Job.Namespace != "monitoring" {
		t.Fatalf("expected job namespace 'monitoring', got %v", withNS.Job.Namespace)
	}
	if got := withNS.Namespace("default"); got != "monitoring" {
		t.Fatalf("expected resolved namespace 'monitoring', got %q", got)
	}

	noNS := byID["no-ns"]
	if noNS.Job.Namespace != nil && *noNS.Job.Namespace != "" {
		t.Fatalf("expected unset namespace so fallback applies, got %q", *noNS.Job.Namespace)
	}
	if got := noNS.Namespace("staging"); got != "staging" {
		t.Fatalf("expected fallback namespace 'staging', got %q", got)
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
