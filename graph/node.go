package graph

import "fmt"

// Node is a vertex in the property graph. It has a stable ULID and a
// set of typed properties. The graph engine treats all nodes identically --
// "types" like knowledge record, concept node, and chunk node are
// conventions enforced by higher layers through property values.
type Node struct {
	ID         string
	Properties Properties
}

// AddNode creates a new node with the given properties and returns it.
// The node is assigned a new ULID. Properties are cloned on creation.
func (g *Graph) AddNode(props Properties) *Node {
	n := &Node{
		ID:         g.newID(),
		Properties: props.Clone(),
	}
	if n.Properties == nil {
		n.Properties = make(Properties)
	}
	g.nodes[n.ID] = n
	g.markNodeDirty(n.ID)
	return n
}

// GetNode returns the node with the given ID, or nil and false if not found.
func (g *Graph) GetNode(id string) (*Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// SetNodeProperty sets a single property on a node. Creates the property
// if absent, overwrites if present.
func (g *Graph) SetNodeProperty(id, key string, val Property) error {
	n, ok := g.nodes[id]
	if !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}
	n.Properties[key] = val
	g.markNodeDirty(id)
	return nil
}

// RemoveNodeProperty removes a property from a node. No error if the
// property doesn't exist.
func (g *Graph) RemoveNodeProperty(id, key string) error {
	n, ok := g.nodes[id]
	if !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}
	delete(n.Properties, key)
	g.markNodeDirty(id)
	return nil
}

// DeleteNode removes a node and all its inbound and outbound edges
// (cascading deletion). Returns ErrNotFound if the node doesn't exist.
func (g *Graph) DeleteNode(id string) error {
	if _, ok := g.nodes[id]; !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}

	// Collect unique edge IDs to delete (outbound + inbound).
	// A self-edge appears in both sets, so we deduplicate.
	edgeSet := make(map[string]struct{})
	if out, ok := g.outEdges[id]; ok {
		for eid := range out {
			edgeSet[eid] = struct{}{}
		}
	}
	if in, ok := g.inEdges[id]; ok {
		for eid := range in {
			edgeSet[eid] = struct{}{}
		}
	}

	// Delete edges first (updates all indexes).
	for eid := range edgeSet {
		g.deleteEdge(eid)
	}

	delete(g.nodes, id)
	delete(g.dirtyNodes, id)
	g.deletedNodes[id] = struct{}{}
	return nil
}

// NodeCount returns the number of nodes in the graph.
func (g *Graph) NodeCount() int {
	return len(g.nodes)
}

// AllNodeIDs returns all node IDs in the graph. Order is not guaranteed.
//
// Prefer NodeIterator for new code -- it avoids allocating the full ID
// slice and will cursor through the prolly tree under lazy loading.
func (g *Graph) AllNodeIDs() []string {
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	return ids
}

// NodeIterator returns an iterator over all nodes in the graph.
// The current implementation wraps the in-memory map. A future lazy
// implementation will cursor through the prolly tree on demand.
func (g *Graph) NodeIterator() NodeIterator {
	nodes := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, n)
	}
	return &sliceNodeIterator{nodes: nodes, pos: -1}
}

// sliceNodeIterator is the in-memory NodeIterator backed by a slice.
type sliceNodeIterator struct {
	nodes []*Node
	pos   int
}

func (it *sliceNodeIterator) Next() bool {
	it.pos++
	return it.pos < len(it.nodes)
}

func (it *sliceNodeIterator) Node() *Node {
	return it.nodes[it.pos]
}

func (it *sliceNodeIterator) Close() {
	it.nodes = nil
}
