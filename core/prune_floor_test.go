package core

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestHistoryFloorLoadsAcrossReopen pins the tombstone plumbing: a
// minted tombstone chunk, pointed at from the sidecar, is picked up
// by the next engine open and answers the pruned-by-policy question.
func TestHistoryFloorLoadsAcrossReopen(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("a record with pruned history"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	if eng.HistoryFloor() != nil {
		t.Fatal("never-pruned store reports a history floor")
	}

	keptFrom := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ts := &graph.Tombstone{
		Records: map[string]graph.RecordFloor{
			n.ID: {KeptFromTS: keptFrom, SweptVersions: 3},
		},
		PrunedAt: time.Now().UTC(),
	}
	hash, err := ts.WriteChunk(eng.Store())
	if err != nil {
		t.Fatalf("tombstone save: %v", err)
	}
	if err := eng.Changelog().SetPruneTombstoneRef(hash); err != nil {
		t.Fatalf("set ref: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	floor := eng2.HistoryFloor()
	if floor == nil {
		t.Fatal("history floor not loaded at reopen")
	}
	if !floor.CoversRecordVersion(n.ID, keptFrom.Add(-time.Hour)) {
		t.Fatal("pre-watermark version not covered as pruned-by-policy")
	}
	if floor.CoversRecordVersion(n.ID, keptFrom.Add(time.Hour)) {
		t.Fatal("post-watermark version wrongly covered")
	}
	if floor.CoversRecordVersion("some-other-record", keptFrom.Add(-time.Hour)) {
		t.Fatal("untouched record wrongly covered")
	}
}

// TestTombstoneUnion pins the union-across-prunes rule: the newest
// tombstone carries the previous prune's floor and record
// watermarks, with swept counts accumulating.
func TestTombstoneUnion(t *testing.T) {
	floorDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	prev := &graph.Tombstone{
		FloorDate:        floorDate,
		OldestKeptCommit: "aaa",
		Baseline:         "bbb",
		Records: map[string]graph.RecordFloor{
			"r1": {KeptFromTS: floorDate, SweptVersions: 2},
			"r2": {KeptFromTS: floorDate, SweptVersions: 1},
		},
	}
	next := &graph.Tombstone{
		Records: map[string]graph.RecordFloor{
			"r1": {KeptFromTS: floorDate.AddDate(0, 2, 0), SweptVersions: 4},
		},
	}
	next.Union(prev)
	if next.FloorDate != floorDate || next.OldestKeptCommit != "aaa" || next.Baseline != "bbb" {
		t.Fatalf("floor not carried forward: %+v", next)
	}
	if next.Records["r2"].SweptVersions != 1 {
		t.Fatalf("untouched record's watermark lost: %+v", next.Records)
	}
	if got := next.Records["r1"]; got.SweptVersions != 6 || !got.KeptFromTS.Equal(floorDate.AddDate(0, 2, 0)) {
		t.Fatalf("merged record floor = %+v, want accumulated sweeps with the newer watermark", got)
	}
}

// TestHistoryFloorRecoversFromChain pins the sidecar-loss fallback:
// with the pointer wiped, the next open recovers the floor from the
// prune commit's tombstone reference and re-caches the pointer.
func TestHistoryFloorRecoversFromChain(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("recovery fixture"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	ts := &graph.Tombstone{
		Records: map[string]graph.RecordFloor{
			n.ID: {KeptFromTS: time.Now().UTC(), SweptVersions: 1},
		},
		PrunedAt: time.Now().UTC(),
	}
	root, err := ts.WriteChunk(eng.Store())
	if err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	eng.Lock()
	eng.SetPendingTombstoneRoot(root)
	if _, err := eng.Save("prune: content depth sweep", graph.CommitAction{Kind: graph.ActionPrune}); err != nil {
		eng.Unlock()
		t.Fatalf("prune commit: %v", err)
	}
	eng.Unlock()
	// Simulate sidecar loss: the pointer is never set (or wiped).
	if err := eng.Changelog().SetPruneTombstoneRef(""); err != nil {
		t.Fatalf("wipe pointer: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	if eng2.HistoryFloor() == nil {
		t.Fatal("floor not recovered from the commit chain")
	}
	if eng2.Changelog().PruneTombstoneRef() != root {
		t.Fatal("pointer not re-cached after recovery")
	}
}
