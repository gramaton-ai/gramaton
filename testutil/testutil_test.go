package testutil

import (
	"testing"
)

func TestNewEngine(t *testing.T) {
	eng := NewEngine(t)
	if eng == nil {
		t.Fatal("engine should not be nil")
	}
	if eng.Graph() == nil {
		t.Fatal("graph should not be nil")
	}
}

func TestRecordBuilder(t *testing.T) {
	eng := NewEngine(t)

	id := Record("Test content").
		Temporality("durable").
		Confidence(0.9).
		KnowledgeType("semantic").
		Keywords("test", "builder").
		Summary("Test record").
		Importance(0.5).
		Add(t, eng)

	if id == "" {
		t.Fatal("id should not be empty")
	}

	eng.RLock()
	defer eng.RUnlock()

	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("node should exist")
	}
	if v, ok := n.Properties.GetString("content_full"); !ok || v != "Test content" {
		t.Fatalf("content_full = %q, want 'Test content'", v)
	}
	if v, ok := n.Properties.GetString("temporality"); !ok || v != "durable" {
		t.Fatalf("temporality = %q, want 'durable'", v)
	}
	if v, ok := n.Properties.GetFloat64("confidence"); !ok || v != 0.9 {
		t.Fatalf("confidence = %v, want 0.9", v)
	}
}

func TestRecordBuilderPending(t *testing.T) {
	eng := NewEngine(t)

	id := Record("Unclassified stuff").Pending().Add(t, eng)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if v, _ := n.Properties.GetString("processing_status"); v != "captured" {
		t.Fatalf("processing_status = %q, want 'captured'", v)
	}
}

func TestRecordBuilderEmbedding(t *testing.T) {
	eng := NewEngine(t)

	vec := []float32{0.5, 0.5, 0.0}
	id := Record("Embedded content").Embedding(vec).Add(t, eng)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetVector("embedding_full"); !ok {
		t.Fatal("embedding should be set")
	}
}

func TestEdge(t *testing.T) {
	eng := NewEngine(t)

	id1 := Record("Source").Add(t, eng)
	id2 := Record("Target").Add(t, eng)
	Edge(t, eng, id1, id2, "relates_to", 0.7)

	eng.RLock()
	defer eng.RUnlock()
	edges := eng.Graph().EdgesFrom(id1)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Type != "relates_to" {
		t.Fatalf("edge type = %q, want 'relates_to'", edges[0].Type)
	}
}

func TestPopulatedEngine(t *testing.T) {
	eng, s := PopulatedEngine(t)

	eng.RLock()
	defer eng.RUnlock()

	nodeCount := eng.Graph().NodeCount()
	// ~50 records + 3 chunks = ~53 nodes
	if nodeCount < 48 || nodeCount > 60 {
		t.Fatalf("expected ~53 nodes, got %d", nodeCount)
	}

	// Verify some specific records exist.
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"WorkReorg", s.WorkReorg},
		{"HealthAllergy", s.HealthAllergy},
		{"CookingRecipe", s.CookingRecipe},
		{"TravelSeat", s.TravelSeat},
		{"FinanceBudget", s.FinanceBudget},
		{"LearnRetention", s.LearnRetention},
		{"PersonVendor", s.PersonVendor},
		{"TodoOpen", s.TodoOpen},
		{"TodoCompleted", s.TodoCompleted},
		{"Orphan1", s.Orphan1},
		{"Pending1", s.Pending1},
		{"EphemeralRecent", s.EphemeralRecent},
		{"ChunkedParent", s.ChunkedParent},
		{"Chunk1", s.Chunk1},
	} {
		if _, ok := eng.Graph().GetNode(tc.id); !ok {
			t.Fatalf("%s: node should exist (id=%s)", tc.name, tc.id)
		}
	}

	// Verify edges exist.
	supersededEdges := 0
	contradictsEdges := 0
	relatedEdges := 0
	chunkEdges := 0
	discussesEdges := 0
	for _, id := range eng.Graph().AllNodeIDs() {
		for _, e := range eng.Graph().EdgesFrom(id) {
			switch e.Type {
			case "supersedes":
				supersededEdges++
			case "contradicts":
				contradictsEdges++
			case "relates_to":
				relatedEdges++
			case "chunk_of":
				chunkEdges++
			case "discusses":
				discussesEdges++
			}
		}
	}

	if supersededEdges < 1 {
		t.Fatal("expected at least 1 supersedes edge")
	}
	if contradictsEdges < 1 {
		t.Fatal("expected at least 1 contradicts edge")
	}
	if relatedEdges < 10 {
		t.Fatalf("expected at least 10 relates_to edges, got %d", relatedEdges)
	}
	if chunkEdges != 3 {
		t.Fatalf("expected 3 chunk_of edges, got %d", chunkEdges)
	}
	if discussesEdges < 2 {
		t.Fatalf("expected at least 2 discusses edges, got %d", discussesEdges)
	}

	// Verify resolution states.
	resolved := 0
	unresolved := 0
	for _, id := range []string{s.TodoOpen, s.TodoCompleted, s.TodoAbandoned, s.TodoObsolete, s.TodoOpenLow} {
		n, _ := eng.Graph().GetNode(id)
		if _, ok := n.Properties.GetString("resolution"); ok {
			resolved++
		} else {
			unresolved++
		}
	}
	if resolved != 3 {
		t.Fatalf("expected 3 resolved TODOs, got %d", resolved)
	}
	if unresolved != 2 {
		t.Fatalf("expected 2 unresolved TODOs, got %d", unresolved)
	}

	// Verify pending records.
	pendingCount := 0
	for _, id := range []string{s.Pending1, s.Pending2, s.Pending3} {
		n, _ := eng.Graph().GetNode(id)
		if v, _ := n.Properties.GetString("processing_status"); v == "captured" {
			pendingCount++
		}
	}
	if pendingCount != 3 {
		t.Fatalf("expected 3 pending records, got %d", pendingCount)
	}

	// Verify temporality distribution.
	temporalities := map[string]int{}
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if v, ok := n.Properties.GetString("temporality"); ok {
			temporalities[v]++
		}
	}
	for _, temp := range []string{"immutable", "durable", "temporal", "ephemeral"} {
		if temporalities[temp] == 0 {
			t.Fatalf("expected at least 1 %s record", temp)
		}
	}

	// Verify epistemic status distribution.
	statuses := map[string]int{}
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if v, ok := n.Properties.GetString("epistemic_status"); ok {
			statuses[v]++
		}
	}
	for _, es := range []string{"well_established", "probable", "speculative", "contested", "refuted"} {
		if statuses[es] == 0 {
			t.Fatalf("expected at least 1 %s record", es)
		}
	}

	// Verify knowledge type distribution.
	types := map[string]int{}
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if v, ok := n.Properties.GetString("knowledge_type"); ok {
			types[v]++
		}
	}
	for _, kt := range []string{"episodic", "semantic", "procedural", "reference"} {
		if types[kt] == 0 {
			t.Fatalf("expected at least 1 %s record", kt)
		}
	}
}
