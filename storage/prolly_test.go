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

// TestProllyTreeDiffSmallChangeInLargeTree exercises the merge-walk
// Diff fix:
// diffing two large trees that differ by a single key in the middle
// must walk only the path down to that change, not both entire trees.
// Correctness assertion is the visible one; the benchmark
// BenchmarkProllyDiffSmallChange quantifies the perf win. Before the
// fix this was O(N); after, it's O(log N) in the happy path.
func TestProllyTreeDiffSmallChangeInLargeTree(t *testing.T) {
	s := testStore(t)

	entries1 := make([]ProllyEntry, 2000)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("v%05d", i)}
	}
	tree1 := NewProllyTree(s, ProllyConfig{})
	if err := tree1.Build(entries1); err != nil {
		t.Fatalf("build tree1: %v", err)
	}

	// Change one entry deep in the middle.
	entries2 := make([]ProllyEntry, 2000)
	copy(entries2, entries1)
	entries2[1000] = ProllyEntry{Key: "k01000", Value: "v01000-updated"}
	tree2 := NewProllyTree(s, ProllyConfig{})
	if err := tree2.Build(entries2); err != nil {
		t.Fatalf("build tree2: %v", err)
	}

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 1 || added[0].Key != "k01000" || added[0].Value != "v01000-updated" {
		t.Errorf("expected single added {k01000, v01000-updated}, got %+v", added)
	}
	if len(removed) != 1 || removed[0].Key != "k01000" || removed[0].Value != "v01000" {
		t.Errorf("expected single removed {k01000, v01000}, got %+v", removed)
	}
}

// TestProllyTreeDiffBoundaryShift exercises the rebalanced-boundary
// path: the second tree adds 100 keys past the end of the first,
// which shifts chunk boundaries and produces internal-node children
// whose keys don't align one-to-one with the first tree's children.
// Correctness must hold through the merge-walk's "unmatched key"
// fallback.
func TestProllyTreeDiffBoundaryShift(t *testing.T) {
	s := testStore(t)

	entries1 := make([]ProllyEntry, 1000)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("v%05d", i)}
	}
	tree1 := NewProllyTree(s, ProllyConfig{})
	if err := tree1.Build(entries1); err != nil {
		t.Fatalf("build tree1: %v", err)
	}

	entries2 := make([]ProllyEntry, 1100)
	copy(entries2, entries1)
	for i := 1000; i < 1100; i++ {
		entries2[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("v%05d", i)}
	}
	tree2 := NewProllyTree(s, ProllyConfig{})
	if err := tree2.Build(entries2); err != nil {
		t.Fatalf("build tree2: %v", err)
	}

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 100 {
		t.Errorf("expected 100 added, got %d", len(added))
	}
	if len(removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(removed))
	}
	// Sanity: the added keys should be exactly k01000..k01099.
	seen := make(map[string]bool, len(added))
	for _, e := range added {
		seen[e.Key] = true
	}
	for i := 1000; i < 1100; i++ {
		if !seen[fmt.Sprintf("k%05d", i)] {
			t.Errorf("missing expected key k%05d in added", i)
		}
	}
}

// TestProllyTreeDiffDeepIdenticalSubtreesSkipped asserts that when
// two trees share a large identical subtree, the diff short-circuits
// on matching internal-node (key, value) pairs rather than walking
// the shared subtree end-to-end. Correctness is the observable
// assertion; the mechanism (hash-equality skip in diffNodes) is
// exercised by TestProllyTreeDiffSmallChangeInLargeTree's sibling-
// skipping behaviour and benchmarked by BenchmarkProllyDiffSmallChange.
func TestProllyTreeDiffDeepIdenticalSubtreesSkipped(t *testing.T) {
	s := testStore(t)

	// Two trees that share the first 1500 entries and differ in the
	// last 500 via a value rewrite.
	baseEntries := make([]ProllyEntry, 1500)
	for i := range baseEntries {
		baseEntries[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("v%05d", i)}
	}

	entries1 := make([]ProllyEntry, 2000)
	copy(entries1, baseEntries)
	for i := 1500; i < 2000; i++ {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("old-v%05d", i)}
	}

	entries2 := make([]ProllyEntry, 2000)
	copy(entries2, baseEntries)
	for i := 1500; i < 2000; i++ {
		entries2[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("new-v%05d", i)}
	}

	tree1 := NewProllyTree(s, ProllyConfig{})
	if err := tree1.Build(entries1); err != nil {
		t.Fatalf("build tree1: %v", err)
	}
	tree2 := NewProllyTree(s, ProllyConfig{})
	if err := tree2.Build(entries2); err != nil {
		t.Fatalf("build tree2: %v", err)
	}

	added, removed, err := tree1.Diff(tree2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// All 500 tail entries should show up; the shared 1500-entry
	// prefix should not contribute.
	if len(added) != 500 || len(removed) != 500 {
		t.Fatalf("expected 500 added + 500 removed, got %d added + %d removed",
			len(added), len(removed))
	}
	for _, e := range added {
		if !stringsHasPrefix(e.Value, "new-v") {
			t.Errorf("unexpected added value %q (expected new-v* from tail)", e.Value)
		}
	}
	for _, e := range removed {
		if !stringsHasPrefix(e.Value, "old-v") {
			t.Errorf("unexpected removed value %q (expected old-v* from tail)", e.Value)
		}
	}
}

