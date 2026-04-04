package graph

import (
	"errors"
	"testing"
)

func TestAddEdge(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)

	e, err := g.AddEdge(src.ID, tgt.ID, "related_to", 0.8, Properties{
		"reason": StringProperty("both about caching"),
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	if e.ID == "" {
		t.Fatal("edge should have non-empty ID")
	}
	if e.SourceID != src.ID {
		t.Fatal("source ID mismatch")
	}
	if e.TargetID != tgt.ID {
		t.Fatal("target ID mismatch")
	}
	if e.Type != "related_to" {
		t.Fatal("type mismatch")
	}
	if e.Weight != 0.8 {
		t.Fatal("weight mismatch")
	}
	if e.Properties["reason"].String() != "both about caching" {
		t.Fatal("property mismatch")
	}
}

func TestAddEdgeClonesProperties(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)

	props := Properties{"k": StringProperty("original")}
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, props)
	props["k"] = StringProperty("mutated")

	if e.Properties["k"].String() != "original" {
		t.Fatal("AddEdge did not clone properties")
	}
}

func TestAddEdgeNilProperties(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)

	e, err := g.AddEdge(src.ID, tgt.ID, "test", 0.5, nil)
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if e.Properties == nil {
		t.Fatal("nil properties should be initialized")
	}
}

