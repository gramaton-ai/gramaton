package graph

import (
	"testing"
)

func TestTraverseBasic(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{"content_short": StringProperty("A")})
	b := g.AddNode(Properties{"content_short": StringProperty("B")})
	c := g.AddNode(Properties{"content_short": StringProperty("C")})

	g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil)
	g.AddEdge(b.ID, c.ID, "justifies", 0.9, nil)

	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 2})

	if len(sub.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(sub.Nodes))
	}
	if len(sub.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(sub.Edges))
	}
}

func TestTraverseDepthLimit(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)
	d := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "x", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "x", 0.5, nil)
	g.AddEdge(c.ID, d.ID, "x", 0.5, nil)

	// Depth 1: a + b only.
	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 1})
	if len(sub.Nodes) != 2 {
		t.Fatalf("depth 1: expected 2 nodes, got %d", len(sub.Nodes))
	}

	// Depth 2: a + b + c.
	sub = g.Traverse(a.ID, TraverseOptions{MaxDepth: 2})
	if len(sub.Nodes) != 3 {
		t.Fatalf("depth 2: expected 3 nodes, got %d", len(sub.Nodes))
	}

	// Depth 3: all 4.
	sub = g.Traverse(a.ID, TraverseOptions{MaxDepth: 3})
	if len(sub.Nodes) != 4 {
		t.Fatalf("depth 3: expected 4 nodes, got %d", len(sub.Nodes))
	}
}

func TestTraverseEdgeTypeFilter(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "justifies", 0.9, nil)
	g.AddEdge(a.ID, c.ID, "related_to", 0.5, nil)

	sub := g.Traverse(a.ID, TraverseOptions{
		MaxDepth:  2,
		EdgeTypes: []string{"justifies"},
	})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes (a + b), got %d", len(sub.Nodes))
	}
	if len(sub.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(sub.Edges))
	}
}

func TestTraverseMinWeight(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "x", 0.9, nil)
	g.AddEdge(a.ID, c.ID, "x", 0.2, nil)

	sub := g.Traverse(a.ID, TraverseOptions{
		MaxDepth:      2,
		MinEdgeWeight: 0.5,
	})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes (a + b, c filtered by weight), got %d", len(sub.Nodes))
	}
}

func TestTraverseFollowsInbound(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)

	// Edge from b → a. Traversal from a should find b via inbound.
	g.AddEdge(b.ID, a.ID, "justifies", 0.9, nil)

	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 1})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(sub.Nodes))
	}
}

func TestTraverseNoCycles(t *testing.T) {
	g := New()
	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c := g.AddNode(nil)

	g.AddEdge(a.ID, b.ID, "x", 0.5, nil)
	g.AddEdge(b.ID, c.ID, "x", 0.5, nil)
	g.AddEdge(c.ID, a.ID, "x", 0.5, nil)

	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 10})

	// Should visit each node exactly once despite the cycle.
	if len(sub.Nodes) != 3 {
		t.Fatalf("expected 3 nodes (no duplicates), got %d", len(sub.Nodes))
	}
}

func TestTraverseIsolatedNode(t *testing.T) {
	g := New()
	a := g.AddNode(nil)

	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 2})

	if len(sub.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(sub.Nodes))
	}
	if len(sub.Edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(sub.Edges))
	}
}

func TestTraversePopulatesNodeFields(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{
		"content_short":    StringProperty("Test node"),
		"content_keywords": StringListProperty([]string{"test", "node"}),
	})

	sub := g.Traverse(a.ID, TraverseOptions{MaxDepth: 1})

	if sub.Nodes[0].SummaryShort != "Test node" {
		t.Fatalf("expected summary, got %q", sub.Nodes[0].SummaryShort)
	}
	if len(sub.Nodes[0].Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(sub.Nodes[0].Keywords))
	}
}
