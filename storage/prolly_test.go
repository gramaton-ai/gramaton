package storage

import (
	"fmt"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestProllyTreeEmpty(t *testing.T) {
	s := testStore(t)
	tree := NewProllyTree(s, ProllyConfig{})
	if tree.RootHash() != "" {
		t.Fatal("empty tree should have empty root hash")
	}

	_, ok := tree.Get("anything")
	if ok {
		t.Fatal("Get on empty tree should return false")
	}

	entries, err := tree.AllEntries()
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatal("empty tree should have no entries")
	}
}

func TestProllyTreeBuildAndGet(t *testing.T) {
	s := testStore(t)
	tree := NewProllyTree(s, ProllyConfig{})

	entries := make([]ProllyEntry, 100)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("node-%04d", i), Value: fmt.Sprintf("hash-%04d", i)}
	}

	if err := tree.Build(entries); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if tree.RootHash() == "" {
		t.Fatal("built tree should have a root hash")
	}

	// Look up every entry.
	for _, e := range entries {
		val, ok := tree.Get(e.Key)
		if !ok {
			t.Fatalf("Get(%q): not found", e.Key)
		}
		if val != e.Value {
			t.Fatalf("Get(%q): expected %q, got %q", e.Key, e.Value, val)
		}
	}

	// Look up non-existent key.
	_, ok := tree.Get("nonexistent")
	if ok {
		t.Fatal("Get should return false for missing key")
	}
}

func TestProllyTreeAllEntries(t *testing.T) {
	s := testStore(t)
	tree := NewProllyTree(s, ProllyConfig{})

	entries := make([]ProllyEntry, 200)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	tree.Build(entries)

	got, err := tree.AllEntries()
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("expected 200 entries, got %d", len(got))
	}

	// Verify sorted order.
	for i := 1; i < len(got); i++ {
		if got[i].Key <= got[i-1].Key {
			t.Fatalf("entries not sorted at index %d", i)
		}
	}
}

func TestProllyTreeEntryCount(t *testing.T) {
	s := testStore(t)
	tree := NewProllyTree(s, ProllyConfig{})

	entries := make([]ProllyEntry, 150)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree.Build(entries)

	count, err := tree.EntryCount()
	if err != nil {
		t.Fatalf("EntryCount: %v", err)
	}
	if count != 150 {
		t.Fatalf("expected 150, got %d", count)
	}
}

func TestProllyTreeLoadFromRoot(t *testing.T) {
	s := testStore(t)
	tree := NewProllyTree(s, ProllyConfig{})

	entries := make([]ProllyEntry, 50)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree.Build(entries)

	// Load from the saved root hash.
	tree2 := LoadProllyTree(s, tree.RootHash())
	val, ok := tree2.Get("k0025")
	if !ok || val != "v0025" {
		t.Fatal("loaded tree should be able to look up entries")
	}
}

func TestProllyTreeDiffIdentical(t *testing.T) {
	s := testStore(t)

	entries := make([]ProllyEntry, 100)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries)

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries)

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("identical trees should have no diff, got %d added, %d removed", len(added), len(removed))
	}
}

func TestProllyTreeDiffAdded(t *testing.T) {
	s := testStore(t)

	entries1 := make([]ProllyEntry, 100)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	entries2 := make([]ProllyEntry, 101)
	copy(entries2, entries1)
	entries2[100] = ProllyEntry{Key: "k0100", Value: "v0100"}

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries1)

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries2)

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(added))
	}
	if added[0].Key != "k0100" {
		t.Fatalf("expected added key k0100, got %q", added[0].Key)
	}
	if len(removed) != 0 {
		t.Fatalf("expected 0 removed, got %d", len(removed))
	}
}

func TestProllyTreeDiffRemoved(t *testing.T) {
	s := testStore(t)

	entries1 := make([]ProllyEntry, 100)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	entries2 := make([]ProllyEntry, 99)
	copy(entries2, entries1[:99]) // Remove the last entry.

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries1)

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries2)

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(removed))
	}
	if removed[0].Key != "k0099" {
		t.Fatalf("expected removed key k0099, got %q", removed[0].Key)
	}
	if len(added) != 0 {
		t.Fatalf("expected 0 added, got %d", len(added))
	}
}

func TestProllyTreeDiffModified(t *testing.T) {
	s := testStore(t)

	entries1 := make([]ProllyEntry, 100)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	entries2 := make([]ProllyEntry, 100)
	copy(entries2, entries1)
	entries2[50].Value = "modified" // Change one value.

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries1)

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries2)

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// Modified = old version removed + new version added (same key, different value).
	if len(added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(added))
	}
	if len(removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(removed))
	}
	if added[0].Key != "k0050" || removed[0].Key != "k0050" {
		t.Fatal("modified entry should have same key in added and removed")
	}
}

func TestProllyTreeDiffSharesChunks(t *testing.T) {
	s := testStore(t)

	// Build two trees with 1000 entries, differing by one.
	entries1 := make([]ProllyEntry, 1000)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	entries2 := make([]ProllyEntry, 1001)
	copy(entries2, entries1)
	entries2[1000] = ProllyEntry{Key: "k1000", Value: "v1000"}

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries1)
	chunksBefore, _ := s.List()

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries2)
	chunksAfter, _ := s.List()

	// The second tree should reuse most chunks from the first.
	// New chunks should be a small fraction of total.
	newChunks := len(chunksAfter) - len(chunksBefore)
	totalChunks := len(chunksAfter)

	if newChunks > totalChunks/2 {
		t.Fatalf("expected chunk sharing: %d new out of %d total", newChunks, totalChunks)
	}
}

func TestProllyTreeDiffEmptyToFull(t *testing.T) {
	s := testStore(t)

	empty := NewProllyTree(s, ProllyConfig{})

	entries := make([]ProllyEntry, 50)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%02d", i), Value: fmt.Sprintf("v%02d", i)}
	}
	full := NewProllyTree(s, ProllyConfig{})
	full.Build(entries)

	added, removed, err := empty.Diff(full)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 50 {
		t.Fatalf("expected 50 added, got %d", len(added))
	}
	if len(removed) != 0 {
		t.Fatalf("expected 0 removed, got %d", len(removed))
	}
}

func TestProllyTreeSortedEntries(t *testing.T) {
	m := map[string]string{
		"z": "3",
		"a": "1",
		"m": "2",
	}

	entries := SortedEntries(m)
	if len(entries) != 3 {
		t.Fatalf("expected 3, got %d", len(entries))
	}
	if entries[0].Key != "a" || entries[1].Key != "m" || entries[2].Key != "z" {
		t.Fatalf("not sorted: %v", entries)
	}
}

func TestProllyTreeDeterministic(t *testing.T) {
	s := testStore(t)

	entries := make([]ProllyEntry, 200)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	tree1 := NewProllyTree(s, ProllyConfig{})
	tree1.Build(entries)

	tree2 := NewProllyTree(s, ProllyConfig{})
	tree2.Build(entries)

	if tree1.RootHash() != tree2.RootHash() {
		t.Fatal("same entries should produce same root hash")
	}
}
