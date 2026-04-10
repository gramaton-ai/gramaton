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
	tool := New(g, propIdx, nil, bm25, nil, nil, nil, cfg)

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

// --- Multi-layer BM25 weighted RRF ---

func TestMultiLayerBM25Ranking(t *testing.T) {
	// Two records about "database". Record A mentions "database" in all
	// three layers. Record B only mentions it in content_full.
	// A should rank higher because content_short (3x weight) and
	// content_medium (2x weight) boost its RRF score.
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	bm25Full := index.NewBM25Index(1.2, 0.75)
	bm25Medium := index.NewBM25Index(1.2, 0.75)
	bm25Short := index.NewBM25Index(1.2, 0.75)
	now := time.Now().UTC()

	a := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("We chose a database solution for the project"),
		"content_medium":    graph.StringProperty("Database selection for the project"),
		"content_short":     graph.StringProperty("database selection"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(now),
	})
	b := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("The database was mentioned briefly in passing during a long discussion about infrastructure and cloud services and other things"),
		"content_medium":    graph.StringProperty("Infrastructure and cloud discussion"),
		"content_short":     graph.StringProperty("infrastructure overview"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(now),
	})

	for _, n := range []*graph.Node{a, b} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
		full, _ := n.Properties.GetString("content_full")
		medium, _ := n.Properties.GetString("content_medium")
		short, _ := n.Properties.GetString("content_short")
		bm25Full.Add(n.ID, full)
		bm25Medium.Add(n.ID, medium)
		bm25Short.Add(n.ID, short)
	}

	cfg := defaultCfg()
	cfg.Search.BM25WeightFull = 1.0
	cfg.Search.BM25WeightMedium = 2.0
	cfg.Search.BM25WeightShort = 3.0

	tool := New(g, propIdx, nil, bm25Full, bm25Medium, bm25Short, nil, cfg)

	results, err := tool.ExecuteWithVector(context.Background(), Query{
		Text: "database",
		Top:  10,
	}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Record A should rank first because "database" appears in all three
	// layers, getting weighted RRF contributions from full(1x) + medium(2x)
	// + short(3x). Record B only has it in full(1x).
	if results[0].ID != a.ID {
		t.Errorf("expected record A (all-layer match) to rank first, got %s", results[0].ID)
	}
	if results[1].ID != b.ID {
		t.Errorf("expected record B (full-only match) to rank second, got %s", results[1].ID)
	}

	// A's score should be meaningfully higher than B's.
	if results[0].EffectiveScore <= results[1].EffectiveScore {
		t.Errorf("A's score (%.4f) should be higher than B's (%.4f)",
			results[0].EffectiveScore, results[1].EffectiveScore)
	}
}

func TestMultiLayerBM25EmptyLayers(t *testing.T) {
	// When medium and short layers are empty, search should still work
	// using only the full layer.
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	bm25Full := index.NewBM25Index(1.2, 0.75)
	bm25Medium := index.NewBM25Index(1.2, 0.75) // empty
	bm25Short := index.NewBM25Index(1.2, 0.75)  // empty
	now := time.Now().UTC()

	n := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("kafka event pipeline for real-time processing"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range n.Properties {
		propIdx.Add(n.ID, k, v)
	}
	bm25Full.Add(n.ID, "kafka event pipeline for real-time processing")
	// Medium and short are intentionally empty.

	cfg := defaultCfg()
	tool := New(g, propIdx, nil, bm25Full, bm25Medium, bm25Short, nil, cfg)

	results, err := tool.ExecuteWithVector(context.Background(), Query{
		Text: "kafka",
		Top:  10,
	}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from full layer only, got %d", len(results))
	}
}

func TestMultiLayerBM25WeightConfig(t *testing.T) {
	// Verify that changing weights actually changes ranking.
	// With equal weights, the record that matches in all layers still
	// wins (more RRF contributions), but the margin should be smaller.
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	bm25Full := index.NewBM25Index(1.2, 0.75)
	bm25Medium := index.NewBM25Index(1.2, 0.75)
	bm25Short := index.NewBM25Index(1.2, 0.75)
	now := time.Now().UTC()

	a := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("redis caching strategy"),
		"content_medium":    graph.StringProperty("redis caching"),
		"content_short":     graph.StringProperty("redis"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(now),
	})
	b := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("redis was considered but we went with memcached"),
		"content_medium":    graph.StringProperty("caching evaluation"),
		"content_short":     graph.StringProperty("caching decision"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(now),
	})

	for _, n := range []*graph.Node{a, b} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
		full, _ := n.Properties.GetString("content_full")
		medium, _ := n.Properties.GetString("content_medium")
		short, _ := n.Properties.GetString("content_short")
		bm25Full.Add(n.ID, full)
		bm25Medium.Add(n.ID, medium)
		bm25Short.Add(n.ID, short)
	}

	// With high short weight (3x), A should win because "redis" is in its short layer.
	cfg := defaultCfg()
	cfg.Search.BM25WeightFull = 1.0
	cfg.Search.BM25WeightMedium = 2.0
	cfg.Search.BM25WeightShort = 3.0
	tool := New(g, propIdx, nil, bm25Full, bm25Medium, bm25Short, nil, cfg)

	results, _ := tool.ExecuteWithVector(context.Background(), Query{Text: "redis", Top: 10}, nil)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != a.ID {
		t.Errorf("with 3x short weight, A (redis in short) should rank first")
	}
}

// --- helpers ---

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