// BenchmarkProllyDiffSmallChange is the perf evidence. Diffs
// two 2000-entry trees differing by a single key. Before the fix the
// diff walked all 2000 entries on both sides (the allEntries fallback
// in the internal-vs-internal branch). After the fix the merge-walk
// skips identical subtrees at every level and touches only the path
// down to the changed leaf. Expected ns/op: microseconds instead of
// milliseconds.
func BenchmarkProllyDiffSmallChange(b *testing.B) {
	s, err := New(filepath.Join(b.TempDir(), "chunks"))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	entries1 := make([]ProllyEntry, 2000)
	for i := range entries1 {
		entries1[i] = ProllyEntry{Key: fmt.Sprintf("k%05d", i), Value: fmt.Sprintf("v%05d", i)}
	}
	entries2 := make([]ProllyEntry, 2000)
	copy(entries2, entries1)
	entries2[1000] = ProllyEntry{Key: "k01000", Value: "v01000-updated"}

	tree1 := NewProllyTree(s, ProllyConfig{})
	if err := tree1.Build(entries1); err != nil {
		b.Fatalf("build tree1: %v", err)
	}
	tree2 := NewProllyTree(s, ProllyConfig{})
	if err := tree2.Build(entries2); err != nil {
		b.Fatalf("build tree2: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := tree1.Diff(tree2); err != nil {
			b.Fatalf("Diff: %v", err)
		}
	}
}

// stringsHasPrefix avoids importing strings into the test file just
// for one call.
func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
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

