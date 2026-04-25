package graph

import (
	"testing"
)

func TestSelfReferencingEdge(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"content_full": StringProperty("self-referencing"),
	})

	e, err := g.AddEdge(n.ID, n.ID, "self_ref", 1.0, nil)
	if err != nil {
		t.Fatalf("self-referencing edge should be allowed: %v", err)
	}

	// Should appear in both EdgesFrom and EdgesTo.
	from := g.EdgesFrom(n.ID)
	to := g.EdgesTo(n.ID)

	foundFrom := false
	for _, edge := range from {
		if edge.ID == e.ID {
			foundFrom = true
		}
	}
	foundTo := false
	for _, edge := range to {
		if edge.ID == e.ID {
			foundTo = true
		}
	}

	if !foundFrom {
		t.Fatal("self-ref edge should appear in EdgesFrom")
	}
	if !foundTo {
		t.Fatal("self-ref edge should appear in EdgesTo")
	}
}

func TestDeleteNodeCascadesInboundAndOutbound(t *testing.T) {
	g := New()
	n1 := g.AddNode(Properties{"content_full": StringProperty("center")})
	n2 := g.AddNode(Properties{"content_full": StringProperty("neighbor1")})
	n3 := g.AddNode(Properties{"content_full": StringProperty("neighbor2")})

	g.AddEdge(n1.ID, n2.ID, "related_to", 0.5, nil)
	g.AddEdge(n1.ID, n3.ID, "related_to", 0.5, nil)
	g.AddEdge(n3.ID, n1.ID, "discusses", 0.8, nil) // inbound

	if g.EdgeCount() != 3 {
		t.Fatalf("expected 3 edges, got %d", g.EdgeCount())
	}

	// Delete center node.
	if err := g.DeleteNode(n1.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	// All edges involving n1 should be gone.
	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges after deleting center, got %d", g.EdgeCount())
	}

	// n2 and n3 should still exist.
	if _, ok := g.GetNode(n2.ID); !ok {
		t.Fatal("n2 should still exist")
	}
	if _, ok := g.GetNode(n3.ID); !ok {
		t.Fatal("n3 should still exist")
	}
}

func TestDeleteNodeWithManyEdges(t *testing.T) {
	g := New()
	center := g.AddNode(Properties{"content_full": StringProperty("hub")})

	// Create 50 neighbors with edges.
	for i := 0; i < 50; i++ {
		n := g.AddNode(Properties{"content_full": StringProperty("spoke")})
		g.AddEdge(center.ID, n.ID, "related_to", 0.5, nil)
	}

	if g.EdgeCount() != 50 {
		t.Fatalf("expected 50 edges, got %d", g.EdgeCount())
	}

	if err := g.DeleteNode(center.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges after hub deletion, got %d", g.EdgeCount())
	}
	if g.NodeCount() != 50 {
		t.Fatalf("expected 50 remaining nodes, got %d", g.NodeCount())
	}
}

func TestDeleteNodeSelfEdge(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"content_full": StringProperty("self")})
	g.AddEdge(n.ID, n.ID, "self_ref", 1.0, nil)

	if err := g.DeleteNode(n.ID); err != nil {
		t.Fatalf("DeleteNode with self-edge: %v", err)
	}

	if g.NodeCount() != 0 {
		t.Fatalf("expected 0 nodes, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges, got %d", g.EdgeCount())
	}
}

// TestNodeCountTracksAddDeleteCycles pins that NodeCount stays in sync
// with the live node set across interleaved add/delete cycles in eager
// mode. The tracker hypothesis (P2-02 sub-fix 4) was that nodeTotal
// could drift below the true count, but current code increments
// nodeTotal in AddNode and decrements in DeleteNode in both modes.
// This test stays as a regression guard against that drift returning.
func TestNodeCountTracksAddDeleteCycles(t *testing.T) {
	g := New()

	if got := g.NodeCount(); got != 0 {
		t.Fatalf("fresh graph: NodeCount()=%d, want 0", got)
	}

	var ids []string
	for range 5 {
		n := g.AddNode(Properties{"content_full": StringProperty("a")})
		ids = append(ids, n.ID)
	}
	if got := g.NodeCount(); got != 5 {
		t.Fatalf("after 5 adds: NodeCount()=%d, want 5", got)
	}

	// Delete two -- count drops by exactly two.
	if err := g.DeleteNode(ids[0]); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if err := g.DeleteNode(ids[1]); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if got := g.NodeCount(); got != 3 {
		t.Fatalf("after 2 deletes: NodeCount()=%d, want 3", got)
	}

	// Add three more -- count is 6.
	for range 3 {
		g.AddNode(Properties{"content_full": StringProperty("b")})
	}
	if got := g.NodeCount(); got != 6 {
		t.Fatalf("after 3 more adds: NodeCount()=%d, want 6", got)
	}

	// Delete all remaining.
	for _, id := range g.AllNodeIDs() {
		if err := g.DeleteNode(id); err != nil {
			t.Fatalf("DeleteNode %s: %v", id, err)
		}
	}
	if got := g.NodeCount(); got != 0 {
		t.Fatalf("after deleting all: NodeCount()=%d, want 0", got)
	}
}

