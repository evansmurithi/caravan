package spec

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/volume"
)

const appJob = `job "app" {
  group "g" {
    volume "data" {
      type   = "host"
      source = "app-data"
    }
    task "t" {
      driver = "docker"
      config { image = "busybox" }
    }
  }
}
`

const appVolume = `type      = "host"
name      = "app-data"
node_id   = node("kamrui")
host_path = "/mnt/data1/app"
`

func kamrui() volume.NodeResolver {
	return volume.NewNodes(func() ([]*api.NodeListStub, error) {
		return []*api.NodeListStub{{ID: "node-kamrui", Name: "kamrui", Status: "ready"}}, nil
	})
}

// TestDiscoverClassifiesJobsAndVolumes mirrors a repo layout where a volume
// spec sits next to the job that claims it.
func TestDiscoverClassifiesJobsAndVolumes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "app", "app.nomad"), appJob)
	mustWrite(t, filepath.Join(root, "app", "app-data.volume.hcl"), appVolume)

	set, err := Discover(root, root, kamrui())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(set.Jobs) != 1 || set.Jobs[0].ID() != "app" {
		t.Fatalf("expected job 'app', got %+v", set.Jobs)
	}
	if len(set.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(set.Volumes))
	}
	v := set.Volumes[0]
	if v.Volume.NodeID != "node-kamrui" || v.Volume.Parameters[volume.SourceParam] != "app/app-data.volume.hcl" {
		t.Fatalf("unexpected volume: %+v", v.Volume)
	}
}

func TestDiscoverRejectsUnknownFiles(t *testing.T) {
	cases := map[string]string{
		"neither":     "foo = \"bar\"\n",
		"csi volume":  "type = \"csi\"\nname = \"x\"\n",
		"both":        "type = \"host\"\n" + appJob,
		"syntax":      "job \"broken\" {\n",
		"dynamic typ": "type = lower(\"HOST\")\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			mustWrite(t, filepath.Join(root, "x.hcl"), src)
			mustWrite(t, filepath.Join(root, "app.nomad"), appJob)

			set, err := Discover(root, root, kamrui())
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
			if !strings.Contains(err.Error(), "x.hcl") {
				t.Fatalf("error should name the bad file: %v", err)
			}
			if len(set.Jobs) != 1 {
				t.Fatalf("a bad file must not hide valid jobs, got %+v", set.Jobs)
			}
		})
	}
}

func TestDiscoverRejectsDuplicateVolumes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.hcl"), appVolume)
	mustWrite(t, filepath.Join(root, "b.hcl"), appVolume)

	_, err := Discover(root, root, kamrui())
	if err == nil || !strings.Contains(err.Error(), "duplicate volume") {
		t.Fatalf("expected duplicate volume error, got %v", err)
	}
}
