package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/config"
	"github.com/evansmurithi/caravan/internal/job"
	"github.com/evansmurithi/caravan/internal/spec"
	"github.com/evansmurithi/caravan/internal/volume"
)

const kamruiID = "11111111-1111-1111-1111-111111111111"
const odroidID = "33333333-3333-3333-3333-333333333333"

func volumeConfig() *config.Config {
	cfg := baseConfig()
	cfg.VolumeHostPathPrefixes = []string{"/mnt/data1", "/mnt/storage"}
	return cfg
}

// desiredVolume mirrors what volume.Parse produces for a spec file.
func desiredVolume(name, nodeID, hostPath string) volume.Desired {
	return volume.Desired{
		Volume: &api.HostVolume{
			Name:     name,
			NodeID:   nodeID,
			HostPath: hostPath,
			Parameters: map[string]string{
				config.ManagedByLabel: config.ManagedByValue,
				volume.SourceParam:    name + "/" + name + ".volume.hcl",
			},
		},
		SourcePath: name + ".volume.hcl",
		NodeName:   "kamrui",
	}
}

// registeredVolume is how Nomad reports a volume caravan registered earlier.
func registeredVolume(id string, d volume.Desired) *api.HostVolume {
	v := *d.Volume
	v.ID = id
	v.Namespace = "default"
	v.RequestedCapabilities = []*api.HostVolumeCapability{{
		AttachmentMode: api.HostVolumeAttachmentModeFilesystem,
		AccessMode:     api.HostVolumeAccessModeSingleNodeWriter,
	}}
	v.State = api.HostVolumeStateReady
	return &v
}

func TestReconcileRegistersVolumesBeforeJobs(t *testing.T) {
	nomad := &fakeNomad{planDiffType: map[string]string{"app": "Added"}}
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{
		Jobs:    []job.Desired{desiredJob("app")},
		Volumes: []volume.Desired{desiredVolume("app-data", kamruiID, "/mnt/data1/app")},
	})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	want := []string{"volume:app-data", "job:app"}
	if strings.Join(nomad.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("expected calls %v, got %v", want, nomad.calls)
	}
	v := nomad.volRegistered[0]
	if v.ID != "" || v.Namespace != "default" || v.NodeID != kamruiID {
		t.Fatalf("unexpected registered volume: %+v", v)
	}
	if len(res.VolumesApplied) != 1 || res.VolumesApplied[0] != "default/app-data@kamrui" {
		t.Fatalf("expected app-data applied, got %v", res.VolumesApplied)
	}
}

func TestReconcileSkipsUnchangedVolume(t *testing.T) {
	d := desiredVolume("app-data", kamruiID, "/mnt/data1/app")
	nomad := &fakeNomad{volumes: []*api.HostVolume{registeredVolume("vol-1", d)}}
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{Volumes: []volume.Desired{d}})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("unchanged volume must not be registered, got %+v", nomad.volRegistered)
	}
	if res.VolumesUnchanged != 1 {
		t.Fatalf("expected 1 unchanged volume, got %d", res.VolumesUnchanged)
	}
}

func TestReconcileUpdatesExistingVolumeByID(t *testing.T) {
	old := desiredVolume("app-data", kamruiID, "/mnt/data1/app")
	nomad := &fakeNomad{volumes: []*api.HostVolume{registeredVolume("vol-1", old)}}

	d := desiredVolume("app-data", kamruiID, "/mnt/data1/app-v2")
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{Volumes: []volume.Desired{d}})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(nomad.volRegistered) != 1 {
		t.Fatalf("expected one register call, got %d", len(nomad.volRegistered))
	}
	if got := nomad.volRegistered[0]; got.ID != "vol-1" || got.HostPath != "/mnt/data1/app-v2" {
		t.Fatalf("expected update of vol-1 with new host path, got %+v", got)
	}
}

func TestReconcileRefusesHostPathsOutsideAllowlist(t *testing.T) {
	cases := map[string]string{
		"outside":   "/etc",
		"traversal": "/mnt/data1/../../etc",
		"sibling":   "/mnt/data10/app",
		"relative":  "mnt/data1/app",
	}
	for name, hostPath := range cases {
		t.Run(name, func(t *testing.T) {
			nomad := &fakeNomad{}
			r := newSpecReconciler(volumeConfig(), nomad, spec.Set{
				Volumes: []volume.Desired{desiredVolume("bad", kamruiID, hostPath)},
			})
			res := r.reconcile(context.Background())
			if res.Err == nil {
				t.Fatalf("expected an error for host_path %q", hostPath)
			}
			if len(nomad.volRegistered) != 0 {
				t.Fatalf("refused volume must not be registered")
			}
		})
	}
}

