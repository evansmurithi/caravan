package volume

import (
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/nomad/api"
)

// ListNodes returns the cluster's nodes.
type ListNodes func() ([]*api.NodeListStub, error)

// Nodes resolves node names through the Nomad nodes API. The node list is
// fetched once, on first use, so create a fresh Nodes per reconcile pass.
type Nodes struct {
	list ListNodes

	once  sync.Once
	stubs []*api.NodeListStub
	err   error
}

// NewNodes returns a resolver backed by list.
func NewNodes(list ListNodes) *Nodes {
	return &Nodes{list: list}
}

// NodeID returns the ID of the node named name. When a node was reinstalled,
// Nomad keeps the old node (status down) next to the new one until it is
// garbage collected, so down nodes are ignored when the name is ambiguous.
func (n *Nodes) NodeID(name string) (string, error) {
	n.once.Do(func() { n.stubs, n.err = n.list() })
	if n.err != nil {
		return "", fmt.Errorf("node(%q): listing nodes: %w", name, n.err)
	}

	var all, live []*api.NodeListStub
	for _, s := range n.stubs {
		if s.Name != name {
			continue
		}
		all = append(all, s)
		if s.Status != api.NodeStatusDown {
			live = append(live, s)
		}
	}

	switch {
	case len(all) == 0:
		return "", fmt.Errorf("node(%q): no node with that name", name)
	case len(all) == 1:
		return all[0].ID, nil
	case len(live) == 1:
		return live[0].ID, nil
	}
	ids := make([]string, 0, len(all))
	for _, s := range all {
		ids = append(ids, fmt.Sprintf("%s (%s)", s.ID, s.Status))
	}
	return "", fmt.Errorf("node(%q): name is ambiguous: %s", name, strings.Join(ids, ", "))
}
