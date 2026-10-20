package volume

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/nomad/api"

	"github.com/evansmurithi/caravan/internal/config"
)

func parse(t *testing.T, src string, nodes NodeResolver) (Desired, error) {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(src), "test.volume.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("hcl: %v", diags)
	}
	return Parse("nomad/app/app.volume.hcl", f.Body, "app/app.volume.hcl", nodes)
}

func staticNodes(stubs ...*api.NodeListStub) *Nodes {
	return NewNodes(func() ([]*api.NodeListStub, error) { return stubs, nil })
}

func TestParseResolvesNodeFunction(t *testing.T) {
	src := `
type      = "host"
name      = "app-data"
namespace = "apps"
node_id   = node("kamrui")
host_path = "/mnt/data1/app"
capacity  = "10GiB"

capability {
  access_mode     = "single-node-writer"
  attachment_mode = "file-system"
}

parameters {
  owner = "app"
}
`
	nodes := staticNodes(
		&api.NodeListStub{ID: "node-kamrui", Name: "kamrui", Status: "ready"},
		&api.NodeListStub{ID: "node-odroid", Name: "odroid", Status: "ready"},
	)
	d, err := parse(t, src, nodes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := d.Volume
	if v.NodeID != "node-kamrui" || d.NodeName != "kamrui" || d.Node() != "kamrui" {
		t.Fatalf("node() not resolved: id=%q name=%q", v.NodeID, d.NodeName)
	}
	if v.Name != "app-data" || v.Namespace != "apps" || v.HostPath != "/mnt/data1/app" {
		t.Fatalf("unexpected volume: %+v", v)
	}
	if v.CapacityBytes != 10*1024*1024*1024 {
		t.Fatalf("expected 10GiB capacity, got %d", v.CapacityBytes)
	}
	if len(v.RequestedCapabilities) != 1 || v.RequestedCapabilities[0].AccessMode != api.HostVolumeAccessModeSingleNodeWriter {
		t.Fatalf("unexpected capabilities: %+v", v.RequestedCapabilities)
	}
	if v.Parameters["owner"] != "app" ||
		v.Parameters[config.ManagedByLabel] != config.ManagedByValue ||
		v.Parameters[SourceParam] != "app/app.volume.hcl" {
		t.Fatalf("unexpected parameters: %v", v.Parameters)
	}
}

func TestParseAcceptsLiteralNodeID(t *testing.T) {
	src := `
type      = "host"
name      = "app-data"
node_id   = "8f1a3e4c-0000-0000-0000-000000000000"
host_path = "/mnt/data1/app"
`
	d, err := parse(t, src, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Volume.NodeID != "8f1a3e4c-0000-0000-0000-000000000000" || d.NodeName != "" {
		t.Fatalf("literal node_id not kept: %+v", d)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"plugin create mode": {`
type      = "host"
name      = "x"
node_id   = "n"
host_path = "/mnt/data1/x"
plugin_id = "mkdir"
`, "create mode"},
		"missing host_path": {`
type    = "host"
name    = "x"
node_id = "n"
`, "host_path is required"},
		"unknown node": {`
type      = "host"
name      = "x"
node_id   = node("nope")
host_path = "/mnt/data1/x"
`, "no node with that name"},
		"unsupported argument": {`
type         = "host"
name         = "x"
node_id      = "n"
host_path    = "/mnt/data1/x"
capacity_min = "1GiB"
`, "capacity_min"},
		"two parameters blocks": {`
type      = "host"
name      = "x"
node_id   = "n"
host_path = "/mnt/data1/x"
parameters {
  a = "1"
}
parameters {
  b = "2"
}
`, "only one parameters block"},
	}
	nodes := staticNodes(&api.NodeListStub{ID: "n1", Name: "kamrui", Status: "ready"})
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parse(t, tc.src, nodes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNodesResolver(t *testing.T) {
	nodes := staticNodes(
		&api.NodeListStub{ID: "old", Name: "kamrui", Status: api.NodeStatusDown},
		&api.NodeListStub{ID: "new", Name: "kamrui", Status: api.NodeStatusReady},
		&api.NodeListStub{ID: "a", Name: "twin", Status: api.NodeStatusReady},
		&api.NodeListStub{ID: "b", Name: "twin", Status: api.NodeStatusReady},
		&api.NodeListStub{ID: "gone", Name: "retired", Status: api.NodeStatusDown},
	)

	if id, err := nodes.NodeID("kamrui"); err != nil || id != "new" {
		t.Errorf("reinstalled node should resolve to the live one, got %q, %v", id, err)
	}
	if id, err := nodes.NodeID("retired"); err != nil || id != "gone" {
		t.Errorf("a single down node should still resolve, got %q, %v", id, err)
	}
	if _, err := nodes.NodeID("twin"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguity error, got %v", err)
	}
	if _, err := nodes.NodeID("missing"); err == nil {
		t.Errorf("expected error for unknown node")
	}
}

func TestValidateHostPath(t *testing.T) {
	prefixes := []string{"/mnt/data1", "/mnt/storage"}
	ok := map[string]string{
		"/mnt/data1":            "/mnt/data1",
		"/mnt/data1/app":        "/mnt/data1/app",
		"/mnt/storage//media/":  "/mnt/storage/media",
		"/mnt/data1/./app/data": "/mnt/data1/app/data",
	}
	for in, want := range ok {
		got, err := ValidateHostPath(in, prefixes)
		if err != nil || got != want {
			t.Errorf("ValidateHostPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"/etc",
		"/mnt/data10/app",
		"/mnt/data1/../data2",
		"/mnt/data1/app/../../../etc",
		"mnt/data1/app",
		"",
		"/",
	}
	for _, in := range bad {
		if got, err := ValidateHostPath(in, prefixes); err == nil {
			t.Errorf("ValidateHostPath(%q) = %q; want an error", in, got)
		}
	}

	if _, err := ValidateHostPath("/mnt/data1/app", nil); err == nil {
		t.Errorf("an empty allowlist must refuse every path")
	}
}

func TestDiff(t *testing.T) {
	desired := &api.HostVolume{
		Name:       "app",
		NodeID:     "n1",
		HostPath:   "/mnt/data1/app",
		Parameters: map[string]string{"owner": "app"},
	}

	added := Diff(desired, nil)
	if len(added) != 4 { // NodeID, HostPath, Capabilities, Parameters[owner]
		t.Fatalf("expected 4 added fields, got %d: %+v", len(added), added)
	}

	// Nomad fills in default capabilities; an omitted block is not a change.
	existing := &api.HostVolume{
		Name:     "app",
		NodeID:   "n1",
		HostPath: "/mnt/data1/app",
		RequestedCapabilities: []*api.HostVolumeCapability{{
			AttachmentMode: api.HostVolumeAttachmentModeFilesystem,
			AccessMode:     api.HostVolumeAccessModeSingleNodeWriter,
		}},
		Parameters: map[string]string{"owner": "app"},
	}
	if d := Diff(desired, existing); len(d) != 0 {
		t.Fatalf("expected no diff, got %+v", d)
	}

	existing.HostPath = "/mnt/data1/old"
	existing.Parameters = map[string]string{"owner": "app", "stale": "x"}
	d := Diff(desired, existing)
	if len(d) != 2 || d[0].Name != "HostPath" || d[0].Type != "Edited" || d[1].Type != "Deleted" {
		t.Fatalf("unexpected diff: %+v", d)
	}
}