func TestProllyTreeUpdateInsert(t *testing.T) {
	s := testStore(t)

	// Build initial tree.
	entries := make([]ProllyEntry, 100)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree := NewProllyTree(s, ProllyConfig{})
	tree.Build(entries)

	// Insert a new key via Update.
	err := tree.Update([]ProllyMutation{
		{Key: "k0050a", Value: "vnew"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify the new key exists.
	val, ok := tree.Get("k0050a")
	if !ok || val != "vnew" {
		t.Fatalf("Get(k0050a) = %q, %v; want vnew, true", val, ok)
	}

	// Verify existing keys are intact.
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("k%04d", i)
		val, ok := tree.Get(key)
		if !ok {
			t.Fatalf("Get(%s) not found after insert", key)
		}
		if val != fmt.Sprintf("v%04d", i) {
			t.Fatalf("Get(%s) = %q, want v%04d", key, val, i)
		}
	}

	// Total count should be 101.
	count, _ := tree.EntryCount()
	if count != 101 {
		t.Fatalf("EntryCount = %d, want 101", count)
	}
}

func TestProllyTreeUpdateModify(t *testing.T) {
	s := testStore(t)

	entries := make([]ProllyEntry, 100)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree := NewProllyTree(s, ProllyConfig{})
	tree.Build(entries)

	// Update existing key.
	err := tree.Update([]ProllyMutation{
		{Key: "k0042", Value: "updated"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	val, ok := tree.Get("k0042")
	if !ok || val != "updated" {
		t.Fatalf("Get(k0042) = %q, %v; want updated, true", val, ok)
	}

	// Count unchanged.
	count, _ := tree.EntryCount()
	if count != 100 {
		t.Fatalf("EntryCount = %d, want 100", count)
	}
}

func TestProllyTreeUpdateDelete(t *testing.T) {
	s := testStore(t)

	entries := make([]ProllyEntry, 100)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree := NewProllyTree(s, ProllyConfig{})
	tree.Build(entries)

	// Delete a key.
	err := tree.Update([]ProllyMutation{
		{Key: "k0042", Delete: true},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, ok := tree.Get("k0042")
	if ok {
		t.Fatal("k0042 should be deleted")
	}

	count, _ := tree.EntryCount()
	if count != 99 {
		t.Fatalf("EntryCount = %d, want 99", count)
	}
}

func TestProllyTreeUpdateMultiple(t *testing.T) {
	s := testStore(t)

	entries := make([]ProllyEntry, 200)
	for i := range entries {
		entries[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}
	tree := NewProllyTree(s, ProllyConfig{})
	tree.Build(entries)

	// Multiple mutations: insert, update, delete.
	err := tree.Update([]ProllyMutation{
		{Key: "k0010", Value: "modified"},  // update
		{Key: "k0050", Delete: true},       // delete
		{Key: "k0200", Value: "appended"},  // insert at end
		{Key: "k0000a", Value: "inserted"}, // insert in middle
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if v, ok := tree.Get("k0010"); !ok || v != "modified" {
		t.Fatalf("k0010 = %q, %v; want modified", v, ok)
	}
	if _, ok := tree.Get("k0050"); ok {
		t.Fatal("k0050 should be deleted")
	}
	if v, ok := tree.Get("k0200"); !ok || v != "appended" {
		t.Fatalf("k0200 = %q, %v; want appended", v, ok)
	}
	if v, ok := tree.Get("k0000a"); !ok || v != "inserted" {
		t.Fatalf("k0000a = %q, %v; want inserted", v, ok)
	}

	count, _ := tree.EntryCount()
	if count != 201 { // 200 - 1 delete + 2 inserts
		t.Fatalf("EntryCount = %d, want 201", count)
	}
}

func TestProllyTreeUpdateMatchesBuild(t *testing.T) {
	// Verify that building from scratch and applying updates
	// produce the same tree structure.
	s := testStore(t)

	initial := make([]ProllyEntry, 100)
	for i := range initial {
		initial[i] = ProllyEntry{Key: fmt.Sprintf("k%04d", i), Value: fmt.Sprintf("v%04d", i)}
	}

	// Path A: Build initial, then Update.
	treeA := NewProllyTree(s, ProllyConfig{})
	treeA.Build(initial)
	treeA.Update([]ProllyMutation{
		{Key: "k0010", Value: "mod"},
		{Key: "k0050", Delete: true},
		{Key: "k0100", Value: "new"},
	})

	// Path B: Build the final state directly.
	final := make([]ProllyEntry, 0, 100)
	for i := 0; i < 100; i++ {
		if i == 50 {
			continue // deleted
		}
		key := fmt.Sprintf("k%04d", i)
		val := fmt.Sprintf("v%04d", i)
		if i == 10 {
			val = "mod"
		}
		final = append(final, ProllyEntry{Key: key, Value: val})
	}
	final = append(final, ProllyEntry{Key: "k0100", Value: "new"})
	sortEntries(final)

	treeB := NewProllyTree(s, ProllyConfig{})
	treeB.Build(final)

	if treeA.RootHash() != treeB.RootHash() {
		t.Fatalf("Update path and Build path produce different roots:\n  Update: %s\n  Build:  %s",
			treeA.RootHash(), treeB.RootHash())
	}
}

func TestProllyTreeUpdateEmpty(t *testing.T) {
	s := testStore(t)

	// Update on empty tree should build.
	tree := NewProllyTree(s, ProllyConfig{})
	err := tree.Update([]ProllyMutation{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if v, ok := tree.Get("a"); !ok || v != "1" {
		t.Fatalf("a = %q, %v", v, ok)
	}
	count, _ := tree.EntryCount()
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestProllyTreeUpdateDeleteAll(t *testing.T) {
	s := testStore(t)

	tree := NewProllyTree(s, ProllyConfig{})
	tree.Build([]ProllyEntry{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
	})

	err := tree.Update([]ProllyMutation{
		{Key: "a", Delete: true},
		{Key: "b", Delete: true},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tree.RootHash() != "" {
		t.Fatal("tree should be empty after deleting all entries")
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