func TestDeleteNonexistentNode(t *testing.T) {
	g := New()
	err := g.DeleteNode("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestAddEdgeNonexistentSource(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"content_full": StringProperty("target")})
	_, err := g.AddEdge("nonexistent", n.ID, "test", 0.5, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent source")
	}
}

func TestAddEdgeNonexistentTarget(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"content_full": StringProperty("source")})
	_, err := g.AddEdge(n.ID, "nonexistent", "test", 0.5, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent target")
	}
}

func TestAddRemoveAddSameEdge(t *testing.T) {
	g := New()
	n1 := g.AddNode(Properties{"content_full": StringProperty("a")})
	n2 := g.AddNode(Properties{"content_full": StringProperty("b")})

	e1, _ := g.AddEdge(n1.ID, n2.ID, "related_to", 0.5, nil)
	g.DeleteEdge(e1.ID)

	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges after delete, got %d", g.EdgeCount())
	}

	e2, err := g.AddEdge(n1.ID, n2.ID, "related_to", 0.8, nil)
	if err != nil {
		t.Fatalf("re-add edge: %v", err)
	}

	if g.EdgeCount() != 1 {
		t.Fatalf("expected 1 edge after re-add, got %d", g.EdgeCount())
	}
	if e2.Weight != 0.8 {
		t.Fatalf("expected weight 0.8, got %f", e2.Weight)
	}
}

func TestEdgesFromEmptyNode(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"content_full": StringProperty("isolated")})

	edges := g.EdgesFrom(n.ID)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
}

func TestEdgesFromNonexistentNode(t *testing.T) {
	g := New()
	edges := g.EdgesFrom("nonexistent")
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for nonexistent, got %d", len(edges))
	}
}

func TestAllNodeIDsOrder(t *testing.T) {
	g := New()
	g.AddNode(Properties{"content_full": StringProperty("a")})
	g.AddNode(Properties{"content_full": StringProperty("b")})
	g.AddNode(Properties{"content_full": StringProperty("c")})

	ids := g.AllNodeIDs()
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
	// No guaranteed order, just verify uniqueness.
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

func TestSetNodePropertyOverwrite(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"content_full": StringProperty("original"),
	})

	g.SetNodeProperty(n.ID, "content_full", StringProperty("updated"))

	got, _ := g.GetNode(n.ID)
	v, _ := got.Properties.GetString("content_full")
	if v != "updated" {
		t.Fatalf("expected 'updated', got %q", v)
	}
}

func TestRemoveNodePropertyKeepsOthers(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"content_full": StringProperty("test"),
		"temporality":  StringProperty("durable"),
	})

	g.RemoveNodeProperty(n.ID, "temporality")

	got, _ := g.GetNode(n.ID)
	if _, ok := got.Properties.GetString("temporality"); ok {
		t.Fatal("temporality should be removed")
	}
	if _, ok := got.Properties.GetString("content_full"); !ok {
		t.Fatal("content_full should remain")
	}
}

func TestRemoveNodePropertyNonexistent(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"content_full": StringProperty("test")})

	// Removing a property that doesn't exist should not error.
	err := g.RemoveNodeProperty(n.ID, "nonexistent")
	if err != nil {
		t.Fatalf("removing nonexistent property should not error: %v", err)
	}
}

func TestRemoveNodePropertyNonexistentNode(t *testing.T) {
	g := New()
	err := g.RemoveNodeProperty("nonexistent", "key")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestEmptyGraphCounts(t *testing.T) {
	g := New()
	if g.NodeCount() != 0 {
		t.Fatalf("expected 0 nodes, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Fatalf("expected 0 edges, got %d", g.EdgeCount())
	}
	if len(g.AllNodeIDs()) != 0 {
		t.Fatalf("expected 0 IDs, got %d", len(g.AllNodeIDs()))
	}
}

func TestNodePropertiesClonedOnAdd(t *testing.T) {
	g := New()
	props := Properties{
		"content_full": StringProperty("original"),
	}
	n := g.AddNode(props)

	// Modifying the original props should not affect the node.
	props["content_full"] = StringProperty("modified")

	got, _ := g.GetNode(n.ID)
	v, _ := got.Properties.GetString("content_full")
	if v != "original" {
		t.Fatal("node properties should be cloned on add")
	}
}
