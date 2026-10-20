// Package volume parses Nomad dynamic host volume specifications from Git and
// compares them with volumes registered in the cluster.
//
// Only register mode is supported: a spec names an existing directory on a
// node (host_path) and has no plugin_id. Caravan never creates, deregisters or
// deletes volumes.
package volume

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/nomad/api"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"

	"github.com/evansmurithi/caravan/internal/config"
)

// SourceParam is the volume parameter caravan stamps with the spec's path in
// Git. It lets caravan recognise a volume whose name or node changed in Git,
// which Nomad cannot update in place.
const SourceParam = "caravan.gitops/source"

// Desired is a host volume rendered from Git together with its source path.
type Desired struct {
	Volume     *api.HostVolume
	SourcePath string
	// NodeName is the name passed to node(), or "" for a literal node_id.
	NodeName string
}

// Name returns the volume's name.
func (d Desired) Name() string { return d.Volume.Name }

// Namespace returns the volume's namespace, or the provided fallback.
func (d Desired) Namespace(fallback string) string {
	if d.Volume.Namespace != "" {
		return d.Volume.Namespace
	}
	return fallback
}

// Node returns a human-readable node reference: the node name when node() was
// used, otherwise the node ID.
func (d Desired) Node() string {
	if d.NodeName != "" {
		return d.NodeName
	}
	return d.Volume.NodeID
}

// NodeResolver maps a Nomad node name to its node ID.
type NodeResolver interface {
	NodeID(name string) (string, error)
}

type hclVolume struct {
	Type         string          `hcl:"type"`
	Name         string          `hcl:"name"`
	Namespace    string          `hcl:"namespace,optional"`
	NodeID       string          `hcl:"node_id"`
	HostPath     string          `hcl:"host_path,optional"`
	PluginID     string          `hcl:"plugin_id,optional"`
	Capacity     string          `hcl:"capacity,optional"`
	Parameters   []hclParameters `hcl:"parameters,block"`
	Capabilities []hclCapability `hcl:"capability,block"`
}

type hclParameters struct {
	Body hcl.Body `hcl:",remain"`
}

type hclCapability struct {
	AccessMode     string `hcl:"access_mode"`
	AttachmentMode string `hcl:"attachment_mode"`
}

// Parse decodes a host volume spec. node_id may be a literal node ID or
// node("<name>"), which is resolved through nodes. relPath is recorded on the
// volume (SourceParam) to identify it across renames of its name or node.
func Parse(path string, body hcl.Body, relPath string, nodes NodeResolver) (Desired, error) {
	var nodeName string
	ctx := &hcl.EvalContext{
		Functions: map[string]function.Function{
			"node": function.New(&function.Spec{
				Params: []function.Parameter{{Name: "name", Type: cty.String}},
				Type:   function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
					name := args[0].AsString()
					if nodes == nil {
						return cty.NilVal, fmt.Errorf("node(%q): no node resolver available", name)
					}
					id, err := nodes.NodeID(name)
					if err != nil {
						return cty.NilVal, err
					}
					nodeName = name
					return cty.StringVal(id), nil
				},
			}),
		},
	}

	var spec hclVolume
	if diags := gohcl.DecodeBody(body, ctx, &spec); diags.HasErrors() {
		return Desired{}, diags
	}

	if spec.Type != "host" {
		return Desired{}, fmt.Errorf("unsupported volume type %q: only \"host\" volumes are supported", spec.Type)
	}
	if spec.Name == "" {
		return Desired{}, fmt.Errorf("volume has no name")
	}
	if spec.NodeID == "" {
		return Desired{}, fmt.Errorf("volume %q: node_id is required", spec.Name)
	}
	if spec.PluginID != "" {
		return Desired{}, fmt.Errorf("volume %q: plugin_id is set; caravan only registers existing host paths (create mode is not supported)", spec.Name)
	}
	if spec.HostPath == "" {
		return Desired{}, fmt.Errorf("volume %q: host_path is required", spec.Name)
	}
	params, err := decodeParameters(spec.Parameters, ctx)
	if err != nil {
		return Desired{}, fmt.Errorf("volume %q: %w", spec.Name, err)
	}

	vol := &api.HostVolume{
		Namespace:  spec.Namespace,
		Name:       spec.Name,
		NodeID:     spec.NodeID,
		HostPath:   spec.HostPath,
		Parameters: map[string]string{},
	}
	if spec.Capacity != "" {
		b, err := humanize.ParseBytes(spec.Capacity)
		if err != nil {
			return Desired{}, fmt.Errorf("volume %q: invalid capacity: %w", spec.Name, err)
		}
		vol.CapacityBytes = int64(b)
	}
	for _, c := range spec.Capabilities {
		vol.RequestedCapabilities = append(vol.RequestedCapabilities, &api.HostVolumeCapability{
			AccessMode:     api.HostVolumeAccessMode(c.AccessMode),
			AttachmentMode: api.HostVolumeAttachmentMode(c.AttachmentMode),
		})
	}
	for k, v := range params {
		vol.Parameters[k] = v
	}
	vol.Parameters[config.ManagedByLabel] = config.ManagedByValue
	vol.Parameters[SourceParam] = filepath.ToSlash(relPath)

	return Desired{Volume: vol, SourcePath: path, NodeName: nodeName}, nil
}

