package graph

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func openTestEdgeBolt(t *testing.T) *bolt.DB {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "edges.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestBboltEdgeStore(t *testing.T) *BboltEdgeStore {
	t.Helper()
	db := openTestEdgeBolt(t)
	s, err := NewBboltEdgeStore(db, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBboltEdgePutGet(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	e := &Edge{
		ID:       "e1",
		SourceID: "n1",
		TargetID: "n2",
		Type:     "related_to",
		Weight:   0.8,
		Properties: Properties{
			"note": StringProperty("test"),
		},
	}
	s.Put(e)

	got, ok := s.Get("e1")
	if !ok {
		t.Fatal("expected to find edge e1")
	}
	if got.SourceID != "n1" || got.TargetID != "n2" {
		t.Fatalf("unexpected source/target: %s/%s", got.SourceID, got.TargetID)
	}
	if got.Type != "related_to" {
		t.Fatalf("unexpected type: %s", got.Type)
	}
	if got.Weight != 0.8 {
		t.Fatalf("unexpected weight: %f", got.Weight)
	}
}

func TestBboltEdgeFromTo(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "rel"})
	s.Put(&Edge{ID: "e2", SourceID: "a", TargetID: "c", Type: "sup"})
	s.Put(&Edge{ID: "e3", SourceID: "b", TargetID: "a", Type: "rel"})

	fromA := s.From("a")
	if len(fromA) != 2 {
		t.Fatalf("expected 2 edges from a, got %d", len(fromA))
	}

	toA := s.To("a")
	if len(toA) != 1 {
		t.Fatalf("expected 1 edge to a, got %d", len(toA))
	}
	if toA[0].ID != "e3" {
		t.Fatalf("expected e3, got %s", toA[0].ID)
	}
}

func TestBboltEdgeByType(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "related_to"})
	s.Put(&Edge{ID: "e2", SourceID: "c", TargetID: "d", Type: "related_to"})
	s.Put(&Edge{ID: "e3", SourceID: "a", TargetID: "c", Type: "supersedes"})

	rel := s.ByType("related_to")
	if len(rel) != 2 {
		t.Fatalf("expected 2 related_to edges, got %d", len(rel))
	}

	sup := s.ByType("supersedes")
	if len(sup) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(sup))
	}
}

func TestBboltEdgeDelete(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "rel"})
	s.Put(&Edge{ID: "e2", SourceID: "a", TargetID: "c", Type: "rel"})

	s.Delete("e1")

	if _, ok := s.Get("e1"); ok {
		t.Fatal("e1 should be deleted")
	}

	fromA := s.From("a")
	if len(fromA) != 1 {
		t.Fatalf("expected 1 edge from a after delete, got %d", len(fromA))
	}

	if s.Count() != 1 {
		t.Fatalf("expected count 1, got %d", s.Count())
	}
}

func TestBboltEdgeCount(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	if s.Count() != 0 {
		t.Fatalf("expected 0, got %d", s.Count())
	}

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "r"})
	s.Put(&Edge{ID: "e2", SourceID: "b", TargetID: "c", Type: "r"})

	if s.Count() != 2 {
		t.Fatalf("expected 2, got %d", s.Count())
	}
}

func TestBboltEdgeForEach(t *testing.T) {
	s := newTestBboltEdgeStore(t)

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "r"})
	s.Put(&Edge{ID: "e2", SourceID: "b", TargetID: "c", Type: "s"})

	var ids []string
	s.ForEach(func(e *Edge) {
		ids = append(ids, e.ID)
	})
	if len(ids) != 2 {
		t.Fatalf("expected 2 edges in ForEach, got %d", len(ids))
	}
}

func TestBboltEdgePersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "edges.db")

	// Write.
	db1, _ := bolt.Open(dbPath, 0600, nil)
	s1, _ := NewBboltEdgeStore(db1, 100)
	s1.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "related_to", Weight: 0.9})
	db1.Close()

	// Reopen.
	db2, _ := bolt.Open(dbPath, 0600, nil)
	defer db2.Close()
	s2, _ := NewBboltEdgeStore(db2, 100)

	e, ok := s2.Get("e1")
	if !ok {
		t.Fatal("expected to find e1 after reopen")
	}
	if e.Weight != 0.9 {
		t.Fatalf("expected weight 0.9, got %f", e.Weight)
	}

	fromA := s2.From("a")
	if len(fromA) != 1 {
		t.Fatalf("expected 1 edge from a after reopen, got %d", len(fromA))
	}
}

func TestBboltEdgeCacheEviction(t *testing.T) {
	// Small cache to test eviction.
	db := openTestEdgeBolt(t)
	s, _ := NewBboltEdgeStore(db, 3)

	s.Put(&Edge{ID: "e1", SourceID: "a", TargetID: "b", Type: "r"})
	s.Put(&Edge{ID: "e2", SourceID: "a", TargetID: "c", Type: "r"})
	s.Put(&Edge{ID: "e3", SourceID: "a", TargetID: "d", Type: "r"})
	// Cache is full (3). Adding e4 should evict e1 from cache.
	s.Put(&Edge{ID: "e4", SourceID: "a", TargetID: "e", Type: "r"})

	// e1 should still be findable (from bbolt, not cache).
	e, ok := s.Get("e1")
	if !ok {
		t.Fatal("e1 should be findable even after cache eviction")
	}
	if e.SourceID != "a" {
		t.Fatalf("unexpected source: %s", e.SourceID)
	}
}