func TestReconcileRefusesVolumesWithoutAllowlist(t *testing.T) {
	nomad := &fakeNomad{}
	r := newSpecReconciler(baseConfig(), nomad, spec.Set{
		Volumes: []volume.Desired{desiredVolume("app-data", kamruiID, "/mnt/data1/app")},
	})
	res := r.reconcile(context.Background())
	if res.Err == nil || !strings.Contains(res.Err.Error(), "CARAVAN_VOLUME_HOST_PATH_PREFIXES") {
		t.Fatalf("expected an allowlist error, got %v", res.Err)
	}
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("volume must not be registered without an allowlist")
	}
}

func TestReconcilePlanModeDoesNotRegisterVolumes(t *testing.T) {
	cfg := volumeConfig()
	cfg.Mode = config.ModePlan
	nomad := &fakeNomad{}
	r := newSpecReconciler(cfg, nomad, spec.Set{
		Volumes: []volume.Desired{desiredVolume("app-data", kamruiID, "/mnt/data1/app")},
	})

	res := r.reconcile(context.Background())
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("plan mode must not register volumes")
	}
	if len(res.VolumesPlanned) != 1 {
		t.Fatalf("expected 1 pending volume, got %v", res.VolumesPlanned)
	}
}

func TestReconcileReportsImmutableNodeChange(t *testing.T) {
	// The spec moved from kamrui to odroid; the volume on kamrui carries the
	// same source path, so registering would leave it behind.
	old := desiredVolume("app-data", kamruiID, "/mnt/data1/app")
	nomad := &fakeNomad{volumes: []*api.HostVolume{registeredVolume("vol-1", old)}}

	moved := desiredVolume("app-data", odroidID, "/mnt/data1/app")
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{Volumes: []volume.Desired{moved}})

	res := r.reconcile(context.Background())
	if res.Err == nil || !strings.Contains(res.Err.Error(), "immutable") {
		t.Fatalf("expected an immutable-field error, got %v", res.Err)
	}
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("moved volume must not be registered")
	}
}

func TestReconcileReportsPluginCreatedVolume(t *testing.T) {
	d := desiredVolume("app-data", kamruiID, "/mnt/data1/app")
	existing := registeredVolume("vol-1", d)
	existing.PluginID = "mkdir"
	nomad := &fakeNomad{volumes: []*api.HostVolume{existing}}
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{Volumes: []volume.Desired{d}})

	res := r.reconcile(context.Background())
	if res.Err == nil || !strings.Contains(res.Err.Error(), "plugin") {
		t.Fatalf("expected a plugin/type error, got %v", res.Err)
	}
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("plugin-created volume must not be re-registered")
	}
}

// TestPruneNeverTouchesVolumes checks that volumes absent from Git are left
// alone even with prune enabled. The client interface has no deregister or
// delete call, so the only observable effect would be a register.
func TestPruneNeverTouchesVolumes(t *testing.T) {
	cfg := volumeConfig()
	cfg.Prune = true
	orphan := registeredVolume("vol-orphan", desiredVolume("orphan", kamruiID, "/mnt/data1/orphan"))
	nomad := &fakeNomad{volumes: []*api.HostVolume{orphan}}
	r := newSpecReconciler(cfg, nomad, spec.Set{})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(nomad.calls) != 0 || len(res.Pruned) != 0 {
		t.Fatalf("prune must not touch volumes; calls=%v pruned=%v", nomad.calls, res.Pruned)
	}

	out, err := r.Diff(context.Background())
	if err != nil {
		t.Fatalf("unexpected diff error: %v", err)
	}
	if len(out.Volumes) != 0 || len(out.Jobs) != 0 {
		t.Fatalf("expected no plans for an orphaned volume, got %+v", out)
	}
}

func TestDiffReportsVolumePlans(t *testing.T) {
	same := desiredVolume("same", kamruiID, "/mnt/data1/same")
	nomad := &fakeNomad{volumes: []*api.HostVolume{registeredVolume("vol-1", same)}}
	r := newSpecReconciler(volumeConfig(), nomad, spec.Set{Volumes: []volume.Desired{
		same,
		desiredVolume("new", kamruiID, "/mnt/storage/new"),
	}})

	out, err := r.Diff(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nomad.volRegistered) != 0 {
		t.Fatalf("diff must not register volumes")
	}
	byName := map[string]VolumePlan{}
	for _, p := range out.Volumes {
		byName[p.Name] = p
	}
	if byName["same"].Changed() {
		t.Errorf("unchanged volume reported a change: %+v", byName["same"].Fields)
	}
	if p := byName["new"]; !p.Changed() || p.ID != "" {
		t.Errorf("new volume should be an addition, got %+v", p)
	}

	rendered := RenderVolumeDiff(byName["new"], false)
	for _, want := range []string{`+ Volume: "new" (namespace default, node kamrui)`, `+ HostPath: "/mnt/storage/new"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered diff missing %q:\n%s", want, rendered)
		}
	}
}
