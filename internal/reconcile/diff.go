package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/job"
)

// JobPlan pairs a job with the diff computed against the cluster during a
// read-only diff pass.
type JobPlan struct {
	ID        string
	Namespace string
	Source    string       // path to the job file in Git ("" for orphans)
	Diff      *api.JobDiff // nil when Err is set
	Orphan    bool         // job is managed by caravan but no longer in Git
	Err       error        // per-job plan/list error
}

// Changed reports whether the plan represents a pending change.
func (p JobPlan) Changed() bool {
	if p.Err != nil || p.Orphan {
		return true
	}
	return p.Diff != nil && p.Diff.Type != "None"
}

// Diff syncs Git, renders the jobs, and plans each against the cluster without
// applying anything. When prune is enabled, managed jobs missing from Git are
// returned as orphans. The returned error carries any job-discovery problems;
// per-job plan errors are attached to the individual JobPlan entries.
func (r *Reconciler) Diff(ctx context.Context) (commit string, plans []JobPlan, err error) {
	commit, err = r.git.Sync()
	if err != nil {
		return "", nil, fmt.Errorf("git sync: %w", err)
	}

	root, baseDir := r.paths()
	desired, discoverErr := r.filesR(root, baseDir)

	desiredIDs := make(map[nsKey]bool, len(desired))
	for _, d := range desired {
		if err := ctx.Err(); err != nil {
			return commit, plans, err
		}
		ns := d.Namespace(r.cfg.NomadNamespace)
		desiredIDs[nsKey{ns, d.ID()}] = true

		p := JobPlan{ID: d.ID(), Namespace: ns, Source: d.SourcePath}
		if resp, e := r.nomad.Plan(d.Job); e != nil {
			p.Err = e
		} else {
			p.Diff = resp.Diff
		}
		plans = append(plans, p)
	}

	if r.cfg.Prune {
		plans = append(plans, r.orphans(ctx, desiredIDs)...)
	}

	return commit, plans, discoverErr
}

// orphans lists managed jobs that are present in the cluster but absent from
// Git, without stopping them.
func (r *Reconciler) orphans(ctx context.Context, desired map[nsKey]bool) []JobPlan {
	namespaces := map[string]bool{r.cfg.NomadNamespace: true}
	for k := range desired {
		namespaces[k.ns] = true
	}

	var out []JobPlan
	for ns := range namespaces {
		if ctx.Err() != nil {
			return out
		}
		stubs, err := r.nomad.List(ns)
		if err != nil {
			out = append(out, JobPlan{Namespace: ns, Err: err})
			continue
		}
		for _, stub := range stubs {
			if !job.IsManaged(stub.Meta) || stub.Stop {
				continue
			}
			if desired[nsKey{ns, stub.ID}] {
				continue
			}
			out = append(out, JobPlan{ID: stub.ID, Namespace: ns, Orphan: true})
		}
	}
	return out
}

// summarizeDiff renders a compact, human-readable summary of a Nomad job diff
// suitable for a single log line.
func summarizeDiff(d *api.JobDiff) string {
	if d == nil {
		return "unknown"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("job=%s", d.Type))

	if n := len(d.Fields); n > 0 {
		parts = append(parts, fmt.Sprintf("fields=%d", n))
	}

	for _, tg := range d.TaskGroups {
		if tg == nil || tg.Type == "None" {
			continue
		}
		tgPart := fmt.Sprintf("%s:%s", tg.Name, tg.Type)
		if tasks := changedTasks(tg); tasks != "" {
			tgPart += "(" + tasks + ")"
		}
		parts = append(parts, "group="+tgPart)
	}

	return strings.Join(parts, " ")
}

func changedTasks(tg *api.TaskGroupDiff) string {
	var names []string
	for _, t := range tg.Tasks {
		if t == nil || t.Type == "None" {
			continue
		}
		names = append(names, fmt.Sprintf("%s:%s", t.Name, t.Type))
	}
	return strings.Join(names, ",")
}
