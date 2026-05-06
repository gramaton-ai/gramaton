package index

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newTestBboltBM25(t *testing.T) *BboltBM25Index {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "test.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	idx, err := NewBboltBM25Index(db, 1.2, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestBboltBM25AddAndSearch(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "the quick brown fox jumps over the lazy dog")
	idx.Add("n2", "a fast brown fox leaps over a sleepy cat")
	idx.Add("n3", "database migration strategy for PostgreSQL")

	if idx.Len() != 3 {
		t.Fatalf("expected 3 docs, got %d", idx.Len())
	}

	results := idx.Search(Tokenize("brown fox"), 10, nil)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results for 'brown fox', got %d", len(results))
	}
	// n1 and n2 should rank above n3.
	for _, r := range results {
		if r.NodeID == "n3" && r.Similarity > results[0].Similarity {
			t.Fatal("n3 should not outrank n1/n2 for 'brown fox'")
		}
	}
}

func TestBboltBM25Remove(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "alpha beta gamma")
	idx.Add("n2", "alpha delta epsilon")

	idx.Remove("n1")

	if idx.Len() != 1 {
		t.Fatalf("expected 1 after remove, got %d", idx.Len())
	}

	results := idx.Search(Tokenize("alpha"), 10, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].NodeID != "n2" {
		t.Fatalf("expected n2, got %s", results[0].NodeID)
	}
}

func TestBboltBM25FilteredSearch(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "kubernetes container orchestration")
	idx.Add("n2", "kubernetes pod networking")
	idx.Add("n3", "docker container runtime")

	candidates := map[string]struct{}{"n1": {}, "n3": {}}
	results := idx.Search(Tokenize("container"), 10, candidates)
	if len(results) != 2 {
		t.Fatalf("expected 2 filtered results, got %d", len(results))
	}
	for _, r := range results {
		if r.NodeID == "n2" {
			t.Fatal("n2 should not appear in filtered results")
		}
	}
}

func TestBboltBM25Replace(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "original content about cats")
	idx.Add("n1", "replaced content about dogs")

	if idx.Len() != 1 {
		t.Fatalf("expected 1 after replace, got %d", idx.Len())
	}

	results := idx.Search(Tokenize("dogs"), 10, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1 for 'dogs' after replace, got %v", results)
	}

	results = idx.Search(Tokenize("cats"), 10, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for 'cats' after replace, got %d", len(results))
	}
}

func TestBboltBM25Persistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Write.
	db1, _ := bolt.Open(dbPath, 0600, nil)
	idx1, _ := NewBboltBM25Index(db1, 1.2, 0.75)
	idx1.Add("n1", "persistent data storage")
	idx1.Add("n2", "ephemeral cache layer")
	db1.Close()

	// Reopen.
	db2, _ := bolt.Open(dbPath, 0600, nil)
	defer db2.Close()
	idx2, _ := NewBboltBM25Index(db2, 1.2, 0.75)

	if idx2.Len() != 2 {
		t.Fatalf("expected 2 after reopen, got %d", idx2.Len())
	}

	results := idx2.Search(Tokenize("storage"), 10, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1 for 'storage' after reopen, got %v", results)
	}
}

func TestBboltBM25PreTokenized(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.AddPreTokenized("n1", map[string]int{
		"golang":   3,
		"database": 2,
		"bbolt":    1,
	}, 6)

	if idx.Len() != 1 {
		t.Fatalf("expected 1, got %d", idx.Len())
	}

	results := idx.Search([]string{"golang"}, 10, nil)
	if len(results) != 1 || results[0].NodeID != "n1" {
		t.Fatalf("expected n1, got %v", results)
	}
}

func TestBboltBM25Empty(t *testing.T) {
	idx := newTestBboltBM25(t)

	results := idx.Search(Tokenize("anything"), 10, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results on empty index, got %d", len(results))
	}
}

func TestBboltBM25ReverseIndexRemove(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "alpha beta gamma delta")
	idx.Add("n2", "alpha epsilon zeta")

	// n1 has 4 terms. Remove should clean up all 4 posting lists.
	idx.Remove("n1")

	// alpha should now only return n2.
	results := idx.Search(Tokenize("alpha"), 10, nil)
	if len(results) != 1 || results[0].NodeID != "n2" {
		t.Fatalf("expected only n2 for alpha after remove, got %v", results)
	}

	// beta/gamma/delta should return nothing.
	for _, term := range []string{"beta", "gamma", "delta"} {
		results = idx.Search(Tokenize(term), 10, nil)
		if len(results) != 0 {
			t.Fatalf("expected 0 results for %s after remove, got %d", term, len(results))
		}
	}
}

func TestBboltBM25IncrementalTotalLen(t *testing.T) {
	idx := newTestBboltBM25(t)

	idx.Add("n1", "one two three") // 3 tokens
	idx.Add("n2", "four five")     // 2 tokens

	if idx.totalLen != 5 {
		t.Fatalf("expected totalLen 5, got %d", idx.totalLen)
	}

	idx.Remove("n1") // subtract 3
	if idx.totalLen != 2 {
		t.Fatalf("expected totalLen 2 after remove, got %d", idx.totalLen)
	}

	idx.Add("n2", "six seven eight nine") // replace: subtract 2, add 4
	if idx.totalLen != 4 {
		t.Fatalf("expected totalLen 4 after replace, got %d", idx.totalLen)
	}
}

// TestBboltBM25Batch exercises the AddTx + FlushBatchTx path that
// replaced the old SetBatch/idx.Batch pattern.
func TestBboltBM25Batch(t *testing.T) {
	idx := newTestBboltBM25(t)

	batch := NewBM25Batch()
	if err := idx.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i < 100; i++ {
			idx.AddTx(tx, batch, "n"+string(rune('A'+i%26)), "batch test content number")
		}
		idx.FlushBatchTx(tx, batch)
		return nil
	}); err != nil {
		t.Fatalf("batch update: %v", err)
	}

	if idx.Len() == 0 {
		t.Fatal("expected non-zero length after batch")
	}
}
