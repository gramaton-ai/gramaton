package graph

import "fmt"

// Edge is a directed relationship between two nodes. Edges are first-class
// objects with their own ID, type, weight, and optional properties.
type Edge struct {
	ID         string
	SourceID   string
	TargetID   string
	Type       string
	Weight     float64
	Properties Properties
}

// AddEdge creates a new edge between two existing nodes. Both source and
// target nodes must exist. Weight should be in [0.0, 1.0]. Properties
// are cloned on creation.
func (g *Graph) AddEdge(sourceID, targetID, edgeType string, weight float64, props Properties) (*Edge, error) {
	if _, ok := g.nodes[sourceID]; !ok {
		return nil, fmt.Errorf("graph: source node %s: %w", sourceID, ErrNotFound)
	}
	if _, ok := g.nodes[targetID]; !ok {
		return nil, fmt.Errorf("graph: target node %s: %w", targetID, ErrNotFound)
	}

	e := &Edge{
		ID:         g.newID(),
		SourceID:   sourceID,
		TargetID:   targetID,
		Type:       edgeType,
		Weight:     weight,
		Properties: props.Clone(),
	}
	if e.Properties == nil {
		e.Properties = make(Properties)
	}

	g.edges[e.ID] = e
	addToIndex(g.outEdges, sourceID, e.ID)
	addToIndex(g.inEdges, targetID, e.ID)
	addToIndex(g.typeEdges, edgeType, e.ID)

	return e, nil
}

// GetEdge returns the edge with the given ID, or nil and false if not found.
func (g *Graph) GetEdge(id string) (*Edge, bool) {
	e, ok := g.edges[id]
	return e, ok
}

// SetEdgeWeight updates an edge's weight.
func (g *Graph) SetEdgeWeight(id string, weight float64) error {
	e, ok := g.edges[id]
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	e.Weight = weight
	return nil
}

// SetEdgeProperty sets a single property on an edge.
func (g *Graph) SetEdgeProperty(id, key string, val Property) error {
	e, ok := g.edges[id]
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	e.Properties[key] = val
	return nil
}

// RemoveEdgeProperty removes a property from an edge.
func (g *Graph) RemoveEdgeProperty(id, key string) error {
	e, ok := g.edges[id]
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	delete(e.Properties, key)
	return nil
}

// DeleteEdge removes an edge and updates all indexes.
func (g *Graph) DeleteEdge(id string) error {
	if _, ok := g.edges[id]; !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	g.deleteEdge(id)
	return nil
}

// deleteEdge is the internal edge deletion that updates indexes.
// Caller must ensure the edge exists.
func (g *Graph) deleteEdge(id string) {
	e := g.edges[id]
	removeFromIndex(g.outEdges, e.SourceID, id)
	removeFromIndex(g.inEdges, e.TargetID, id)
	removeFromIndex(g.typeEdges, e.Type, id)
	delete(g.edges, id)
}

// EdgesFrom returns all outbound edges from a node.
func (g *Graph) EdgesFrom(nodeID string) []*Edge {
	ids, ok := g.outEdges[nodeID]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, g.edges[eid])
	}
	return edges
}

// EdgesTo returns all inbound edges to a node.
func (g *Graph) EdgesTo(nodeID string) []*Edge {
	ids, ok := g.inEdges[nodeID]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, g.edges[eid])
	}
	return edges
}

// EdgesByType returns all edges of the given type.
func (g *Graph) EdgesByType(edgeType string) []*Edge {
	ids, ok := g.typeEdges[edgeType]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, g.edges[eid])
	}
	return edges
}

// EdgeCount returns the number of edges in the graph.
func (g *Graph) EdgeCount() int {
	return len(g.edges)
}

// IsStructuralEdge returns true for edge types that represent structural
// relationships (chunk_of, section_of, member_of) rather than semantic ones.
func IsStructuralEdge(edgeType string) bool {
	return edgeType == "chunk_of" || edgeType == "section_of" || edgeType == "member_of"
}

// IsStructuralChild returns true if a node has an outbound structural
// edge (chunk_of or section_of), meaning it is a chunk or section of
// another record.
func (g *Graph) IsStructuralChild(id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if IsStructuralEdge(e.Type) {
			return true
		}
	}
	return false
}

// SemanticEdgeCount returns the total edge count (in + out) excluding
// structural edges (chunk_of, section_of).
func (g *Graph) SemanticEdgeCount(id string) int {
	count := 0
	for _, e := range g.EdgesFrom(id) {
		if !IsStructuralEdge(e.Type) {
			count++
		}
	}
	for _, e := range g.EdgesTo(id) {
		if !IsStructuralEdge(e.Type) {
			count++
		}
	}
	return count
}
