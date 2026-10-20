package reconcile

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/config"
	"github.com/evansmurithi/caravan/internal/job"
	"github.com/evansmurithi/caravan/internal/spec"
	"github.com/evansmurithi/caravan/internal/volume"
)

type fakeGit struct{ commit string }

func (f *fakeGit) Sync() (string, error) { return f.commit, nil }
func (f *fakeGit) Dir() string           { return "/tmp/does-not-matter" }

type fakeNomad struct {
	// planDiffType maps job ID -> diff type returned by Plan.
	planDiffType map[string]string
	// existing is what List returns.
	existing []*api.JobListStub

	// nodes is what Nodes returns; volumes is the cluster's host volumes.
	nodes   []*api.NodeListStub
	volumes []*api.HostVolume

	registered []string
	stopped    []string
	// volRegistered holds every volume passed to RegisterHostVolume.
	volRegistered []*api.HostVolume
	// calls records the order of write calls ("volume:<name>", "job:<id>").
	calls []string
}

func (f *fakeNomad) Plan(j *api.Job) (*api.JobPlanResponse, error) {
	t := f.planDiffType[*j.ID]
	if t == "" {
		t = "None"
	}
	return &api.JobPlanResponse{Diff: &api.JobDiff{Type: t}}, nil
}

func (f *fakeNomad) Register(j *api.Job) (*api.JobRegisterResponse, error) {
	f.registered = append(f.registered, *j.ID)
	f.calls = append(f.calls, "job:"+*j.ID)
	return &api.JobRegisterResponse{EvalID: "eval-" + *j.ID}, nil
}

func (f *fakeNomad) List(string) ([]*api.JobListStub, error) { return f.existing, nil }

func (f *fakeNomad) Stop(jobID, _ string, _ bool) error {
	f.stopped = append(f.stopped, jobID)
	return nil
}

func (f *fakeNomad) Nodes() ([]*api.NodeListStub, error) { return f.nodes, nil }

func (f *fakeNomad) HostVolumes(ns string) ([]*api.HostVolumeStub, error) {
	var out []*api.HostVolumeStub
	for _, v := range f.volumes {
		if v.Namespace != ns {
			continue
		}
		out = append(out, &api.HostVolumeStub{
			Namespace: v.Namespace, ID: v.ID, Name: v.Name,
			PluginID: v.PluginID, NodeID: v.NodeID,
		})
	}
	return out, nil
}

func (f *fakeNomad) HostVolume(id, _ string) (*api.HostVolume, error) {
	for _, v := range f.volumes {
		if v.ID == id {
			return v, nil
		}
	}
	return nil, fmt.Errorf("volume %s not found", id)
}

func (f *fakeNomad) RegisterHostVolume(v *api.HostVolume) (*api.HostVolumeRegisterResponse, error) {
	f.volRegistered = append(f.volRegistered, v)
	f.calls = append(f.calls, "volume:"+v.Name)
	return &api.HostVolumeRegisterResponse{Volume: v}, nil
}

func desiredJob(id string) job.Desired {
	return job.Desired{
		Job:        &api.Job{ID: strptr(id), Meta: map[string]string{config.ManagedByLabel: config.ManagedByValue}},
		SourcePath: id + ".nomad.hcl",
	}
}

func strptr(s string) *string { return &s }

func newTestReconciler(cfg *config.Config, nomad NomadClient, desired []job.Desired) *Reconciler {
	return newSpecReconciler(cfg, nomad, spec.Set{Jobs: desired})
}

func newSpecReconciler(cfg *config.Config, nomad NomadClient, set spec.Set) *Reconciler {
	r := New(cfg, &fakeGit{commit: "abc123"}, nomad, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.discover = func(string, string, volume.NodeResolver) (spec.Set, error) { return set, nil }
	return r
}

func baseConfig() *config.Config {
	return &config.Config{
		Mode:           config.ModeSync,
		NomadNamespace: "default",
		SyncInterval:   time.Minute,
		Once:           true,
	}
}

func TestReconcileAppliesChangedJobsOnly(t *testing.T) {
	nomad := &fakeNomad{planDiffType: map[string]string{
		"changed":   "Edited",
		"unchanged": "None",
	}}
	r := newTestReconciler(baseConfig(), nomad, []job.Desired{
		desiredJob("changed"),
		desiredJob("unchanged"),
	})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}

	if len(nomad.registered) != 1 || nomad.registered[0] != "changed" {
		t.Fatalf("expected only 'changed' registered, got %v", nomad.registered)
	}
	if res.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", res.Unchanged)
	}
}

func TestReconcilePlanModeDoesNotApply(t *testing.T) {
	cfg := baseConfig()
	cfg.Mode = config.ModePlan

	nomad := &fakeNomad{planDiffType: map[string]string{"changed": "Edited"}}
	r := newTestReconciler(cfg, nomad, []job.Desired{desiredJob("changed")})

	res := r.reconcile(context.Background())
	if len(nomad.registered) != 0 {
		t.Fatalf("plan mode must not register jobs, got %v", nomad.registered)
	}
	if len(res.Planned) != 1 {
		t.Fatalf("expected 1 pending job, got %v", res.Planned)
	}
}

func TestDiffReturnsPlansAndOrphans(t *testing.T) {
	cfg := baseConfig()
	cfg.Prune = true

	managed := map[string]string{config.ManagedByLabel: config.ManagedByValue}
	nomad := &fakeNomad{
		planDiffType: map[string]string{"changed": "Edited", "unchanged": "None"},
		existing: []*api.JobListStub{
			{ID: "changed", Meta: managed},
			{ID: "orphan", Meta: managed},
			{ID: "not-managed", Meta: map[string]string{}},
		},
	}
	r := newTestReconciler(cfg, nomad, []job.Desired{
		desiredJob("changed"),
		desiredJob("unchanged"),
	})

	out, err := r.Diff(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Commit != "abc123" {
		t.Fatalf("expected commit abc123, got %q", out.Commit)
	}

	byID := map[string]JobPlan{}
	for _, p := range out.Jobs {
		byID[p.ID] = p
	}

	if !byID["changed"].Changed() {
		t.Errorf("changed job should report a change")
	}
	if byID["unchanged"].Changed() {
		t.Errorf("unchanged job should not report a change")
	}
	if o, ok := byID["orphan"]; !ok || !o.Orphan || !o.Changed() {
		t.Errorf("orphan job should be reported as an orphan change, got %+v", o)
	}
	if _, ok := byID["not-managed"]; ok {
		t.Errorf("jobs not managed by caravan must not appear as orphans")
	}
}

func TestReconcilePrunesOrphans(t *testing.T) {
	cfg := baseConfig()
	cfg.Prune = true

	nomad := &fakeNomad{
		planDiffType: map[string]string{"keep": "None"},
		existing: []*api.JobListStub{
			{ID: "keep", Meta: map[string]string{config.ManagedByLabel: config.ManagedByValue}},
			{ID: "orphan", Meta: map[string]string{config.ManagedByLabel: config.ManagedByValue}},
			{ID: "not-managed", Meta: map[string]string{}},
		},
	}
	r := newTestReconciler(cfg, nomad, []job.Desired{desiredJob("keep")})

	res := r.reconcile(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(nomad.stopped) != 1 || nomad.stopped[0] != "orphan" {
		t.Fatalf("expected only 'orphan' pruned, got %v", nomad.stopped)
	}
}
