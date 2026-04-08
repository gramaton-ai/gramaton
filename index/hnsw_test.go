package index

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func randomVec(dim int, rng *rand.Rand) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

func TestHNSWEmpty(t *testing.T) {
	h := NewHNSWIndex(0, 0, 0)
	if h.Len() != 0 {
		t.Fatalf("Len = %d, want 0", h.Len())
	}
	results := h.Search([]float32{1, 0, 0}, 5, nil)
	if len(results) != 0 {
		t.Fatalf("expected no results from empty index")
	}
}

func TestHNSWAddAndSearch(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)

	// Add a few vectors.
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})
	h.Add("c", []float32{0, 0, 1})
	h.Add("d", []float32{0.9, 0.1, 0})

	if h.Len() != 4 {
		t.Fatalf("Len = %d, want 4", h.Len())
	}

	// Search for something close to "a".
	results := h.Search([]float32{1, 0, 0}, 2, nil)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	// "a" should be closest (exact match), "d" second.
	if results[0].NodeID != "a" {
		t.Errorf("closest should be 'a', got %q", results[0].NodeID)
	}
	if results[0].Similarity < 0.99 {
		t.Errorf("exact match similarity = %f, want ~1.0", results[0].Similarity)
	}
}

func TestHNSWRemove(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})
	h.Add("c", []float32{0, 0, 1})

	h.Remove("b")

	if h.Len() != 2 {
		t.Fatalf("Len = %d, want 2", h.Len())
	}

	results := h.Search([]float32{0, 1, 0}, 5, nil)
	for _, r := range results {
		if r.NodeID == "b" {
			t.Fatal("removed node 'b' should not appear in results")
		}
	}
}

func TestHNSWUpdate(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})

	// Update "a" to point in the opposite direction.
	h.Add("a", []float32{-1, 0, 0})

	if h.Len() != 2 {
		t.Fatalf("Len = %d after update, want 2", h.Len())
	}

	results := h.Search([]float32{-1, 0, 0}, 1, nil)
	if len(results) == 0 || results[0].NodeID != "a" {
		t.Fatalf("updated 'a' should be closest to {-1,0,0}")
	}
}

func TestHNSWCandidates(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0.9, 0.1, 0})
	h.Add("c", []float32{0, 0, 1})

	// Search with candidates excluding "a".
	candidates := map[string]struct{}{
		"b": {},
		"c": {},
	}
	results := h.Search([]float32{1, 0, 0}, 5, candidates)
	for _, r := range results {
		if r.NodeID == "a" {
			t.Fatal("'a' should be excluded by candidate filter")
		}
	}
	if len(results) == 0 {
		t.Fatal("expected results from candidate search")
	}
}

func TestHNSWRecall(t *testing.T) {
	// Test recall at moderate scale: 1000 vectors, 32 dimensions.
	rng := rand.New(rand.NewSource(12345))
	dim := 32
	n := 1000

	h := NewHNSWIndex(16, 100, 50)
	flat := NewFlatIndex()

	vecs := make(map[string][]float32, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n%04d", i)
		v := randomVec(dim, rng)
		vecs[id] = v
		h.Add(id, v)
		flat.Add(id, v)
	}

	// Run 20 random queries and check recall@10.
	k := 10
	totalRecall := 0.0
	queries := 20

	for q := 0; q < queries; q++ {
		query := randomVec(dim, rng)

		hnswResults := h.Search(query, k, nil)
		flatResults := flat.Search(query, k, nil)

		// Build ground truth set.
		truth := make(map[string]struct{}, k)
		for _, r := range flatResults {
			truth[r.NodeID] = struct{}{}
		}

		// Count how many HNSW results are in ground truth.
		hits := 0
		for _, r := range hnswResults {
			if _, ok := truth[r.NodeID]; ok {
				hits++
			}
		}
		totalRecall += float64(hits) / float64(k)
	}

	avgRecall := totalRecall / float64(queries)
	if avgRecall < 0.8 {
		t.Fatalf("average recall@%d = %.2f, want >= 0.80", k, avgRecall)
	}
	t.Logf("average recall@%d = %.2f (%d queries, %d vectors)", k, avgRecall, queries, n)
}

func TestHNSWMarshalRoundTrip(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})
	h.Add("c", []float32{0, 0, 1})
	h.Add("d", []float32{0.5, 0.5, 0})

	// Search before.
	query := []float32{1, 0, 0}
	before := h.Search(query, 4, nil)

	// Marshal.
	data, err := h.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	// Unmarshal.
	h2 := NewHNSWIndex(0, 0, 0)
	if err := h2.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}

	if h2.Len() != 4 {
		t.Fatalf("Len = %d, want 4", h2.Len())
	}
	if h2.M != h.M {
		t.Errorf("M = %d, want %d", h2.M, h.M)
	}

	// Search after -- should return same results.
	after := h2.Search(query, 4, nil)
	if len(after) != len(before) {
		t.Fatalf("result count: before=%d, after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].NodeID != after[i].NodeID {
			t.Errorf("result[%d]: before=%s, after=%s", i, before[i].NodeID, after[i].NodeID)
		}
		if math.Abs(float64(before[i].Similarity-after[i].Similarity)) > 1e-6 {
			t.Errorf("result[%d] score: before=%f, after=%f", i, before[i].Similarity, after[i].Similarity)
		}
	}
}

func TestHNSWMarshalEmpty(t *testing.T) {
	h := NewHNSWIndex(0, 0, 0)
	data, err := h.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	h2 := NewHNSWIndex(0, 0, 0)
	if err := h2.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if h2.Len() != 0 {
		t.Fatalf("Len = %d, want 0", h2.Len())
	}
}

func TestHNSWRemoveEntryPoint(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})

	// Remove the entry point.
	h.Remove(h.entryID)

	if h.Len() != 1 {
		t.Fatalf("Len = %d, want 1", h.Len())
	}

	// Should still be searchable.
	results := h.Search([]float32{0, 1, 0}, 1, nil)
	if len(results) == 0 {
		t.Fatal("expected results after removing entry point")
	}
}

func TestHNSWRemoveAll(t *testing.T) {
	h := NewHNSWIndex(4, 20, 20)
	h.Add("a", []float32{1, 0, 0})
	h.Add("b", []float32{0, 1, 0})

	h.Remove("a")
	h.Remove("b")

	if h.Len() != 0 {
		t.Fatalf("Len = %d, want 0", h.Len())
	}

	results := h.Search([]float32{1, 0, 0}, 1, nil)
	if len(results) != 0 {
		t.Fatal("expected no results from empty index")
	}
}
