package index

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func newTestSecondaryIdx(t *testing.T) *BboltSecondaryIndex {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "secondary.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	idx, err := NewBboltSecondaryIndex(db)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestSecondaryTimeCreatedAt(t *testing.T) {
	idx := newTestSecondaryIdx(t)
	now := time.Now().UTC()

	idx.SetCreatedAt("n1", now.Add(-3*time.Hour))
	idx.SetCreatedAt("n2", now.Add(-2*time.Hour))
	idx.SetCreatedAt("n3", now.Add(-1*time.Hour))

	recent := idx.RecentByCreatedAt(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent, got %d", len(recent))
	}
	if recent[0] != "n3" {
		t.Fatalf("expected n3 first (newest), got %s", recent[0])
	}
	if recent[1] != "n2" {
		t.Fatalf("expected n2 second, got %s", recent[1])
	}
}

func TestSecondaryTimeLastAccessed(t *testing.T) {
	idx := newTestSecondaryIdx(t)
	now := time.Now().UTC()

	idx.SetLastAccessed("a", now.Add(-10*time.Minute))
	idx.SetLastAccessed("b", now.Add(-5*time.Minute))
	idx.SetLastAccessed("c", now)

	recent := idx.RecentByLastAccessed(10)
	if len(recent) != 3 {
		t.Fatalf("expected 3, got %d", len(recent))
	}
	if recent[0] != "c" {
		t.Fatalf("expected c first, got %s", recent[0])
	}
}

func TestSecondaryTimeUpdate(t *testing.T) {
	idx := newTestSecondaryIdx(t)
	now := time.Now().UTC()

	idx.SetCreatedAt("n1", now.Add(-1*time.Hour))
	idx.SetCreatedAt("n1", now) // update to newer timestamp

	recent := idx.RecentByCreatedAt(10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 after update (dedup), got %d", len(recent))
	}
}

func TestSecondaryEdgeCounts(t *testing.T) {
	idx := newTestSecondaryIdx(t)

	idx.SetEdgeCounts("n1", 3, 2)
	idx.SetEdgeCounts("n2", 0, 0)
	idx.SetEdgeCounts("n3", 1, 0)

	in, out, ok := idx.GetEdgeCounts("n1")
	if !ok || in != 3 || out != 2 {
		t.Fatalf("n1: got in=%d out=%d ok=%v", in, out, ok)
	}

	_, _, ok = idx.GetEdgeCounts("nonexistent")
	if ok {
		t.Fatal("nonexistent should return ok=false")
	}
}

func TestSecondaryOrphans(t *testing.T) {
	idx := newTestSecondaryIdx(t)

	idx.SetEdgeCounts("n1", 3, 2)
	idx.SetEdgeCounts("n2", 0, 0) // orphan
	idx.SetEdgeCounts("n3", 0, 1)
	idx.SetEdgeCounts("n4", 0, 0) // orphan

	orphans := idx.Orphans()
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphans, got %d", len(orphans))
	}
}

func TestSecondaryFieldExists(t *testing.T) {
	idx := newTestSecondaryIdx(t)

	idx.SetFieldExists("temporality", "n1")
	idx.SetFieldExists("temporality", "n2")
	idx.SetFieldExists("resolution", "n1")

	nodes := idx.NodesWithField("temporality")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes with temporality, got %d", len(nodes))
	}

	nodes = idx.NodesWithField("resolution")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node with resolution, got %d", len(nodes))
	}

	nodes = idx.NodesWithField("nonexistent")
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes with nonexistent field, got %d", len(nodes))
	}
}

func TestSecondaryNodesMissingField(t *testing.T) {
	idx := newTestSecondaryIdx(t)

	idx.SetFieldExists("temporality", "n1")
	idx.SetFieldExists("temporality", "n3")

	missing := idx.NodesMissingField("temporality", []string{"n1", "n2", "n3", "n4"})
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %d", len(missing))
	}
	// n2 and n4 are missing.
	has := make(map[string]bool)
	for _, id := range missing {
		has[id] = true
	}
	if !has["n2"] || !has["n4"] {
		t.Fatalf("expected n2 and n4 missing, got %v", missing)
	}
}

func TestSecondaryRemoveNode(t *testing.T) {
	idx := newTestSecondaryIdx(t)
	now := time.Now().UTC()

	idx.SetCreatedAt("n1", now)
	idx.SetLastAccessed("n1", now)
	idx.SetEdgeCounts("n1", 1, 2)
	idx.SetFieldExists("temporality", "n1")

	idx.RemoveNode("n1")

	recent := idx.RecentByCreatedAt(10)
	if len(recent) != 0 {
		t.Fatal("n1 should be removed from created_at index")
	}

	_, _, ok := idx.GetEdgeCounts("n1")
	if ok {
		t.Fatal("n1 should be removed from edge counts")
	}
}

func TestSecondaryFieldExistsClear(t *testing.T) {
	idx := newTestSecondaryIdx(t)

	idx.SetFieldExists("temporality", "n1")
	idx.SetFieldExists("temporality", "n2")
	idx.ClearFieldExists("temporality", "n1")

	nodes := idx.NodesWithField("temporality")
	if len(nodes) != 1 || nodes[0] != "n2" {
		t.Fatalf("expected only n2, got %v", nodes)
	}
}
