package curation

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestDrainContradictionsNoLLMMarksInWindowPairs asserts the drain walks
// every pair in the contradiction similarity window that doesn't already
// have an edge and writes an artificial no_contradiction edge. Companion
// to TestDetectContradictionsSkipsPairsWithNoContradictionEdge: together
// they verify drain + subsequent skip round-trip.
func TestDrainContradictionsNoLLMMarksInWindowPairs(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95

	// Two records in the similarity window; no edge between them.
	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha observation", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta observation, similar-but-distinct", []float32{0.7, 0.7, 0.0})

	result, err := DrainContradictionsNoLLM(context.Background(), eng, cfg, nil)
	if err != nil {
		t.Fatalf("DrainContradictionsNoLLM: %v", err)
	}
	if result.PairsDrained != 1 {
		t.Fatalf("expected 1 pair drained, got %d (considered=%d)", result.PairsDrained, result.PairsConsidered)
	}

	// Verify the edge exists in one direction with artificial=true.
	eng.RLock()
	defer eng.RUnlock()
	var edge *graph.Edge
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.Type == "no_contradiction" && e.TargetID == idB {
			edge = e
			break
		}
	}
	if edge == nil {
		for _, e := range eng.Graph().EdgesFrom(idB) {
			if e.Type == "no_contradiction" && e.TargetID == idA {
				edge = e
				break
			}
		}
	}
	if edge == nil {
		t.Fatal("drain did not create a no_contradiction edge in either direction")
	}
	artificial, _ := edge.Properties.GetBool("artificial")
	if !artificial {
		t.Fatal("drained edge should carry artificial=true")
	}
	if _, ok := edge.Properties.GetTimestamp("checked_at"); !ok {
		t.Fatal("drained edge missing checked_at")
	}
}

// TestDrainContradictionsNoLLMSkipsAlreadyEdgedPairs asserts pairs with
// any existing edge (contradicts, supersedes, no_contradiction from a
// prior drain) are left alone. The drain must not double-write.
func TestDrainContradictionsNoLLMSkipsAlreadyEdgedPairs(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta, similar", []float32{0.7, 0.7, 0.0})

	// Pre-seed an existing contradicts edge between them.
	eng.Lock()
	if _, err := eng.Graph().AddEdge(idA, idB, "contradicts", 0.9, nil); err != nil {
		eng.Unlock()
		t.Fatalf("seed edge: %v", err)
	}
	eng.Unlock()

	result, err := DrainContradictionsNoLLM(context.Background(), eng, cfg, nil)
	if err != nil {
		t.Fatalf("DrainContradictionsNoLLM: %v", err)
	}
	if result.PairsDrained != 0 {
		t.Fatalf("expected 0 pairs drained (pre-existing edge), got %d", result.PairsDrained)
	}

	// Verify only the original edge exists (no extra no_contradiction edge).
	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.Type == "no_contradiction" {
			t.Fatal("drain should not add no_contradiction edge when a contradicts edge exists")
		}
	}
	for _, e := range eng.Graph().EdgesFrom(idB) {
		if e.Type == "no_contradiction" {
			t.Fatal("drain should not add no_contradiction edge when a contradicts edge exists (reverse)")
		}
	}
}

// TestDrainContradictionsNoLLMOutOfWindowSkipped asserts pairs with
// similarity outside [min, max] are not marked. Specifically, pairs at
// or above max (dedup territory) should stay uncounted.
func TestDrainContradictionsNoLLMOutOfWindowSkipped(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.85

	// Two records so dissimilar they won't hit min_sim.
	addProcessedNodeWithEmbedding(t, eng, "Auth tokens", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Database schema", []float32{0.0, 0.0, 1.0})

	result, err := DrainContradictionsNoLLM(context.Background(), eng, cfg, nil)
	if err != nil {
		t.Fatalf("DrainContradictionsNoLLM: %v", err)
	}
	if result.PairsDrained != 0 {
		t.Fatalf("expected 0 drained for dissimilar records, got %d", result.PairsDrained)
	}
}
