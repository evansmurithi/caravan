package reconcile

import (
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// maxFieldValue bounds how much of a single field value is printed so large
// values (such as inlined template contents) don't flood the diff output.
const maxFieldValue = 200

// RenderJobDiff renders a Nomad job diff as an indented, +/- annotated tree
// similar to `nomad plan`. When color is true, ANSI colors are applied.
func RenderJobDiff(d *api.JobDiff, color bool) string {
	if d == nil {
		return ""
	}
	p := painter{enabled: color}
	var b strings.Builder

	writeLine(&b, p, 0, d.Type, fmt.Sprintf("Job: %q", d.ID))
	writeFields(&b, p, 1, d.Fields)
	writeObjects(&b, p, 1, d.Objects)

	for _, tg := range d.TaskGroups {
		if tg == nil || tg.Type == "None" {
			continue
		}
		header := fmt.Sprintf("Task Group: %q", tg.Name)
		if u := updatesSummary(tg.Updates); u != "" {
			header += " (" + u + ")"
		}
		writeLine(&b, p, 1, tg.Type, header)
		writeFields(&b, p, 2, tg.Fields)
		writeObjects(&b, p, 2, tg.Objects)

		for _, t := range tg.Tasks {
			if t == nil || t.Type == "None" {
				continue
			}
			writeLine(&b, p, 2, t.Type, fmt.Sprintf("Task: %q", t.Name))
			writeFields(&b, p, 3, t.Fields)
			writeObjects(&b, p, 3, t.Objects)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func writeFields(b *strings.Builder, p painter, indent int, fields []*api.FieldDiff) {
	for _, f := range fields {
		if f == nil || f.Type == "None" {
			continue
		}
		var text string
		switch f.Type {
		case "Added":
			text = fmt.Sprintf("%s: %q", f.Name, trunc(f.New))
		case "Deleted":
			text = fmt.Sprintf("%s: %q", f.Name, trunc(f.Old))
		case "Edited":
			text = fmt.Sprintf("%s: %q => %q", f.Name, trunc(f.Old), trunc(f.New))
		default:
			text = fmt.Sprintf("%s: %q", f.Name, trunc(f.New))
		}
		writeLine(b, p, indent, f.Type, text)
	}
}

func writeObjects(b *strings.Builder, p painter, indent int, objects []*api.ObjectDiff) {
	for _, o := range objects {
		if o == nil || o.Type == "None" {
			continue
		}
		writeLine(b, p, indent, o.Type, o.Name+" {")
		writeFields(b, p, indent+1, o.Fields)
		writeObjects(b, p, indent+1, o.Objects)
	}
}

func writeLine(b *strings.Builder, p painter, indent int, diffType, text string) {
	line := strings.Repeat("  ", indent) + marker(diffType) + " " + text
	fmt.Fprintln(b, p.paint(colorCode(diffType), line))
}

func marker(diffType string) string {
	switch diffType {
	case "Added":
		return "+"
	case "Deleted":
		return "-"
	case "Edited":
		return "~"
	default:
		return " "
	}
}

func colorCode(diffType string) string {
	switch diffType {
	case "Added":
		return "32" // green
	case "Deleted":
		return "31" // red
	case "Edited":
		return "33" // yellow
	default:
		return ""
	}
}

// trunc collapses newlines and clips long values for single-line display.
func trunc(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > maxFieldValue {
		return s[:maxFieldValue] + "…(truncated)"
	}
	return s
}

func updatesSummary(updates map[string]uint64) string {
	if len(updates) == 0 {
		return ""
	}
	var parts []string
	// Stable, meaningful ordering of the common update kinds.
	for _, k := range []string{"ignore", "create", "destroy", "in-place update", "create/destroy update", "canary"} {
		if v, ok := updates[k]; ok && v > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", v, k))
		}
	}
	return strings.Join(parts, ", ")
}

type painter struct{ enabled bool }

func (p painter) paint(code, s string) string {
	if !p.enabled || code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
