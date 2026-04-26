package search

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestScanConceptMatchesAboveThreshold pins Phase 1 telemetry: a
// concept whose embedding has cosine >= threshold against the query
// is returned with its keyword and live member IDs. Concepts below
// threshold are skipped. Tracker 01KQ5JVY5DY7B0WNGBMKG1C3ND.
func TestScanConceptMatchesAboveThreshold(t *testing.T) {
	g := graph.New()
	now := time.Now().UTC()

	queryVec := []float32{1, 0, 0}

	// Concept 1: aligned with query → cosine 1.0.
	c1 := g.AddNode(graph.Properties{
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty("aligned"),
		"embedding_full":  graph.VectorProperty([]float32{1, 0, 0}),
		"created_at":      graph.TimestampProperty(now),
	})

	// Concept 2: orthogonal → cosine 0.0; should be skipped.
	c2 := g.AddNode(graph.Properties{
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty("orthogonal"),
		"embedding_full":  graph.VectorProperty([]float32{0, 1, 0}),
		"created_at":      graph.TimestampProperty(now),
	})

	// Two members for concept 1, one is historical (valid_until in the
	// past) and must be filtered out of LiveMembers.
	memberLive := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("live member"),
		"created_at":   graph.TimestampProperty(now.Add(-time.Hour)),
	})
	memberHist := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("historical member"),
		"created_at":   graph.TimestampProperty(now.Add(-48 * time.Hour)),
		"valid_until":  graph.TimestampProperty(now.Add(-time.Hour)),
	})
	g.AddEdge(memberLive.ID, c1.ID, "instance_of", 0.8, nil)
	g.AddEdge(memberHist.ID, c1.ID, "instance_of", 0.8, nil)

	matches := ScanConceptMatches(g, queryVec, 0.7)

	if len(matches) != 1 {
		t.Fatalf("ScanConceptMatches: got %d matches, want 1 (only c1 above threshold)", len(matches))
	}
	got := matches[0]
	if got.ID != c1.ID {
		t.Errorf("matched concept id: got %q, want %q", got.ID, c1.ID)
	}
	if got.Keyword != "aligned" {
		t.Errorf("matched keyword: got %q, want aligned", got.Keyword)
	}
	if got.Cosine < 0.99 {
		t.Errorf("cosine: got %f, want ~1.0", got.Cosine)
	}
	if len(got.LiveMembers) != 1 || got.LiveMembers[0] != memberLive.ID {
		t.Errorf("live members: got %v, want [%s] (historical member must be filtered)",
			got.LiveMembers, memberLive.ID)
	}

	// c2 shouldn't appear at any threshold below ~0.0.
	_ = c2
}

// TestScanConceptMatchesEmptyInputs verifies the fail-silent paths:
// empty query vector or zero/negative threshold returns nil.
func TestScanConceptMatchesEmptyInputs(t *testing.T) {
	g := graph.New()

	if got := ScanConceptMatches(g, nil, 0.7); got != nil {
		t.Errorf("nil queryVec: got %v, want nil", got)
	}
	if got := ScanConceptMatches(g, []float32{1, 0, 0}, 0); got != nil {
		t.Errorf("threshold=0: got %v, want nil", got)
	}
}

// TestScanConceptMatchesSkipsDimensionMismatch ensures concepts whose
// embeddings have a different dimension (e.g., from a stale model
// before reembed completes) are skipped, not crashed on.
func TestScanConceptMatchesSkipsDimensionMismatch(t *testing.T) {
	g := graph.New()
	now := time.Now().UTC()
	queryVec := []float32{1, 0, 0}

	g.AddNode(graph.Properties{
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty("wrong-dim"),
		"embedding_full":  graph.VectorProperty([]float32{1, 0, 0, 0, 0}), // 5-dim, not 3
		"created_at":      graph.TimestampProperty(now),
	})

	matches := ScanConceptMatches(g, queryVec, 0.7)
	if len(matches) != 0 {
		t.Errorf("dimension mismatch should be skipped silently; got %d matches", len(matches))
	}
}
