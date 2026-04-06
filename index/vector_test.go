package index

import (
	"math"
	"testing"
)

// --- Cosine similarity ---

func TestCosineSimilarityIdentical(t *testing.T) {
	v := []float32{1.0, 0.0, 0.0}
	sim := CosineSimilarity(v, v)
	if !approxEqual(sim, 1.0) {
		t.Fatalf("identical vectors: expected ~1.0, got %f", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	sim := CosineSimilarity(a, b)
	if !approxEqual(sim, 0.0) {
		t.Fatalf("orthogonal vectors: expected ~0.0, got %f", sim)
	}
}

func TestCosineSimilarityOpposite(t *testing.T) {
	a := []float32{1.0, 0.0}
	b := []float32{-1.0, 0.0}
	sim := CosineSimilarity(a, b)
	if !approxEqual(sim, -1.0) {
		t.Fatalf("opposite vectors: expected ~-1.0, got %f", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{0.0, 0.0}
	sim := CosineSimilarity(a, b)
	if sim != 0.0 {
		t.Fatalf("zero vector: expected 0.0, got %f", sim)
	}
}

func TestCosineSimilarityDifferentLengths(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{1.0}
	sim := CosineSimilarity(a, b)
	if sim != 0.0 {
		t.Fatalf("different lengths: expected 0.0, got %f", sim)
	}
}

func TestCosineSimilarityScaleInvariant(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{2.0, 4.0, 6.0} // Same direction, different magnitude.
	sim := CosineSimilarity(a, b)
	if !approxEqual(sim, 1.0) {
		t.Fatalf("scaled vectors: expected ~1.0, got %f", sim)
	}
}

// --- FlatIndex ---

func TestFlatIndexAddAndLen(t *testing.T) {
	idx := NewFlatIndex()
	if idx.Len() != 0 {
		t.Fatal("empty index should have 0 length")
	}

	idx.Add("n1", []float32{0.1, 0.2})
	idx.Add("n2", []float32{0.3, 0.4})
	if idx.Len() != 2 {
		t.Fatalf("expected 2, got %d", idx.Len())
	}
}

func TestFlatIndexAddReplace(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{0.1, 0.2})
	idx.Add("n1", []float32{0.9, 0.8})
	if idx.Len() != 1 {
		t.Fatal("replace should not increase count")
	}

	results := idx.Search([]float32{0.9, 0.8}, 1, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatal("search should find the updated vector")
	}
}

func TestFlatIndexAddDefensiveCopy(t *testing.T) {
	idx := NewFlatIndex()
	v := []float32{1.0, 2.0}
	idx.Add("n1", v)
	v[0] = 999.0

	results := idx.Search([]float32{1.0, 2.0}, 1, nil)
	if !approxEqual(results[0].Similarity, 1.0) {
		t.Fatal("Add did not copy: external mutation affected stored vector")
	}
}

func TestFlatIndexRemove(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{0.1, 0.2})
	idx.Add("n2", []float32{0.3, 0.4})
	idx.Remove("n1")

	if idx.Len() != 1 {
		t.Fatalf("expected 1 after remove, got %d", idx.Len())
	}

	results := idx.Search([]float32{0.1, 0.2}, 10, nil)
	for _, r := range results {
		if r.NodeID == "n1" {
			t.Fatal("removed node should not appear in results")
		}
	}
}

func TestFlatIndexRemoveNonexistent(t *testing.T) {
	idx := NewFlatIndex()
	idx.Remove("n1") // Should not panic.
}

// --- Search ---

func TestFlatIndexSearchBasic(t *testing.T) {
	idx := NewFlatIndex()
	// Three vectors in different directions.
	idx.Add("right", []float32{1.0, 0.0})
	idx.Add("up", []float32{0.0, 1.0})
	idx.Add("diagonal", []float32{1.0, 1.0})

	// Search toward "right".
	results := idx.Search([]float32{1.0, 0.0}, 3, nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// "right" should be first (similarity 1.0).
	if results[0].NodeID != "right" {
		t.Fatalf("expected 'right' first, got %q", results[0].NodeID)
	}
	if !approxEqual(results[0].Similarity, 1.0) {
		t.Fatalf("expected similarity ~1.0, got %f", results[0].Similarity)
	}

	// "diagonal" should be second (~0.707).
	if results[1].NodeID != "diagonal" {
		t.Fatalf("expected 'diagonal' second, got %q", results[1].NodeID)
	}

	// "up" should be last (similarity 0.0).
	if results[2].NodeID != "up" {
		t.Fatalf("expected 'up' last, got %q", results[2].NodeID)
	}
}

func TestFlatIndexSearchTopK(t *testing.T) {
	idx := NewFlatIndex()
	for i := 0; i < 20; i++ {
		v := make([]float32, 3)
		v[i%3] = 1.0
		idx.Add(nodeID(i), v)
	}

	results := idx.Search([]float32{1.0, 0.0, 0.0}, 5, nil)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
}

func TestFlatIndexSearchEmpty(t *testing.T) {
	idx := NewFlatIndex()
	results := idx.Search([]float32{1.0, 0.0}, 5, nil)
	if len(results) != 0 {
		t.Fatal("expected 0 results from empty index")
	}
}

func TestFlatIndexSearchZeroK(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0})
	results := idx.Search([]float32{1.0}, 0, nil)
	if len(results) != 0 {
		t.Fatal("expected 0 results for k=0")
	}
}

func TestFlatIndexSearchKLargerThanIndex(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0, 0.0})
	idx.Add("n2", []float32{0.0, 1.0})

	results := idx.Search([]float32{1.0, 0.0}, 100, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 (all results), got %d", len(results))
	}
}

