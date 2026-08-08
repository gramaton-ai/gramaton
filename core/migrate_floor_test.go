package core

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestBackfillTSIndexGroundsAtPruneFloor: a pruned store's chain is
// deliberately truncated -- the oldest kept commit still names its
// pruned parent by hash. The migrate backfill must ground its walk
// at the floor instead of failing on the absent chunk, or the next
// format bump bricks every pruned store.
func TestBackfillTSIndexGroundsAtPruneFloor(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	c1, err := eng.Save("first")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	c2, err := eng.Save("second")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v3")
	if _, err := eng.Save("third"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 3: %v", err)
	}
	eng.Unlock()

	// Simulate an older-than prune with its floor at the middle
	// commit: the first commit's chunk is gone, and the kept chain's
	// oldest commit still references it as Parent.
	eng.SetHistoryFloor(&graph.Tombstone{OldestKeptCommit: c2.Hash})
	if err := eng.Store().Delete(c1.Hash); err != nil {
		t.Fatalf("delete pruned commit chunk: %v", err)
	}

	if err := backfillTSIndex(eng); err != nil {
		t.Fatalf("backfill on a pruned store must ground at the floor, got: %v", err)
	}
	if _, ok := eng.TSIndex().CommitAt(c2.Timestamp); !ok {
		t.Fatal("kept commits missing from the timestamp index after backfill")
	}
}