// decodeParameters reads the optional parameters block, matching the block
// syntax of `nomad volume register` specs.
func decodeParameters(blocks []hclParameters, ctx *hcl.EvalContext) (map[string]string, error) {
	if len(blocks) > 1 {
		return nil, fmt.Errorf("only one parameters block is allowed")
	}
	out := map[string]string{}
	if len(blocks) == 0 {
		return out, nil
	}
	attrs, diags := blocks[0].Body.JustAttributes()
	if diags.HasErrors() {
		return nil, diags
	}
	for name, attr := range attrs {
		var v string
		if diags := gohcl.DecodeExpression(attr.Expr, ctx, &v); diags.HasErrors() {
			return nil, diags
		}
		if strings.HasPrefix(name, "caravan.gitops/") {
			return nil, fmt.Errorf("parameter %q is reserved for caravan", name)
		}
		out[name] = v
	}
	return out, nil
}

// IsVolumeSpec reports whether a parsed HCL file is a host volume spec: it has
// a top-level type attribute. It returns an error when the type is not the
// literal string "host".
func IsVolumeSpec(body *hclsyntax.Body) (bool, error) {
	attr, ok := body.Attributes["type"]
	if !ok {
		return false, nil
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.Type() != cty.String || v.IsNull() {
		return false, fmt.Errorf("top-level type must be a literal string")
	}
	if t := v.AsString(); t != "host" {
		return false, fmt.Errorf("unsupported volume type %q: only \"host\" volumes are supported", t)
	}
	return true, nil
}

// ValidateHostPath checks that hostPath is absolute, has no ".." elements and,
// once cleaned, lies at or under one of the allowed prefixes. It returns the
// cleaned path. Registering a volume is root-equivalent on the node and Nomad
// ACLs cannot restrict paths, so this allowlist is the only guard.
func ValidateHostPath(hostPath string, prefixes []string) (string, error) {
	if !filepath.IsAbs(hostPath) {
		return "", fmt.Errorf("host_path %q must be absolute", hostPath)
	}
	for _, elem := range strings.Split(hostPath, "/") {
		if elem == ".." {
			return "", fmt.Errorf("host_path %q must not contain \"..\"", hostPath)
		}
	}
	if len(prefixes) == 0 {
		return "", fmt.Errorf("host_path %q refused: no allowed prefixes configured (set CARAVAN_VOLUME_HOST_PATH_PREFIXES)", hostPath)
	}
	clean := filepath.Clean(hostPath)
	for _, p := range prefixes {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return clean, nil
		}
	}
	return "", fmt.Errorf("host_path %q is outside the allowed prefixes %v", hostPath, prefixes)
}

// defaultCapabilities mirrors what Nomad assigns to a registered volume that
// requests none, so an omitted capability block does not show as a change.
var defaultCapabilities = []*api.HostVolumeCapability{{
	AttachmentMode: api.HostVolumeAttachmentModeFilesystem,
	AccessMode:     api.HostVolumeAccessModeSingleNodeWriter,
}}

// Diff compares the desired volume with an existing one (nil when absent) and
// returns the changed fields in the style of a Nomad job plan.
func Diff(desired, existing *api.HostVolume) []*api.FieldDiff {
	var d []*api.FieldDiff
	field := func(name, from, to string) {
		switch {
		case existing == nil:
			if to != "" {
				d = append(d, &api.FieldDiff{Type: "Added", Name: name, New: to})
			}
		case from == to:
		case from == "":
			d = append(d, &api.FieldDiff{Type: "Added", Name: name, New: to})
		case to == "":
			d = append(d, &api.FieldDiff{Type: "Deleted", Name: name, Old: from})
		default:
			d = append(d, &api.FieldDiff{Type: "Edited", Name: name, Old: from, New: to})
		}
	}

	var ex api.HostVolume
	if existing != nil {
		ex = *existing
	}
	if existing == nil {
		// The node is part of a volume's identity, so it only shows for a
		// new volume; it can never differ for a matched one.
		field("NodeID", "", desired.NodeID)
	}
	field("HostPath", ex.HostPath, desired.HostPath)
	field("CapacityBytes", capacity(ex.CapacityBytes), capacity(desired.CapacityBytes))
	field("Capabilities", capabilities(ex.RequestedCapabilities), capabilities(desired.RequestedCapabilities))

	keys := map[string]bool{}
	for k := range ex.Parameters {
		keys[k] = true
	}
	for k := range desired.Parameters {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		field("Parameters["+k+"]", ex.Parameters[k], desired.Parameters[k])
	}
	return d
}

func capacity(b int64) string {
	if b == 0 {
		return ""
	}
	return fmt.Sprintf("%d", b)
}

func capabilities(caps []*api.HostVolumeCapability) string {
	if len(caps) == 0 {
		caps = defaultCapabilities
	}
	parts := make([]string, 0, len(caps))
	for _, c := range caps {
		if c == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s", c.AttachmentMode, c.AccessMode))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
