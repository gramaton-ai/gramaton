package graph

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

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

// AddEdge creates a new edge between two existing nodes via the
// edge store's own transaction. Both source and target nodes must
// exist. Weight should be in [0.0, 1.0]. Properties are cloned on
// creation.
func (g *Graph) AddEdge(sourceID, targetID, edgeType string, weight float64, props Properties) (*Edge, error) {
	return g.AddEdgeTx(nil, nil, sourceID, targetID, edgeType, weight, props)
}

// AddEdgeTx is AddEdge via the caller's bbolt transaction + optional
// *EdgeBatch cache. When tx and batch are both nil, falls back to
// the edge store opening its own Update (non-batched path). When
// non-nil, writes go through the bbolt-backed store's PutTx so the
// shared tx is used and the adjacency cache amortizes re-encode cost.
// In-memory edge stores ignore tx/batch. See D40.
func (g *Graph) AddEdgeTx(tx *bolt.Tx, batch *EdgeBatch, sourceID, targetID, edgeType string, weight float64, props Properties) (*Edge, error) {
	if _, ok := g.GetNode(sourceID); !ok {
		return nil, fmt.Errorf("graph: source node %s: %w", sourceID, ErrNotFound)
	}
	if _, ok := g.GetNode(targetID); !ok {
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

	if tx != nil {
		g.edgeStore.PutTx(tx, batch, e)
	} else {
		g.edgeStore.Put(e)
	}
	g.markEdgeDirty(e.ID)

	return e, nil
}

// GetEdge returns the edge with the given ID, or nil and false if not found.
func (g *Graph) GetEdge(id string) (*Edge, bool) {
	return g.edgeStore.Get(id)
}

// ForEachEdge iterates every edge in the store in unspecified order,
// calling fn for each. Avoids the per-node EdgesFrom/EdgesTo round
// trips when callers genuinely need every edge (e.g. Validate).
func (g *Graph) ForEachEdge(fn func(*Edge)) {
	g.edgeStore.ForEach(fn)
}

// SetEdgeWeight updates an edge's weight.
func (g *Graph) SetEdgeWeight(id string, weight float64) error {
	e, ok := g.edgeStore.Get(id)
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	e.Weight = weight
	g.markEdgeDirty(id)
	return nil
}

// SetEdgeProperty sets a single property on an edge.
func (g *Graph) SetEdgeProperty(id, key string, val Property) error {
	e, ok := g.edgeStore.Get(id)
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	e.Properties[key] = val
	g.markEdgeDirty(id)
	return nil
}

// RemoveEdgeProperty removes a property from an edge.
func (g *Graph) RemoveEdgeProperty(id, key string) error {
	e, ok := g.edgeStore.Get(id)
	if !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	delete(e.Properties, key)
	g.markEdgeDirty(id)
	return nil
}

// DeleteEdge removes an edge and updates all indexes.
func (g *Graph) DeleteEdge(id string) error {
	if _, ok := g.edgeStore.Get(id); !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	g.deleteEdge(id)
	return nil
}

// DeleteEdgeTx removes an edge via the caller's tx + optional
// *EdgeBatch. Inside a shared write transaction (WithWriteBatch) the
// plain DeleteEdge would open its own bbolt Update against the same
// DB and self-deadlock; this variant routes through the edge store's
// DeleteTx instead. In-memory edge stores ignore tx/batch. See D40.
func (g *Graph) DeleteEdgeTx(tx *bolt.Tx, batch *EdgeBatch, id string) error {
	if _, ok := g.edgeStore.Get(id); !ok {
		return fmt.Errorf("graph: edge %s: %w", id, ErrNotFound)
	}
	g.edgeStore.DeleteTx(tx, batch, id)
	delete(g.dirtyEdges, id)
	g.deletedEdges[id] = struct{}{}
	return nil
}

// deleteEdge is the internal edge deletion that updates indexes.
// Caller must ensure the edge exists.
func (g *Graph) deleteEdge(id string) {
	g.edgeStore.Delete(id)
	delete(g.dirtyEdges, id)
	g.deletedEdges[id] = struct{}{}
}

// EdgesFrom returns all outbound edges from a node.
func (g *Graph) EdgesFrom(nodeID string) []*Edge {
	return g.edgeStore.From(nodeID)
}

// EdgesTo returns all inbound edges to a node.
func (g *Graph) EdgesTo(nodeID string) []*Edge {
	return g.edgeStore.To(nodeID)
}

// EdgesByType returns all edges of the given type.
func (g *Graph) EdgesByType(edgeType string) []*Edge {
	return g.edgeStore.ByType(edgeType)
}

// EdgeCount returns the number of edges in the graph.
func (g *Graph) EdgeCount() int {
	return g.edgeStore.Count()
}

// IsStructuralEdge returns true for edge types that represent structural
// relationships rather than semantic ones. Structural edges are excluded
// from semantic edge counts and graph-based scoring.
func IsStructuralEdge(edgeType string) bool {
	switch edgeType {
	case "chunk_of", "section_of", "member_of", "observation_of",
		"topic_of", "segment_of", "continues_from", "extracted_as":
		return true
	}
	return false
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
