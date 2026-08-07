package core

import (
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
