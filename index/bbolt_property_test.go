package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
	bolt "go.etcd.io/bbolt"
)

func openTestBolt(t *testing.T) *bolt.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := bolt.Open(filepath.Join(dir, "test.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestBboltPropIdx(t *testing.T) *BboltPropertyIndex {
	t.Helper()
	db := openTestBolt(t)
	idx, err := NewBboltPropertyIndex(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestBboltPropertyExactMatch(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "color", graph.StringProperty("red"))
	idx.Add("n2", "color", graph.StringProperty("blue"))
	idx.Add("n3", "color", graph.StringProperty("red"))

	reds := idx.Lookup("color", graph.StringProperty("red"))
	if len(reds) != 2 {
		t.Fatalf("expected 2 red nodes, got %d", len(reds))
	}

	blues := idx.Lookup("color", graph.StringProperty("blue"))
	if len(blues) != 1 {
		t.Fatalf("expected 1 blue node, got %d", len(blues))
	}

	greens := idx.Lookup("color", graph.StringProperty("green"))
	if len(greens) != 0 {
		t.Fatalf("expected 0 green nodes, got %d", len(greens))
	}
}

func TestBboltPropertyRemove(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "status", graph.StringProperty("active"))
	idx.Add("n2", "status", graph.StringProperty("active"))

	idx.Remove("n1", "status", graph.StringProperty("active"))

	active := idx.Lookup("status", graph.StringProperty("active"))
	if len(active) != 1 {
		t.Fatalf("expected 1 active node after remove, got %d", len(active))
	}
	if active[0] != "n2" {
		t.Fatalf("expected n2, got %s", active[0])
	}
}

func TestBboltPropertyRemoveNode(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	props := graph.Properties{
		"color":  graph.StringProperty("red"),
		"status": graph.StringProperty("active"),
	}
	idx.Add("n1", "color", props["color"])
	idx.Add("n1", "status", props["status"])

	idx.RemoveNode("n1", props)

	if len(idx.Lookup("color", graph.StringProperty("red"))) != 0 {
		t.Fatal("expected color=red to be empty after RemoveNode")
	}
	if len(idx.Lookup("status", graph.StringProperty("active"))) != 0 {
		t.Fatal("expected status=active to be empty after RemoveNode")
	}
}

func TestBboltPropertyKeyword(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	kws1 := graph.StringListProperty([]string{"go", "rust", "python"})
	kws2 := graph.StringListProperty([]string{"go", "java"})

	idx.Add("n1", "tags", kws1)
	idx.Add("n2", "tags", kws2)

	goNodes := idx.LookupKeyword("tags", "go")
	if len(goNodes) != 2 {
		t.Fatalf("expected 2 go nodes, got %d", len(goNodes))
	}

	rustNodes := idx.LookupKeyword("tags", "rust")
	if len(rustNodes) != 1 {
		t.Fatalf("expected 1 rust node, got %d", len(rustNodes))
	}

	counts := idx.KeywordCounts("tags")
	if counts["go"] != 2 {
		t.Fatalf("expected go count 2, got %d", counts["go"])
	}
	if counts["rust"] != 1 {
		t.Fatalf("expected rust count 1, got %d", counts["rust"])
	}
}

func TestBboltPropertySubstring(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "name", graph.StringProperty("Alice"))
	idx.Add("n2", "name", graph.StringProperty("Bob"))
	idx.Add("n3", "name", graph.StringProperty("alice jones"))

	// Case-sensitive.
	results := idx.Contains("name", "Alice")
	if len(results) != 1 {
		t.Fatalf("expected 1 Contains result, got %d", len(results))
	}

	// Case-insensitive.
	results = idx.ContainsFold("name", "alice")
	if len(results) != 2 {
		t.Fatalf("expected 2 ContainsFold results, got %d", len(results))
	}
}

func TestBboltPropertyNodesWithKey(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "status", graph.StringProperty("active"))
	idx.Add("n2", "status", graph.StringProperty("pending"))
	idx.Add("n3", "other", graph.StringProperty("x"))

	nodes := idx.NodesWithKey("status")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes with status, got %d", len(nodes))
	}
	if _, ok := nodes["n1"]; !ok {
		t.Fatal("missing n1")
	}
	if _, ok := nodes["n2"]; !ok {
		t.Fatal("missing n2")
	}
}

func TestBboltPropertyCount(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "a", graph.StringProperty("x"))
	idx.Add("n2", "a", graph.StringProperty("x"))
	idx.Add("n3", "b", graph.StringProperty("y"))

	count := idx.Count()
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}
}

func TestBboltPropertyPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Write data.
	db1, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	idx1, err := NewBboltPropertyIndex(db1, nil)
	if err != nil {
		t.Fatal(err)
	}
	idx1.Add("n1", "status", graph.StringProperty("active"))
	idx1.Add("n2", "tags", graph.StringListProperty([]string{"go", "rust"}))
	db1.Close()

	// Reopen and verify.
	db2, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	idx2, err := NewBboltPropertyIndex(db2, nil)
	if err != nil {
		t.Fatal(err)
	}

	active := idx2.Lookup("status", graph.StringProperty("active"))
	if len(active) != 1 || active[0] != "n1" {
		t.Fatalf("expected n1 active after reopen, got %v", active)
	}
	goNodes := idx2.LookupKeyword("tags", "go")
	if len(goNodes) != 1 || goNodes[0] != "n2" {
		t.Fatalf("expected n2 go after reopen, got %v", goNodes)
	}
}

func TestBboltPropertyDuplicateAdd(t *testing.T) {
	idx := newTestBboltPropIdx(t)

	idx.Add("n1", "color", graph.StringProperty("red"))
	idx.Add("n1", "color", graph.StringProperty("red")) // duplicate

	reds := idx.Lookup("color", graph.StringProperty("red"))
	if len(reds) != 1 {
		t.Fatalf("expected 1 red node (no dupes), got %d", len(reds))
	}
}

func TestBboltPropertySelectiveIndexing(t *testing.T) {
	db := openTestBolt(t)
	// Only index "status" and "tags", not "color".
	idx, err := NewBboltPropertyIndex(db, []string{"status", "tags"})
	if err != nil {
		t.Fatal(err)
	}

	idx.Add("n1", "status", graph.StringProperty("active"))
	idx.Add("n1", "color", graph.StringProperty("red"))        // NOT indexed
	idx.Add("n1", "meta.project", graph.StringProperty("foo")) // meta.* always indexed

	// status should be findable.
	if len(idx.Lookup("status", graph.StringProperty("active"))) != 1 {
		t.Fatal("expected to find status=active")
	}

	// color should NOT be findable (not in indexed fields).
	if len(idx.Lookup("color", graph.StringProperty("red"))) != 0 {
		t.Fatal("color should not be indexed")
	}

	// meta.* should always be findable.
	if len(idx.Lookup("meta.project", graph.StringProperty("foo"))) != 1 {
		t.Fatal("expected to find meta.project=foo")
	}
}

// Verify interface compliance.
var _ PropertyIndex = (*BboltPropertyIndex)(nil)

// Suppress unused import warning.
var _ = os.Remove
