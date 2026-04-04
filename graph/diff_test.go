package graph

import (
	"testing"
)

func TestDiffCommitsAdded(t *testing.T) {
	g := New()
	s := tempStorage(t)

	c1, _ := g.Save(s, "", "empty")

	g.AddNode(Properties{"x": StringProperty("new")})
	c2, _ := g.Save(s, c1.Hash, "add node")

	diff, err := DiffCommits(s, c1, c2)
	if err != nil {
		t.Fatalf("DiffCommits: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(diff.Added))
	}
	if len(diff.Removed) != 0 {
		t.Fatalf("expected 0 removed, got %d", len(diff.Removed))
	}
}

func TestDiffCommitsRemoved(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"x": StringProperty("old")})
	c1, _ := g.Save(s, "", "with node")

	g.DeleteNode(n.ID)
	c2, _ := g.Save(s, c1.Hash, "remove node")

	diff, err := DiffCommits(s, c1, c2)
	if err != nil {
		t.Fatalf("DiffCommits: %v", err)
	}
	if len(diff.Removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(diff.Removed))
	}
	if len(diff.Added) != 0 {
		t.Fatalf("expected 0 added, got %d", len(diff.Added))
	}
}

func TestDiffCommitsModified(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"x": StringProperty("v1")})
	c1, _ := g.Save(s, "", "v1")

	g.SetNodeProperty(n.ID, "x", StringProperty("v2"))
	c2, _ := g.Save(s, c1.Hash, "v2")

	diff, err := DiffCommits(s, c1, c2)
	if err != nil {
		t.Fatalf("DiffCommits: %v", err)
	}
	// Modified = old hash removed + new hash added (same key).
	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added (new version), got %d", len(diff.Added))
	}
	if len(diff.Removed) != 1 {
		t.Fatalf("expected 1 removed (old version), got %d", len(diff.Removed))
	}
	// Both should have the same node ID as the key.
	if diff.Added[0].Key != n.ID {
		t.Fatalf("added key should be node ID %s, got %s", n.ID, diff.Added[0].Key)
	}
	if diff.Removed[0].Key != n.ID {
		t.Fatalf("removed key should be node ID %s, got %s", n.ID, diff.Removed[0].Key)
	}
}

func TestDiffCommitsIdentical(t *testing.T) {
	g := New()
	s := tempStorage(t)

	g.AddNode(Properties{"x": StringProperty("same")})
	c1, _ := g.Save(s, "", "first")
	c2, _ := g.Save(s, c1.Hash, "second")

	diff, err := DiffCommits(s, c1, c2)
	if err != nil {
		t.Fatalf("DiffCommits: %v", err)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatal("identical state should produce empty diff")
	}
}

func TestNodeIDsInCommit(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n1 := g.AddNode(Properties{"x": StringProperty("a")})
	n2 := g.AddNode(Properties{"x": StringProperty("b")})
	c, _ := g.Save(s, "", "two nodes")

	ids, err := NodeIDsInCommit(s, c.Hash)
	if err != nil {
		t.Fatalf("NodeIDsInCommit: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}

	idSet := map[string]bool{ids[0]: true, ids[1]: true}
	if !idSet[n1.ID] || !idSet[n2.ID] {
		t.Fatal("missing expected node IDs")
	}
}

func TestNodeHashInCommit(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"x": StringProperty("findme")})
	c, _ := g.Save(s, "", "test")

	hash, found, err := NodeHashInCommit(s, c.Hash, n.ID)
	if err != nil {
		t.Fatalf("NodeHashInCommit: %v", err)
	}
	if !found {
		t.Fatal("expected to find node")
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}

	// Verify the hash points to the right data.
	data, _ := s.Read(hash)
	loaded, _ := UnmarshalNode(data)
	if loaded.ID != n.ID {
		t.Fatal("hash pointed to wrong node")
	}

	// Non-existent node.
	_, found, _ = NodeHashInCommit(s, c.Hash, "nonexistent")
	if found {
		t.Fatal("should not find nonexistent node")
	}
}
