package search

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// TestConceptScanRetrievalOptIn pins the derived-layer retrieval
// contract: a concept absent from the primary vector index is still
// retrievable with include_concepts (the cosine scan over its
// embedding_full), and stays invisible without it.
func TestConceptScanRetrievalOptIn(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	now := time.Now().UTC()
	queryVec := []float32{1, 0, 0}

	record := g.AddNode(graph.Properties{
		"content_short":  graph.StringProperty("a member record"),
		"embedding_full": graph.VectorProperty([]float32{0.9, 0.1, 0}),
		"created_at":     graph.TimestampProperty(now),
	})
	vecIdx.Add(record.ID, []float32{0.9, 0.1, 0})

	concept := g.AddNode(graph.Properties{
		"content_short":   graph.StringProperty("the crystallized pattern"),
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty("pattern"),
		"embedding_full":  graph.VectorProperty([]float32{1, 0, 0}),
		"created_at":      graph.TimestampProperty(now),
	})
	// Deliberately NOT added to vecIdx -- the derived-layer contract
	// keeps concepts out of the primary index.

	for _, n := range []*graph.Node{record, concept} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
	}

	tool := New(g, propIdx, vecIdx, nil, nil, config.Defaults())

	// Default: concepts excluded.
	results, err := tool.ExecuteWithVector(context.Background(), Query{Top: 10, ExcludeConcepts: true}, queryVec)
	if err != nil {
		t.Fatalf("ExecuteWithVector: %v", err)
	}
	for _, r := range results {
		if r.ID == concept.ID {
			t.Fatalf("concept surfaced without include_concepts: %+v", results)
		}
	}

	// Opt-in: the cosine scan retrieves it.
	results, err = tool.ExecuteWithVector(context.Background(), Query{Top: 10}, queryVec)
	if err != nil {
		t.Fatalf("ExecuteWithVector include: %v", err)
	}
	var conceptHit *Result
	for i, r := range results {
		if r.ID == concept.ID {
			conceptHit = &results[i]
		}
	}
	if conceptHit == nil {
		t.Fatalf("concept absent with include_concepts; results = %+v", results)
	}
	if conceptHit.MatchedBy != "concept_scan" {
		t.Fatalf("matched_by = %q, want concept_scan", conceptHit.MatchedBy)
	}
}

// TestFindDuplicatesSkipsConcepts pins the duplicates boundary: a
// concept sharing an embedding with its own member (its function, not
// duplication) never nominates a pair.
func TestFindDuplicatesSkipsConcepts(t *testing.T) {
	g := graph.New()
	vecIdx := index.NewFlatIndex()

	shared := []float32{0.5, 0.5, 0}
	a := g.AddNode(graph.Properties{
		"content_full":   graph.StringProperty("record one"),
		"embedding_full": graph.VectorProperty(shared),
	})
	b := g.AddNode(graph.Properties{
		"content_full":   graph.StringProperty("record two"),
		"embedding_full": graph.VectorProperty(shared),
	})
	concept := g.AddNode(graph.Properties{
		"content_full":   graph.StringProperty("the concept summarizing both"),
		"node_type":      graph.StringProperty("concept"),
		"embedding_full": graph.VectorProperty(shared),
	})
	// Legacy-store shape: the concept still sits in the vector index.
	for _, n := range []*graph.Node{a, b, concept} {
		vecIdx.Add(n.ID, shared)
	}

	pairs := FindDuplicates(g, vecIdx, 0.9, 50)
	if len(pairs) == 0 {
		t.Fatal("record pair not found")
	}
	for _, p := range pairs {
		if p.IDA == concept.ID || p.IDB == concept.ID {
			t.Fatalf("concept nominated as a duplicate candidate: %+v", p)
		}
	}
}
