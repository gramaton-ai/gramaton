package index

import (
	"math"
	"testing"
)

func TestBM25Empty(t *testing.T) {
	idx := NewBM25Index(0, 0)
	results := idx.Search(Tokenize("hello world"), 10, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestBM25Defaults(t *testing.T) {
	idx := NewBM25Index(0, 0)
	if idx.k1 != 1.2 {
		t.Fatalf("expected k1=1.2, got %f", idx.k1)
	}
	if idx.b != 0.75 {
		t.Fatalf("expected b=0.75, got %f", idx.b)
	}
}

func TestBM25AddAndSearch(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "the quick brown fox jumps over the lazy dog")
	idx.Add("doc2", "the quick brown car drives over the hill")
	idx.Add("doc3", "consciousness and memory play a role in cognition")

	results := idx.Search(Tokenize("memory cognition"), 10, nil)
	if len(results) == 0 {
		t.Fatal("expected results for 'memory cognition'")
	}
	if results[0].NodeID != "doc3" {
		t.Fatalf("expected doc3 first, got %s", results[0].NodeID)
	}
}

func TestBM25TermFrequencyMatters(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "memory memory memory is important for cognition")
	idx.Add("doc2", "memory is sometimes discussed in philosophy")

	results := idx.Search(Tokenize("memory"), 10, nil)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// doc1 has higher TF for "memory", should score higher.
	if results[0].NodeID != "doc1" {
		t.Fatalf("expected doc1 (higher TF) first, got %s", results[0].NodeID)
	}
}

func TestBM25IDFMatters(t *testing.T) {
	idx := NewBM25Index(0, 0)
	// "the" appears in all docs (low IDF), "consciousness" in one (high IDF).
	idx.Add("doc1", "the consciousness of the mind")
	idx.Add("doc2", "the state of the art")
	idx.Add("doc3", "the theory of the everything")

	results := idx.Search(Tokenize("consciousness"), 10, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result for rare term, got %d", len(results))
	}
	if results[0].NodeID != "doc1" {
		t.Fatalf("expected doc1, got %s", results[0].NodeID)
	}
}

func TestBM25Candidates(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "consciousness and memory")
	idx.Add("doc2", "consciousness and perception")
	idx.Add("doc3", "consciousness and thought")

	candidates := map[string]struct{}{
		"doc1": {},
		"doc3": {},
	}

	results := idx.Search(Tokenize("consciousness"), 10, candidates)
	for _, r := range results {
		if r.NodeID == "doc2" {
			t.Fatal("doc2 should be excluded by candidate filter")
		}
	}
}

func TestBM25Remove(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "consciousness and memory")
	idx.Add("doc2", "consciousness and thought")

	if idx.Len() != 2 {
		t.Fatalf("expected 2 docs, got %d", idx.Len())
	}

	idx.Remove("doc1")
	if idx.Len() != 1 {
		t.Fatalf("expected 1 doc after remove, got %d", idx.Len())
	}

	results := idx.Search(Tokenize("memory"), 10, nil)
	if len(results) != 0 {
		t.Fatal("removed doc should not appear in results")
	}
}

func TestBM25Replace(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "old content about memory")
	idx.Add("doc1", "new content about perception")

	if idx.Len() != 1 {
		t.Fatalf("expected 1 doc after replace, got %d", idx.Len())
	}

	// Should find "perception" but not "memory".
	results := idx.Search(Tokenize("perception"), 10, nil)
	if len(results) != 1 || results[0].NodeID != "doc1" {
		t.Fatal("replaced doc should contain new content")
	}

	results = idx.Search(Tokenize("memory"), 10, nil)
	if len(results) != 0 {
		t.Fatal("replaced doc should not contain old content")
	}
}

func TestBM25TopK(t *testing.T) {
	idx := NewBM25Index(0, 0)
	for i := 0; i < 20; i++ {
		idx.Add("doc"+string(rune('A'+i)), "consciousness is important")
	}

	results := idx.Search(Tokenize("consciousness"), 5, nil)
	if len(results) != 5 {
		t.Fatalf("expected 5 results with top-k=5, got %d", len(results))
	}
}

func TestBM25LengthNormalization(t *testing.T) {
	idx := NewBM25Index(0, 0)
	// Short doc with "memory" once.
	idx.Add("short", "memory cognition")
	// Long doc with "memory" once plus lots of padding.
	idx.Add("long", "memory and many other words about various topics in philosophy "+
		"that discuss different aspects of mind and brain and neural processes "+
		"and computational models of intelligence and artificial systems")

	results := idx.Search(Tokenize("memory"), 10, nil)
	if len(results) < 2 {
		t.Fatal("expected 2 results")
	}
	// Short doc should score higher (b > 0 penalizes long docs).
	if results[0].NodeID != "short" {
		t.Fatalf("expected short doc first (length normalization), got %s", results[0].NodeID)
	}
}

func TestBM25MultipleQueryTerms(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "consciousness and memory in cognitive systems")
	idx.Add("doc2", "consciousness in philosophical tradition")
	idx.Add("doc3", "memory in computer science databases")

	results := idx.Search(Tokenize("consciousness memory"), 10, nil)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// doc1 matches both terms, should be first.
	if results[0].NodeID != "doc1" {
		t.Fatalf("expected doc1 (matches both terms) first, got %s", results[0].NodeID)
	}
}

func TestBM25ScoresPositive(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "test content for scoring")
	idx.Add("doc2", "other content for testing")

	results := idx.Search(Tokenize("test"), 10, nil)
	for _, r := range results {
		if r.Similarity <= 0 {
			t.Errorf("BM25 score should be positive, got %f for %s", r.Similarity, r.NodeID)
		}
		if math.IsNaN(float64(r.Similarity)) || math.IsInf(float64(r.Similarity), 0) {
			t.Errorf("BM25 score is NaN/Inf for %s", r.NodeID)
		}
	}
}

func TestBM25EmptyQuery(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Add("doc1", "some content")
	results := idx.Search(nil, 10, nil)
	if len(results) != 0 {
		t.Fatal("empty query should return no results")
	}
}

func TestBM25RemoveNonExistent(t *testing.T) {
	idx := NewBM25Index(0, 0)
	idx.Remove("nonexistent") // should not panic
	if idx.Len() != 0 {
		t.Fatal("should still be empty")
	}
}