func TestFlatIndexSearchDescendingOrder(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0, 0.0})
	idx.Add("n2", []float32{0.7, 0.7})
	idx.Add("n3", []float32{0.0, 1.0})

	results := idx.Search([]float32{1.0, 0.0}, 3, nil)
	for i := 1; i < len(results); i++ {
		if results[i].Similarity > results[i-1].Similarity {
			t.Fatal("results should be in descending similarity order")
		}
	}
}

// --- Search with candidates ---

func TestFlatIndexSearchWithCandidates(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0, 0.0})
	idx.Add("n2", []float32{0.9, 0.1})
	idx.Add("n3", []float32{0.0, 1.0})

	// Only search within n2 and n3.
	candidates := map[string]struct{}{
		"n2": {},
		"n3": {},
	}
	results := idx.Search([]float32{1.0, 0.0}, 10, candidates)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// n1 should not appear.
	for _, r := range results {
		if r.NodeID == "n1" {
			t.Fatal("n1 should be excluded by candidate set")
		}
	}
	// n2 should rank first (closer to query).
	if results[0].NodeID != "n2" {
		t.Fatalf("expected n2 first, got %q", results[0].NodeID)
	}
}

func TestFlatIndexSearchCandidatesNotInIndex(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0, 0.0})

	candidates := map[string]struct{}{
		"missing": {},
	}
	results := idx.Search([]float32{1.0, 0.0}, 10, candidates)
	if len(results) != 0 {
		t.Fatal("candidates not in index should produce 0 results")
	}
}

func TestFlatIndexSearchEmptyCandidates(t *testing.T) {
	idx := NewFlatIndex()
	idx.Add("n1", []float32{1.0, 0.0})

	candidates := map[string]struct{}{}
	results := idx.Search([]float32{1.0, 0.0}, 10, candidates)
	if len(results) != 0 {
		t.Fatal("empty candidate set should produce 0 results")
	}
}

// --- Interface compliance ---

func TestFlatIndexImplementsVectorIndex(t *testing.T) {
	var _ VectorIndex = (*FlatIndex)(nil)
}

// --- Helpers ---

func approxEqual(a, b float32) bool {
	return math.Abs(float64(a-b)) < 1e-5
}

func nodeID(i int) string {
	return string(rune('A' + i%26))
}
