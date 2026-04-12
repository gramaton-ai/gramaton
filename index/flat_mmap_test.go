package index

import (
	"math"
	"path/filepath"
	"testing"
)

func newTestMmapIndex(t *testing.T, dim int) *MmapFlatIndex {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vec.flat")
	idx, err := NewMmapFlatIndex(path, dim)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestMmapFlatAddAndSearch(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n2", []float32{0, 1, 0, 0})
	idx.Add("n3", []float32{0.9, 0.1, 0, 0})

	if idx.Len() != 3 {
		t.Fatalf("expected 3 vectors, got %d", idx.Len())
	}

	// Search for something close to n1.
	results := idx.Search([]float32{1, 0, 0, 0}, 2, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// n1 or n3 should be first (both close to query).
	if results[0].NodeID != "n1" && results[0].NodeID != "n3" {
		t.Fatalf("expected n1 or n3 first, got %s", results[0].NodeID)
	}
}

func TestMmapFlatFilteredSearch(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n2", []float32{0, 1, 0, 0})
	idx.Add("n3", []float32{0.9, 0.1, 0, 0})

	// Only search n2 and n3.
	candidates := map[string]struct{}{"n2": {}, "n3": {}}
	results := idx.Search([]float32{1, 0, 0, 0}, 10, candidates)
	if len(results) != 2 {
		t.Fatalf("expected 2 filtered results, got %d", len(results))
	}
	// n3 should rank higher (closer to [1,0,0,0]).
	if results[0].NodeID != "n3" {
		t.Fatalf("expected n3 first in filtered search, got %s", results[0].NodeID)
	}
}

func TestMmapFlatRemove(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n2", []float32{0, 1, 0, 0})

	idx.Remove("n1")

	if idx.Len() != 1 {
		t.Fatalf("expected 1 after remove, got %d", idx.Len())
	}

	results := idx.Search([]float32{1, 0, 0, 0}, 10, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].NodeID != "n2" {
		t.Fatalf("expected n2, got %s", results[0].NodeID)
	}
}

func TestMmapFlatReplace(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n1", []float32{0, 1, 0, 0}) // replace

	if idx.Len() != 1 {
		t.Fatalf("expected 1 after replace, got %d", idx.Len())
	}

	// Should now be close to [0,1,0,0], not [1,0,0,0].
	results := idx.Search([]float32{0, 1, 0, 0}, 1, nil)
	if len(results) != 1 {
		t.Fatal("expected 1 result")
	}
	if results[0].Similarity < 0.9 {
		t.Fatalf("expected high similarity after replace, got %.4f", results[0].Similarity)
	}
}

func TestMmapFlatPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vec.flat")

	// Write.
	idx1, err := NewMmapFlatIndex(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	idx1.Add("n1", []float32{1, 0, 0, 0})
	idx1.Add("n2", []float32{0, 1, 0, 0})
	idx1.Close()

	// Reopen.
	idx2, err := NewMmapFlatIndex(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	if idx2.Len() != 2 {
		t.Fatalf("expected 2 after reopen, got %d", idx2.Len())
	}

	results := idx2.Search([]float32{1, 0, 0, 0}, 1, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1 first after reopen, got %v", results)
	}
}

func TestMmapFlatEmpty(t *testing.T) {
	idx := newTestMmapIndex(t, 384)

	if idx.Len() != 0 {
		t.Fatalf("expected 0, got %d", idx.Len())
	}

	results := idx.Search(make([]float32, 384), 10, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results on empty index, got %d", len(results))
	}
}

func TestMmapFlatBufferAndFlush(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	// Add vectors (goes to buffer, not disk).
	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n2", []float32{0, 1, 0, 0})

	// Should be searchable from buffer before flush.
	results := idx.Search([]float32{1, 0, 0, 0}, 1, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1 from buffer search, got %v", results)
	}

	// Flush to disk.
	if err := idx.Flush(); err != nil {
		t.Fatal(err)
	}

	// Should still be searchable from mmap'd data.
	results = idx.Search([]float32{1, 0, 0, 0}, 1, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1 from mmap search after flush, got %v", results)
	}

	if idx.Len() != 2 {
		t.Fatalf("expected 2 after flush, got %d", idx.Len())
	}

	// Add another after flush (goes to new buffer).
	idx.Add("n3", []float32{0, 0, 1, 0})
	if idx.Len() != 3 {
		t.Fatalf("expected 3, got %d", idx.Len())
	}
}

func TestMmapFlatBufferRemove(t *testing.T) {
	idx := newTestMmapIndex(t, 4)

	idx.Add("n1", []float32{1, 0, 0, 0})
	idx.Add("n2", []float32{0, 1, 0, 0})

	// Remove from buffer (before flush).
	idx.Remove("n1")
	if idx.Len() != 1 {
		t.Fatalf("expected 1 after buffer remove, got %d", idx.Len())
	}

	results := idx.Search([]float32{1, 0, 0, 0}, 10, nil)
	if len(results) != 1 || results[0].NodeID != "n2" {
		t.Fatalf("expected only n2 after remove, got %v", results)
	}

	// Flush and verify.
	idx.Flush()
	results = idx.Search([]float32{0, 1, 0, 0}, 1, nil)
	if len(results) != 1 || results[0].NodeID != "n2" {
		t.Fatalf("expected n2 after flush, got %v", results)
	}
}

func TestMmapFlatDimensionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vec.flat")

	idx, _ := NewMmapFlatIndex(path, 384)
	idx.Close()

	// Reopen with wrong dimension.
	_, err := NewMmapFlatIndex(path, 128)
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
}

func TestQuantizeRoundTrip(t *testing.T) {
	// Verify that quantization preserves relative ordering.
	vecs := [][]float32{
		{0.5, 0.3, 0.1, 0.8},
		{0.5, 0.3, 0.1, 0.79},
		{0.0, 0.0, 1.0, 0.0},
	}

	scale := quantizationScale(4) // 4-dim test vectors
	q0 := quantizeF32ToU8(vecs[0], scale)
	q1 := quantizeF32ToU8(vecs[1], scale)
	q2 := quantizeF32ToU8(vecs[2], scale)

	// vecs[0] and vecs[1] should be very similar.
	sim01 := cosineSimU8(q0, q1)
	// vecs[0] and vecs[2] should be less similar.
	sim02 := cosineSimU8(q0, q2)

	if sim01 < sim02 {
		t.Fatalf("expected sim(0,1)=%.4f > sim(0,2)=%.4f", sim01, sim02)
	}
}

func TestCosineSimU8(t *testing.T) {
	// Identical vectors should have similarity ~1.0.
	a := []byte{128, 64, 200, 10}
	sim := cosineSimU8(a, a)
	if math.Abs(float64(sim)-1.0) > 0.001 {
		t.Fatalf("expected ~1.0 for identical vectors, got %.4f", sim)
	}
}

// TestQuantizeDiscrimination verifies that uint8 quantization preserves
// the similarity structure of L2-normalized embedding vectors. This
// catches the per-vector min-max bug where different vectors with
// similar relative distributions produce near-identical uint8 patterns.
func TestQuantizeDiscrimination(t *testing.T) {
	// Simulate L2-normalized 384-dim BERT embeddings.
	// Build two 384-dim vectors with different patterns (cosine sim ~0.6 in float32).
	const dim = 384
	vecA := make([]float32, dim)
	vecB := make([]float32, dim)
	for i := 0; i < dim; i++ {
		// Different patterns: sin vs cos with varying frequency.
		vecA[i] = float32(math.Sin(float64(i)*0.1)) * 0.05
		vecB[i] = float32(math.Cos(float64(i)*0.17)) * 0.05
	}
	// Near-duplicate of A.
	vecC := make([]float32, dim)
	copy(vecC, vecA)
	vecC[0] += 0.001

	scale := quantizationScale(dim)
	qA := quantizeF32ToU8(vecA, scale)
	qB := quantizeF32ToU8(vecB, scale)
	qC := quantizeF32ToU8(vecC, scale)

	simAB := cosineSimU8(qA, qB)
	simAC := cosineSimU8(qA, qC)
	simAA := cosineSimU8(qA, qA)

	t.Logf("sim(A,A) = %.4f", simAA)
	t.Logf("sim(A,B) = %.4f (different vectors)", simAB)
	t.Logf("sim(A,C) = %.4f (tiny perturbation)", simAC)

	// Self-similarity must be ~1.0.
	if math.Abs(float64(simAA)-1.0) > 0.01 {
		t.Errorf("self-similarity: got %.4f, want ~1.0", simAA)
	}

	// Different vectors: uint8 similarity is approximate and inflated
	// relative to float32. The dedup path (CheckDedup in engine.go)
	// recomputes with float32 for threshold decisions. Here we just
	// verify the uint8 similarity is meaningfully lower than self-similarity.
	if simAB > 0.999 {
		t.Errorf("different vectors indistinguishable after quantization: sim(A,B) = %.4f (want < 0.999)", simAB)
	}
	if simAB >= simAA {
		t.Errorf("different vectors should be less similar than self: sim(A,B)=%.4f >= sim(A,A)=%.4f", simAB, simAA)
	}

	// Near-identical vectors should still be very similar.
	if simAC < 0.99 {
		t.Errorf("near-identical vectors should be similar: sim(A,C) = %.4f (want > 0.99)", simAC)
	}
}

// TestQuantizeFixedRange verifies that quantization uses a fixed global
// range, not per-vector min-max. Two vectors with the same shape but
// different scales should produce DIFFERENT quantized patterns.
func TestQuantizeFixedRange(t *testing.T) {
	// Same shape, different scale.
	small := []float32{0.01, 0.02, -0.01, 0.03}
	large := []float32{0.1, 0.2, -0.1, 0.3}

	scale := quantizationScale(4) // 4-dim
	qSmall := quantizeF32ToU8(small, scale)
	qLarge := quantizeF32ToU8(large, scale)

	// With per-vector min-max, these would be IDENTICAL (same relative distribution).
	// With fixed-range, they must differ.
	identical := true
	for i := range qSmall {
		if qSmall[i] != qLarge[i] {
			identical = false
			break
		}
	}
	if identical {
		t.Fatal("quantized vectors are identical despite different scales -- per-vector scaling bug")
	}
}

// TestMmapFlatSearchDiscrimination is an integration test that verifies
// the full Add -> Search path produces discriminative similarity scores.
func TestMmapFlatSearchDiscrimination(t *testing.T) {
	idx := newTestMmapIndex(t, 8)

	// Three clearly different 8-dim vectors (L2-normalized-ish).
	idx.Add("cats", []float32{0.5, 0.3, -0.1, 0.2, -0.4, 0.3, 0.1, -0.2})
	idx.Add("dogs", []float32{0.4, 0.35, -0.1, 0.15, -0.35, 0.3, 0.15, -0.15})
	idx.Add("quantum", []float32{-0.3, 0.1, 0.5, -0.2, 0.3, -0.4, 0.2, 0.1})

	// Search for cats - should rank cats first, dogs second, quantum third.
	results := idx.Search([]float32{0.5, 0.3, -0.1, 0.2, -0.4, 0.3, 0.1, -0.2}, 3, nil)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].NodeID != "cats" {
		t.Errorf("expected cats first, got %s (sim=%.4f)", results[0].NodeID, results[0].Similarity)
	}
	if results[1].NodeID != "dogs" {
		t.Errorf("expected dogs second, got %s (sim=%.4f)", results[1].NodeID, results[1].Similarity)
	}
	if results[2].NodeID != "quantum" {
		t.Errorf("expected quantum third, got %s (sim=%.4f)", results[2].NodeID, results[2].Similarity)
	}

	// The similarity gap between cats-dogs and cats-quantum should be meaningful.
	catsDogs := results[1].Similarity
	catsQuantum := results[2].Similarity
	t.Logf("sim(cats,dogs)=%.4f, sim(cats,quantum)=%.4f", catsDogs, catsQuantum)

	if catsDogs-catsQuantum < 0.05 {
		t.Errorf("insufficient discrimination: sim gap = %.4f (want > 0.05)", catsDogs-catsQuantum)
	}
}
