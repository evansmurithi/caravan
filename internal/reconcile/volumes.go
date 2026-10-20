package reconcile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/config"
	"github.com/evansmurithi/caravan/internal/volume"
)

// VolumePlan is the pending change for one host volume in Git. Caravan only
// ever registers or updates volumes; it never deregisters or deletes them, so
// there are no orphan plans even with prune enabled.
type VolumePlan struct {
	Name      string
	Namespace string
	Node      string // node name from node(), or the literal node ID
	Source    string // path to the volume spec in Git
	ID        string // existing volume ID; "" when the volume is new
	Fields    []*api.FieldDiff
	Err       error

	volume *api.HostVolume // what a sync would register
}

// Changed reports whether the plan represents a pending change.
func (p VolumePlan) Changed() bool {
	return p.Err != nil || len(p.Fields) > 0
}

// Label identifies the volume in logs and status output.
func (p VolumePlan) Label() string {
	return fmt.Sprintf("%s/%s@%s", p.Namespace, p.Name, p.Node)
}

// planVolumes compares each desired volume with the cluster. A volume is
// matched by namespace, name and node; when found, its ID is injected so that
// registering updates it in place instead of conflicting.
func (r *Reconciler) planVolumes(ctx context.Context, desired []volume.Desired) []VolumePlan {
	lookup := &volumeLookup{
		nomad: r.nomad,
		lists: map[string][]*api.HostVolumeStub{},
		vols:  map[string]*api.HostVolume{},
	}

	plans := make([]VolumePlan, 0, len(desired))
	for _, d := range desired {
		if ctx.Err() != nil {
			break
		}
		ns := d.Namespace(r.cfg.NomadNamespace)
		p := VolumePlan{Name: d.Name(), Namespace: ns, Node: d.Node(), Source: d.SourcePath}
		p.Err = r.planVolume(lookup, d, &p)
		plans = append(plans, p)
	}
	return plans
}

func (r *Reconciler) planVolume(lookup *volumeLookup, d volume.Desired, p *VolumePlan) error {
	vol := *d.Volume
	vol.Namespace = p.Namespace

	hostPath, err := volume.ValidateHostPath(vol.HostPath, r.cfg.VolumeHostPathPrefixes)
	if err != nil {
		return err
	}
	vol.HostPath = hostPath

	stubs, err := lookup.list(p.Namespace)
	if err != nil {
		return err
	}
	var matches []*api.HostVolumeStub
	for _, s := range stubs {
		if s.Name == vol.Name && s.NodeID == vol.NodeID {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		if err := lookup.checkMoved(&vol, stubs); err != nil {
			return err
		}
		p.Fields = volume.Diff(&vol, nil)
	case 1:
		existing, err := lookup.get(matches[0].ID, p.Namespace)
		if err != nil {
			return err
		}
		if existing.PluginID != "" {
			return fmt.Errorf("existing volume %s was created by plugin %q; caravan only manages registered volumes (type cannot change)", existing.ID, existing.PluginID)
		}
		vol.ID = existing.ID
		p.ID = existing.ID
		p.Fields = volume.Diff(&vol, existing)
	default:
		return fmt.Errorf("%d volumes named %q on node %s; resolve the duplicates manually", len(matches), vol.Name, vol.NodeID)
	}

	p.volume = &vol
	return nil
}

func (r *Reconciler) applyVolume(log *slog.Logger, p VolumePlan, res *Result) {
	vlog := log.With("volume", p.Name, "namespace", p.Namespace, "node", p.Node, "source", p.Source)

	if p.Err != nil {
		vlog.Error("volume plan failed", "error", p.Err)
		if res.Err == nil {
			res.Err = fmt.Errorf("volume %s: %w", p.Label(), p.Err)
		}
		return
	}
	if !p.Changed() {
		res.VolumesUnchanged++
		vlog.Debug("no changes")
		return
	}

	summary := summarizeVolume(p)
	res.VolumesPlanned = append(res.VolumesPlanned, p.Label())

	if r.cfg.Mode == config.ModePlan {
		vlog.Info("volume changes detected (plan mode, not applying)", "diff", summary)
		return
	}

	resp, err := r.nomad.RegisterHostVolume(p.volume)
	if err != nil {
		vlog.Error("volume register failed", "error", err)
		if res.Err == nil {
			res.Err = err
		}
		return
	}
	res.VolumesApplied = append(res.VolumesApplied, p.Label())
	args := []any{"diff", summary}
	if resp.Volume != nil {
		args = append(args, "id", resp.Volume.ID)
	}
	if resp.Warnings != "" {
		args = append(args, "warnings", resp.Warnings)
	}
	vlog.Info("registered volume", args...)
}

func summarizeVolume(p VolumePlan) string {
	kind := "Edited"
	if p.ID == "" {
		kind = "Added"
	}
	return fmt.Sprintf("volume=%s fields=%d", kind, len(p.Fields))
}

// volumeLookup caches volume lists and reads for one pass.
type volumeLookup struct {
	nomad NomadClient
	lists map[string][]*api.HostVolumeStub
	vols  map[string]*api.HostVolume
}

func (l *volumeLookup) list(ns string) ([]*api.HostVolumeStub, error) {
	if stubs, ok := l.lists[ns]; ok {
		return stubs, nil
	}
	stubs, err := l.nomad.HostVolumes(ns)
	if err != nil {
		return nil, err
	}
	l.lists[ns] = stubs
	return stubs, nil
}

func (l *volumeLookup) get(id, ns string) (*api.HostVolume, error) {
	if v, ok := l.vols[id]; ok {
		return v, nil
	}
	v, err := l.nomad.HostVolume(id, ns)
	if err != nil {
		return nil, err
	}
	l.vols[id] = v
	return v, nil
}

// checkMoved reports an error when a volume registered from the same spec
// file already exists under another name or node. Nomad cannot change a
// volume's name or node, and caravan never deregisters, so registering would
// leave the old volume behind.
func (l *volumeLookup) checkMoved(vol *api.HostVolume, stubs []*api.HostVolumeStub) error {
	source := vol.Parameters[volume.SourceParam]
	for _, s := range stubs {
		if s.PluginID != "" {
			continue
		}
		existing, err := l.get(s.ID, vol.Namespace)
		if err != nil {
			return err
		}
		if existing.Parameters[volume.SourceParam] != source {
			continue
		}
		return fmt.Errorf("%s is already registered as volume %q on node %s (ID %s); name, type and node_id are immutable, so deregister that volume manually first",
			source, existing.Name, existing.NodeID, existing.ID)
	}
	return nil
}