func TestAddEdgeSourceNotFound(t *testing.T) {
	g := New()
	tgt := g.AddNode(nil)

	_, err := g.AddEdge("nonexistent", tgt.ID, "test", 0.5, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAddEdgeTargetNotFound(t *testing.T) {
	g := New()
	src := g.AddNode(nil)

	_, err := g.AddEdge(src.ID, "nonexistent", "test", 0.5, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetEdge(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, nil)

	got, ok := g.GetEdge(e.ID)
	if !ok {
		t.Fatal("GetEdge returned false for existing edge")
	}
	if got.ID != e.ID {
		t.Fatal("ID mismatch")
	}
}

func TestGetEdgeNotFound(t *testing.T) {
	g := New()
	_, ok := g.GetEdge("nonexistent")
	if ok {
		t.Fatal("GetEdge should return false for missing edge")
	}
}

func TestSetEdgeWeight(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, nil)

	if err := g.SetEdgeWeight(e.ID, 0.9); err != nil {
		t.Fatalf("SetEdgeWeight: %v", err)
	}

	got, _ := g.GetEdge(e.ID)
	if got.Weight != 0.9 {
		t.Fatalf("expected 0.9, got %f", got.Weight)
	}
}

func TestSetEdgeWeightNotFound(t *testing.T) {
	g := New()
	err := g.SetEdgeWeight("nonexistent", 0.5)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetEdgeProperty(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, nil)

	if err := g.SetEdgeProperty(e.ID, "note", StringProperty("added later")); err != nil {
		t.Fatalf("SetEdgeProperty: %v", err)
	}

	got, _ := g.GetEdge(e.ID)
	if got.Properties["note"].String() != "added later" {
		t.Fatal("property not set")
	}
}

func TestSetEdgePropertyNotFound(t *testing.T) {
	g := New()
	err := g.SetEdgeProperty("nonexistent", "k", StringProperty("v"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveEdgeProperty(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, Properties{
		"x": StringProperty("remove me"),
	})

	if err := g.RemoveEdgeProperty(e.ID, "x"); err != nil {
		t.Fatalf("RemoveEdgeProperty: %v", err)
	}

	got, _ := g.GetEdge(e.ID)
	if _, ok := got.Properties["x"]; ok {
		t.Fatal("property should have been removed")
	}
}

func TestRemoveEdgePropertyNotFound(t *testing.T) {
	g := New()
	err := g.RemoveEdgeProperty("nonexistent", "k")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteEdge(t *testing.T) {
	g := New()
	src := g.AddNode(nil)
	tgt := g.AddNode(nil)
	e, _ := g.AddEdge(src.ID, tgt.ID, "test", 0.5, nil)

	if err := g.DeleteEdge(e.ID); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if g.EdgeCount() != 0 {
		t.Fatal("expected 0 edges")
	}
	if _, ok := g.GetEdge(e.ID); ok {
		t.Fatal("deleted edge should not be retrievable")
	}
}

func TestDeleteEdgeNotFound(t *testing.T) {
	g := New()
	err := g.DeleteEdge("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestEdgeCount(t *testing.T) {
	g := New()
	if g.EdgeCount() != 0 {
		t.Fatal("empty graph should have 0 edges")
	}
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	g.AddEdge(a.ID, b.ID, "e1", 0.5, nil)
	g.AddEdge(a.ID, b.ID, "e2", 0.5, nil)
	if g.EdgeCount() != 2 {
		t.Fatal("expected 2 edges")
	}
}

// --- Edge indexes ---

func TestEdgesFrom(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "x", 0.5, nil)
	g.AddEdge(a.ID, c.ID, "y", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "z", 0.5, nil)

	out := g.EdgesFrom(a.ID)
	if len(out) != 2 {
		t.Fatalf("expected 2 outbound edges from a, got %d", len(out))
	}

	out = g.EdgesFrom(b.ID)
	if len(out) != 1 {
		t.Fatalf("expected 1 outbound edge from b, got %d", len(out))
	}

	out = g.EdgesFrom(c.ID)
	if len(out) != 0 {
		t.Fatalf("expected 0 outbound edges from c, got %d", len(out))
	}
}

func TestEdgesFromNoNode(t *testing.T) {
	g := New()
	out := g.EdgesFrom("nonexistent")
	if len(out) != 0 {
		t.Fatal("EdgesFrom on missing node should return empty")
	}
}

func TestEdgesTo(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, c.ID, "x", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "y", 0.5, nil)

	in := g.EdgesTo(c.ID)
	if len(in) != 2 {
		t.Fatalf("expected 2 inbound edges to c, got %d", len(in))
	}

	in = g.EdgesTo(a.ID)
	if len(in) != 0 {
		t.Fatalf("expected 0 inbound edges to a, got %d", len(in))
	}
}

func TestEdgesToNoNode(t *testing.T) {
	g := New()
	in := g.EdgesTo("nonexistent")
	if len(in) != 0 {
		t.Fatal("EdgesTo on missing node should return empty")
	}
}

func TestEdgesByType(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "justifies", 0.9, nil)
	g.AddEdge(a.ID, c.ID, "related_to", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "justifies", 0.7, nil)

	just := g.EdgesByType("justifies")
	if len(just) != 2 {
		t.Fatalf("expected 2 justifies edges, got %d", len(just))
	}

	rel := g.EdgesByType("related_to")
	if len(rel) != 1 {
		t.Fatalf("expected 1 related_to edge, got %d", len(rel))
	}

	none := g.EdgesByType("nonexistent")
	if len(none) != 0 {
		t.Fatal("EdgesByType for missing type should return empty")
	}
}

// --- Cascading deletion ---

func TestDeleteNodeCascadesEdges(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	// a → b, b → c, c → a (cycle)
	g.AddEdge(a.ID, b.ID, "e1", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "e2", 0.5, nil)
	g.AddEdge(c.ID, a.ID, "e3", 0.5, nil)

	if g.EdgeCount() != 3 {
		t.Fatalf("expected 3 edges, got %d", g.EdgeCount())
	}

	// Delete b -- should remove e1 (inbound) and e2 (outbound).
	if err := g.DeleteNode(b.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	if g.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 1 {
		t.Fatalf("expected 1 remaining edge, got %d", g.EdgeCount())
	}

	// The remaining edge should be c → a.
	remaining := g.EdgesFrom(c.ID)
	if len(remaining) != 1 {
		t.Fatalf("expected 1 edge from c, got %d", len(remaining))
	}
	if remaining[0].TargetID != a.ID {
		t.Fatal("remaining edge should point to a")
	}
}

func TestDeleteNodeCascadesAllEdges(t *testing.T) {
	g := New()
	hub := g.AddNode(nil)
	var spokes []*Node
	for i := 0; i < 5; i++ {
		s := g.AddNode(nil)
		spokes = append(spokes, s)
		g.AddEdge(hub.ID, s.ID, "out", 0.5, nil)
		g.AddEdge(s.ID, hub.ID, "in", 0.5, nil)
	}

	if g.EdgeCount() != 10 {
		t.Fatalf("expected 10 edges, got %d", g.EdgeCount())
	}

	if err := g.DeleteNode(hub.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges after hub deletion, got %d", g.EdgeCount())
	}
	if g.NodeCount() != 5 {
		t.Fatalf("expected 5 remaining nodes, got %d", g.NodeCount())
	}
}

func TestDeleteNodeUpdatesAllEdgeIndexes(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "justifies", 0.9, nil)

	g.DeleteNode(b.ID)

	// All indexes should be clean.
	if len(g.EdgesFrom(a.ID)) != 0 {
		t.Fatal("outEdges index not cleaned")
	}
	if len(g.EdgesTo(b.ID)) != 0 {
		t.Fatal("inEdges index not cleaned")
	}
	if len(g.EdgesByType("justifies")) != 0 {
		t.Fatal("typeEdges index not cleaned")
	}
}

func TestDeleteEdgeUpdatesIndexes(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)

	e, _ := g.AddEdge(a.ID, b.ID, "test", 0.5, nil)
	g.DeleteEdge(e.ID)

	if len(g.EdgesFrom(a.ID)) != 0 {
		t.Fatal("outEdges index not cleaned after edge delete")
	}
	if len(g.EdgesTo(b.ID)) != 0 {
		t.Fatal("inEdges index not cleaned after edge delete")
	}
	if len(g.EdgesByType("test")) != 0 {
		t.Fatal("typeEdges index not cleaned after edge delete")
	}
}

// --- Self-edges ---

func TestSelfEdge(t *testing.T) {
	g := New()
	n := g.AddNode(nil)

	e, err := g.AddEdge(n.ID, n.ID, "self", 1.0, nil)
	if err != nil {
		t.Fatalf("AddEdge self: %v", err)
	}

	if len(g.EdgesFrom(n.ID)) != 1 {
		t.Fatal("expected 1 outbound edge")
	}
	if len(g.EdgesTo(n.ID)) != 1 {
		t.Fatal("expected 1 inbound edge")
	}

	// Deleting the node should clean up the self-edge.
	g.DeleteNode(n.ID)
	if g.EdgeCount() != 0 {
		t.Fatal("self-edge should be deleted with node")
	}
	_ = e
}

// --- Multiple edges between same nodes ---

func TestMultipleEdgesBetweenSameNodes(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)

	e1, _ := g.AddEdge(a.ID, b.ID, "justifies", 0.9, nil)
	e2, _ := g.AddEdge(a.ID, b.ID, "related_to", 0.5, nil)

	if g.EdgeCount() != 2 {
		t.Fatal("expected 2 edges")
	}
	if e1.ID == e2.ID {
		t.Fatal("parallel edges should have different IDs")
	}

	out := g.EdgesFrom(a.ID)
	if len(out) != 2 {
		t.Fatalf("expected 2 outbound edges, got %d", len(out))
	}
}
