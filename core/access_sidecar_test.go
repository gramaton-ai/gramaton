package core

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestRecordAccessProducesNoCommits is the sidecar's headline pin:
// reads bump bookkeeping without dirtying the graph or minting a
// commit -- the mechanism behind million-commit histories is gone.
func TestRecordAccessProducesNoCommits(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("read me repeatedly"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	head := eng.HeadHash()

	eng.Lock()
	for range 5 {
		eng.RecordAccess(n.ID, time.Now().UTC())
	}
	eng.Unlock()

	if got := eng.HeadHash(); got != head {
		t.Fatalf("reads minted a commit: HEAD %q -> %q", head, got)
	}
	eng.RLock()
	defer eng.RUnlock()
	if eng.Graph().IsDirty() {
		t.Fatal("reads dirtied the graph; access bookkeeping must stay out of the commit substrate")
	}
	got, _ := eng.Graph().GetNode(n.ID)
	if c, _ := got.Properties.GetInt64("access_count"); c != 5 {
		t.Fatalf("access_count = %d, want 5", c)
	}
}

// TestAccessSurvivesRestart pins durability: the sidecar, not the
// (never re-committed) node blob, carries the counts across an
// engine reopen, and the load-hook overlay makes them visible on the
// re-materialized node.
func TestAccessSurvivesRestart(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("persistent bookkeeping"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	for range 3 {
		eng.RecordAccess(n.ID, time.Now().UTC())
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	eng2.RLock()
	defer eng2.RUnlock()
	loaded, ok := eng2.Graph().GetNode(n.ID)
	if !ok {
		t.Fatal("node missing after reopen")
	}
	if c, _ := loaded.Properties.GetInt64("access_count"); c != 3 {
		t.Fatalf("access_count after reopen = %d, want 3 (sidecar overlay)", c)
	}
	if _, ok := loaded.Properties.GetTimestamp("last_accessed"); !ok {
		t.Fatal("last_accessed missing after reopen")
	}
}

// TestAccessSeedsFromLegacyProps pins the migration seam: a record
// whose blob carries pre-sidecar access props (committed by the old
// versioned flow) seeds the sidecar from them on first access
// instead of restarting at zero.
func TestAccessSeedsFromLegacyProps(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("record with legacy bookkeeping"),
		"access_count": graph.Int64Property(41),
	})
	if _, err := eng.Save("seed with legacy access props"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.RecordAccess(n.ID, time.Now().UTC())
	eng.Unlock()

	eng.RLock()
	defer eng.RUnlock()
	got, _ := eng.Graph().GetNode(n.ID)
	if c, _ := got.Properties.GetInt64("access_count"); c != 42 {
		t.Fatalf("access_count = %d, want 42 (seeded from the legacy prop)", c)
	}
	if m, ok := eng.AccessIdx().Get(n.ID); !ok || m.Count != 42 {
		t.Fatalf("sidecar entry = %+v, want seeded count 42", m)
	}
}

// TestDeleteNodeCleansAccessSidecar pins deletion hygiene: a batched
// hard delete removes the record's sidecar entry after the commit.
func TestDeleteNodeCleansAccessSidecar(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("doomed but bookkept"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.RecordAccess(n.ID, time.Now().UTC())
	eng.Unlock()

	if _, ok := eng.AccessIdx().Get(n.ID); !ok {
		t.Fatal("sidecar entry missing before deletion")
	}
	err := eng.WithWriteBatch("delete", func(ws *WriteSession) (bool, error) {
		return true, ws.DeleteNode(n.ID)
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}
	if _, ok := eng.AccessIdx().Get(n.ID); ok {
		t.Fatal("sidecar entry survived the node's deletion")
	}
}

// TestChangelogRecordsLogicalVersions pins the append path: real
// content changes mint one entry per commit, bookkeeping-only
// commits (a reembed sweep's embedding writes) mint none, and
// deletion closes the record's history with an empty-hash entry.
func TestChangelogRecordsLogicalVersions(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("version one"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "version two")
	c2, err := eng.Save("revise")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	// Bookkeeping-only change: an embedding write must NOT mint a
	// logical version.
	eng.SetProp(n.ID, "embedding_model", graph.StringProperty("test-model-v2"))
	if _, err := eng.Save("reembed sweep"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 3: %v", err)
	}
	eng.Unlock()

	versions := eng.Changelog().Versions(n.ID)
	if len(versions) != 2 {
		t.Fatalf("versions = %d (%+v), want 2 (create + revise; the bookkeeping commit must not count)", len(versions), versions)
	}
	if versions[1].Commit != c2.Hash {
		t.Fatalf("second version commit = %s, want the revise commit %s", versions[1].Commit, c2.Hash)
	}
	if eng.Changelog().Marker() != eng.HeadHash() {
		t.Fatalf("marker %s != HEAD %s after saves", eng.Changelog().Marker(), eng.HeadHash())
	}

	// Deletion closes the history.
	err = eng.WithWriteBatch("delete", func(ws *WriteSession) (bool, error) {
		return true, ws.DeleteNode(n.ID)
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	versions = eng.Changelog().Versions(n.ID)
	if len(versions) != 3 || versions[2].NodeHash != "" {
		t.Fatalf("versions after delete = %+v, want a trailing empty-hash deletion entry", versions)
	}
}

// TestChangelogGapWalkRepairsDrift pins the durability contract: a
// marker left behind (simulating a crash between the HEAD write and
// the changelog append) is repaired at the next open by re-deriving
// the missing commits' entries from the chain.
// TestChangelogSkipsNeverCommittedDeletion: a node created and
// deleted within one write phase never existed in any commit, so
// the changelog must not mint a deletion-only entry for it -- a
// phantom version no reader could ever resolve to content.
func TestChangelogSkipsNeverCommittedDeletion(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("born and gone in one phase"),
	})
	if err := eng.Graph().DeleteNode(n.ID); err != nil {
		eng.Unlock()
		t.Fatalf("delete: %v", err)
	}
	if _, err := eng.Save("churn"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	if versions := eng.Changelog().Versions(n.ID); len(versions) != 0 {
		t.Fatalf("versions = %+v, want none for a never-committed node", versions)
	}
}

func TestChangelogGapWalkRepairsDrift(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	c1, err := eng.Save("create")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	c2, err := eng.Save("revise")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.Unlock()

	// Simulate the crash: rewind the marker to the first commit AND
	// delete the second commit's entries, as if its append never ran.
	// Without the retraction the walk's idempotent tail-skip would
	// pass this test vacuously -- the entries would already be there.
	if err := eng.Changelog().SetMarker(c1.Hash); err != nil {
		t.Fatalf("SetMarker: %v", err)
	}
	if err := eng.Changelog().RetractCommits(map[string]bool{c2.Hash: true}); err != nil {
		t.Fatalf("RetractCommits: %v", err)
	}
	if got := eng.Changelog().Versions(n.ID); len(got) != 1 {
		t.Fatalf("pre-repair versions = %+v, want only the first (retraction must have removed the second)", got)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	if eng2.Changelog().Marker() != eng2.HeadHash() {
		t.Fatalf("gap walk did not advance the marker to HEAD")
	}
	versions := eng2.Changelog().Versions(n.ID)
	if len(versions) != 2 {
		t.Fatalf("versions after gap walk = %+v, want both (replay must not duplicate the first)", versions)
	}
}

// TestBackfillChangelogIndexesHistory pins the offline walk: a store
// with pre-changelog history (marker wiped to simulate it) re-indexes
// every logical version, skips a bookkeeping-only commit, and a
// second run adds nothing.
func TestBackfillChangelogIndexesHistory(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	if _, err := eng.Save("revise"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.SetProp(n.ID, "embedding_model", graph.StringProperty("swept"))
	if _, err := eng.Save("reembed sweep"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 3: %v", err)
	}
	eng.Unlock()

	// Simulate a pre-changelog store: wipe coverage.
	if err := eng.Changelog().SetMarker(""); err != nil {
		t.Fatalf("SetMarker: %v", err)
	}

	indexed, err := eng.BackfillChangelog(nil)
	if err != nil {
		t.Fatalf("BackfillChangelog: %v", err)
	}
	if indexed == 0 {
		t.Fatal("backfill indexed nothing")
	}
	versions := eng.Changelog().Versions(n.ID)
	if len(versions) != 2 {
		t.Fatalf("versions = %d (%+v), want 2 (bookkeeping commit must not mint)", len(versions), versions)
	}
	if eng.Changelog().Marker() != eng.HeadHash() {
		t.Fatal("marker did not land on HEAD")
	}

	again, err := eng.BackfillChangelog(nil)
	if err != nil {
		t.Fatalf("second BackfillChangelog: %v", err)
	}
	_ = again
	if got := len(eng.Changelog().Versions(n.ID)); got != 2 {
		t.Fatalf("re-run duplicated entries: %d", got)
	}
}

// TestAccessOverlayConcurrentLoad pins the load-hook concurrency
// contract under the race detector: many readers materializing the
// same uncached record concurrently (each slow-path load runs
// overlayAccess on its own pre-publication copy, then races to the
// cache) while writers bump access metadata and commit revisions. A
// cold reopen forces the same GetNode slow path an LRU eviction
// would.
func TestAccessOverlayConcurrentLoad(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	// Seed a sidecar entry so the hook takes its overlay branch.
	eng.RecordAccess(n.ID, time.Now().UTC())
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 50 {
				eng2.RLock()
				if node, ok := eng2.Graph().GetNode(n.ID); ok {
					_, _ = node.Properties.GetInt64("access_count")
				}
				eng2.RUnlock()
			}
		}()
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 25 {
				eng2.Lock()
				eng2.RecordAccess(n.ID, time.Now().UTC())
				eng2.Unlock()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := range 10 {
			eng2.Lock()
			eng2.SetContentProp(n.ID, "content_full", fmt.Sprintf("rev %d", i))
			if _, err := eng2.Save("concurrent revise"); err != nil {
				eng2.Unlock()
				t.Errorf("Save: %v", err)
				return
			}
			eng2.Unlock()
		}
	}()
	close(start)
	wg.Wait()

	eng2.RLock()
	defer eng2.RUnlock()
	node, ok := eng2.Graph().GetNode(n.ID)
	if !ok {
		t.Fatal("record vanished under concurrent load")
	}
	if c, _ := node.Properties.GetInt64("access_count"); c < 1 {
		t.Fatalf("access_count = %d, want the bumps to have landed", c)
	}
}

// TestBackfillMergesHistoryUnderLiveCoverage pins the oldest-first
// contract when the backfill runs AFTER live coverage began (the
// advertised upgrade flow): history indexed later must merge UNDER
// the live entries, not append at the tail -- every consumer of the
// list is positional, and retention slots are counted from it.
func TestBackfillMergesHistoryUnderLiveCoverage(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	c1, err := eng.Save("create")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	if _, err := eng.Save("revise"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.Unlock()

	// Simulate coverage that began at v2: v1's entry absent, marker
	// current. The backfill must merge v1 back UNDER v2.
	if err := eng.Changelog().RetractCommits(map[string]bool{c1.Hash: true}); err != nil {
		t.Fatalf("RetractCommits: %v", err)
	}
	if _, err := eng.BackfillChangelog(nil); err != nil {
		t.Fatalf("BackfillChangelog: %v", err)
	}
	got := eng.Changelog().Versions(n.ID)
	if len(got) != 2 {
		t.Fatalf("versions = %d, want 2", len(got))
	}
	if got[0].Commit != c1.Hash {
		t.Fatalf("first entry = %s, want the OLDER commit %s (history merged at the tail)", trunc12(got[0].Commit), trunc12(c1.Hash))
	}
	if !got[0].Timestamp.Before(got[1].Timestamp) && !got[0].Timestamp.Equal(got[1].Timestamp) {
		t.Fatalf("entries out of chronological order: %v then %v", got[0].Timestamp, got[1].Timestamp)
	}
	// The marker never moved backwards past live coverage.
	if eng.Changelog().Marker() != eng.HeadHash() {
		t.Fatalf("marker = %s, want HEAD (backfill must not rewind coverage)", trunc12(eng.Changelog().Marker()))
	}
}
