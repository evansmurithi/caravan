package reconcile

import (
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func TestRenderJobDiff(t *testing.T) {
	d := &api.JobDiff{
		Type: "Edited",
		ID:   "web",
		Fields: []*api.FieldDiff{
			{Type: "Edited", Name: "Priority", Old: "50", New: "70"},
			{Type: "None", Name: "Region", Old: "global", New: "global"},
		},
		TaskGroups: []*api.TaskGroupDiff{
			{
				Type:    "Edited",
				Name:    "web",
				Updates: map[string]uint64{"in-place update": 1},
				Tasks: []*api.TaskDiff{
					{
						Type: "Edited",
						Name: "server",
						Objects: []*api.ObjectDiff{
							{
								Type: "Edited",
								Name: "Template",
								Fields: []*api.FieldDiff{
									{Type: "Edited", Name: "EmbeddedTmpl", Old: "old-config", New: "new-config"},
								},
							},
						},
					},
				},
			},
		},
	}

	out := RenderJobDiff(d, false)

	wants := []string{
		`~ Job: "web"`,
		`  ~ Priority: "50" => "70"`,
		`  ~ Task Group: "web" (1 in-place update)`,
		`    ~ Task: "server"`,
		`      ~ Template {`,
		`        ~ EmbeddedTmpl: "old-config" => "new-config"`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered diff missing %q\n---\n%s", w, out)
		}
	}
	if strings.Contains(out, "Region") {
		t.Errorf("None fields should be omitted, got:\n%s", out)
	}
}

func TestTruncEscapesNewlines(t *testing.T) {
	if got := trunc("a\nb"); got != `a\nb` {
		t.Errorf("trunc should escape newlines: got %q", got)
	}
	long := strings.Repeat("x", maxFieldValue+50)
	if got := trunc(long); !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("trunc should clip long values: got %q", got)
	}
}

func TestRenderJobDiffColor(t *testing.T) {
	d := &api.JobDiff{Type: "Added", ID: "x"}
	out := RenderJobDiff(d, true)
	if !strings.Contains(out, "\x1b[32m") {
		t.Errorf("expected green ANSI color for Added job, got: %q", out)
	}
}
