package graph

import (
	"path/filepath"
	"testing"
)

func TestDiffCommitsAdded(t *testing.T) {
	g := New()
	s := tempStorage(t)

	c1, _ := g.Save(s, "", "empty")

	g.AddNode(Properties{"x": StringProperty("new")})
	c2, _ := g.Save(s, c1.Hash, "add node")

	diff := DiffCommits(c1, c2)
	if len(diff.AddedNodes) != 1 {
		t.Fatalf("expected 1 added node, got %d", len(diff.AddedNodes))
	}
	if len(diff.RemovedNodes) != 0 {
		t.Fatalf("expected 0 removed nodes, got %d", len(diff.RemovedNodes))
	}
}

func TestDiffCommitsRemoved(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"x": StringProperty("old")})
	c1, _ := g.Save(s, "", "with node")

	g.DeleteNode(n.ID)
	c2, _ := g.Save(s, c1.Hash, "remove node")

	diff := DiffCommits(c1, c2)
	if len(diff.RemovedNodes) != 1 {
		t.Fatalf("expected 1 removed node, got %d", len(diff.RemovedNodes))
	}
	if len(diff.AddedNodes) != 0 {
		t.Fatalf("expected 0 added nodes, got %d", len(diff.AddedNodes))
	}
}

func TestDiffCommitsModified(t *testing.T) {
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"x": StringProperty("v1")})
	c1, _ := g.Save(s, "", "v1")

	g.SetNodeProperty(n.ID, "x", StringProperty("v2"))
	c2, _ := g.Save(s, c1.Hash, "v2")

	diff := DiffCommits(c1, c2)
	// Modified = old hash removed + new hash added.
	if len(diff.AddedNodes) != 1 {
		t.Fatalf("expected 1 added (new version), got %d", len(diff.AddedNodes))
	}
	if len(diff.RemovedNodes) != 1 {
		t.Fatalf("expected 1 removed (old version), got %d", len(diff.RemovedNodes))
	}
}

func TestDiffCommitsEdges(t *testing.T) {
	g := New()
	s := tempStorage(t)

	a := g.AddNode(nil)
	b := g.AddNode(nil)
	c1, _ := g.Save(s, "", "no edges")

	g.AddEdge(a.ID, b.ID, "test", 0.5, nil)
	c2, _ := g.Save(s, c1.Hash, "add edge")

	diff := DiffCommits(c1, c2)
	if len(diff.AddedEdges) != 1 {
		t.Fatalf("expected 1 added edge, got %d", len(diff.AddedEdges))
	}
}

func TestDiffCommitsIdentical(t *testing.T) {
	g := New()
	s := tempStorage(t)

	g.AddNode(Properties{"x": StringProperty("same")})
	c1, _ := g.Save(s, "", "first")
	c2, _ := g.Save(s, c1.Hash, "second")

	diff := DiffCommits(c1, c2)
	if len(diff.AddedNodes) != 0 || len(diff.RemovedNodes) != 0 {
		t.Fatal("identical state should produce empty diff")
	}
}

func TestDiffRequiresStorage(t *testing.T) {
	// tempStorage is from commit_test.go in the same package.
	_ = filepath.Join(t.TempDir(), "chunks")
}
