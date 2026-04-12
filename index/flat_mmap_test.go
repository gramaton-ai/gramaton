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

	q0 := quantizeF32ToU8(vecs[0])
	q1 := quantizeF32ToU8(vecs[1])
	q2 := quantizeF32ToU8(vecs[2])

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
