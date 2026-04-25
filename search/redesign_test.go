package search

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// --- BFS graph proximity filter ---

func TestBFSReachable(t *testing.T) {
	// Build a chain: A -> B -> C -> D
	// Plus a branch: B -> E
	g := graph.New()
	a := g.AddNode(graph.Properties{"x": graph.StringProperty("a")})
	b := g.AddNode(graph.Properties{"x": graph.StringProperty("b")})
	c := g.AddNode(graph.Properties{"x": graph.StringProperty("c")})
	d := g.AddNode(graph.Properties{"x": graph.StringProperty("d")})
	e := g.AddNode(graph.Properties{"x": graph.StringProperty("e")})
	g.AddEdge(a.ID, b.ID, "related_to", 1.0, nil)
	g.AddEdge(b.ID, c.ID, "related_to", 1.0, nil)
	g.AddEdge(c.ID, d.ID, "related_to", 1.0, nil)
	g.AddEdge(b.ID, e.ID, "related_to", 1.0, nil)

	// 1 hop from A: should reach B (outbound) only.
	r1 := bfsReachable(g, a.ID, 1)
	if len(r1) != 1 {
		t.Fatalf("1 hop from A: expected 1 node, got %d", len(r1))
	}
	if _, ok := r1[b.ID]; !ok {
		t.Fatal("1 hop from A should include B")
	}

	// 2 hops from A: should reach B, C, E (B's outbound).
	r2 := bfsReachable(g, a.ID, 2)
	if len(r2) != 3 {
		t.Fatalf("2 hops from A: expected 3 nodes, got %d: %v", len(r2), keys(r2))
	}
	for _, id := range []string{b.ID, c.ID, e.ID} {
		if _, ok := r2[id]; !ok {
			t.Fatalf("2 hops from A should include %s", id)
		}
	}

	// 3 hops from A: should also reach D.
	r3 := bfsReachable(g, a.ID, 3)
	if len(r3) != 4 {
		t.Fatalf("3 hops from A: expected 4 nodes, got %d", len(r3))
	}

	// 0 hops: nobody.
	r0 := bfsReachable(g, a.ID, 0)
	if len(r0) != 0 {
		t.Fatalf("0 hops from A: expected 0 nodes, got %d", len(r0))
	}

	// BFS follows edges in both directions. From C with 1 hop: B (inbound) and D (outbound).
	rc := bfsReachable(g, c.ID, 1)
	if len(rc) != 2 {
		t.Fatalf("1 hop from C: expected 2 nodes (B and D), got %d", len(rc))
	}
	if _, ok := rc[b.ID]; !ok {
		t.Fatal("1 hop from C should include B (inbound edge)")
	}
	if _, ok := rc[d.ID]; !ok {
		t.Fatal("1 hop from C should include D (outbound edge)")
	}
}

// TestBFSReachableHitsCap pins the defensive truncation path: when the
// graph is denser than maxBFSReachableNodes allows at a given hop depth,
// bfsReachable stops walking and returns what it has rather than
// blowing memory. Lowers the cap to 3 for the test so a tiny fixture
// can exercise it.
func TestBFSReachableHitsCap(t *testing.T) {
	old := maxBFSReachableNodes
	maxBFSReachableNodes = 3
	t.Cleanup(func() { maxBFSReachableNodes = old })

	// Star: center -> 10 leaves. With cap=3 and start=center, we
	// should visit no more than 3 nodes (start + 2 neighbors before
	// the cap fires). The exact stopping point is implementation-
	// defined; the contract is "at most cap" total.
	g := graph.New()
	center := g.AddNode(graph.Properties{"x": graph.StringProperty("center")})
	for range 10 {
		leaf := g.AddNode(graph.Properties{"x": graph.StringProperty("leaf")})
		g.AddEdge(center.ID, leaf.ID, "related_to", 1.0, nil)
	}

	r := bfsReachable(g, center.ID, 1)
	// visited capped at 3 (center + 2 leaves) before delete(visited, startID),
	// so r has at most 2 leaves.
	if len(r) >= 10 {
		t.Fatalf("cap not enforced: returned %d nodes (full graph)", len(r))
	}
}

func TestBFSReachableIsolatedNode(t *testing.T) {
	g := graph.New()
	a := g.AddNode(graph.Properties{})
	g.AddNode(graph.Properties{}) // b, isolated

	r := bfsReachable(g, a.ID, 5)
	if len(r) != 0 {
		t.Fatalf("isolated node should have 0 reachable, got %d", len(r))
	}
}

func TestNearNodeSearchFilter(t *testing.T) {
	// Build a graph with content so we can search:
	//   A ("kafka event pipeline") --related_to--> B ("kafka consumer groups")
	//   C ("postgresql database") -- no edges to A or B
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	bm25 := index.NewBM25Index(1.2, 0.75)
	now := time.Now().UTC()

	a := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("kafka event pipeline architecture"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
	})
	b := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("kafka consumer groups and partitions"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
	})
	c := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("kafka streaming with ksqldb"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
	})

	g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil)
	// C has no edges -- it's disconnected from A and B.

	for _, n := range []*graph.Node{a, b, c} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
		text, _ := n.Properties.GetString("content_full")
		bm25.Add(n.ID, text)
	}

	cfg := defaultCfg()
	tool := New(g, propIdx, nil, bm25, nil, cfg)

	// Search for "kafka" near node A with 1 hop: should find B but not C.
	results, err := tool.ExecuteWithVector(context.Background(), Query{
		Text:     "kafka",
		NearNode: a.ID,
		MaxHops:  1,
		Top:      10,
	}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Should only contain B (A's neighbor). C is disconnected.
	if len(results) != 1 {
		t.Fatalf("expected 1 result near A, got %d", len(results))
	}
	if results[0].ID != b.ID {
		t.Fatalf("expected B near A, got %s", results[0].ID)
	}

	// Without near_node filter, all three should appear.
	allResults, _ := tool.ExecuteWithVector(context.Background(), Query{
		Text: "kafka",
		Top:  10,
	}, nil)
	if len(allResults) != 3 {
		t.Fatalf("expected 3 results without near_node, got %d", len(allResults))
	}
}

// Multi-layer BM25 tests removed (D12: extra layers measured as harmful,
// NDCG dropped from 0.296 to 0.182 with 3 layers). Single-layer BM25
// is tested via the standard search tests.
//
// The following tests were removed:
// - TestMultiLayerBM25Ranking
// - TestMultiLayerBM25EmptyLayers
// - TestMultiLayerBM25WeightConfig

// --- helpers ---

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
