package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// newReadOnlyTestDir creates a temp config+data dir so the same store
// can be opened, closed, frozen offline, and reopened across engine
// instances (setupTestEngine hides the dir, so it can't be reused).
func newReadOnlyTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openReadOnlyTestEngine opens an engine against dir with the
// standard test fixtures (in-memory vec index, volatile storage) plus
// any extra options. Close is idempotent, so registering cleanup is
// safe even for engines the test closes mid-flow; LIFO ordering
// drains bbolt handles before t.TempDir's RemoveAll fires (Windows).
func openReadOnlyTestEngine(t *testing.T, dir string, extra ...EngineOption) *Engine {
	t.Helper()
	opts := append([]EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithVolatileStorage(),
	}, extra...)
	eng, err := LoadEngineWithOptions(dir, nil, opts)
	if err != nil {
		t.Fatalf("LoadEngineWithOptions: %v", err)
	}
	t.Cleanup(func() {
		if err := eng.Close(); err != nil {
			t.Logf("engine close: %v", err)
		}
	})
	return eng
}

// TestFrozenStoreLifecycle drives the full freeze/thaw cycle through
// the engine: seed a writable store, freeze it offline via
// FreezeStore, reopen and verify (a) the open succeeds with derived
// indexes rebuilt -- indexes.db is deleted first to force the
// rebuildPrimaryIfMissing write path, which must NOT be gated on
// readOnly -- (b) reads keep working, (c) every write backstop
// rejects or short-circuits, then thaw and verify writes work again.
func TestFrozenStoreLifecycle(t *testing.T) {
	dir := newReadOnlyTestDir(t)

	// Phase 1: writable store, seed one record.
	eng := openReadOnlyTestEngine(t, dir)
	if eng.ReadOnly() {
		t.Fatal("fresh store should not be read-only")
	}
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("frozen knowledge survives"),
		"temporality":  graph.StringProperty("durable"),
	})
	eng.IndexNode(n.ID, "frozen knowledge survives", nil)
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("seed save: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close writable engine: %v", err)
	}

	// Phase 2: freeze offline (no engine needed), then delete the
	// derived index cache to prove a frozen store still rebuilds
	// indexes.db at startup (rebuildPrimaryIfMissing writes it
	// directly, bypassing Save -- that must keep working).
	if err := FreezeStore(dir, "publisher@example.com"); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "indexes.db")); err != nil {
		t.Fatalf("remove indexes.db: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	if !eng2.ReadOnly() {
		t.Fatal("frozen store should open with ReadOnly() true")
	}

	// Reads keep working and the derived indexes were rebuilt.
	eng2.RLock()
	_, ok := eng2.Graph().GetNode(n.ID)
	ids := eng2.PropIdx().Lookup("temporality", graph.StringProperty("durable"))
	bm25Hits := eng2.BM25Full().Search([]string{"frozen"}, 10, nil)
	eng2.RUnlock()
	if !ok {
		t.Fatal("existing record should be readable on a frozen store")
	}
	if len(ids) != 1 || ids[0] != n.ID {
		t.Fatalf("property index not rebuilt on frozen store: %v", ids)
	}
	if len(bm25Hits) != 1 || bm25Hits[0].NodeID != n.ID {
		t.Fatalf("BM25 index not rebuilt on frozen store: %v", bm25Hits)
	}

	// Save rejects before any work.
	eng2.Lock()
	_, saveErr := eng2.Save("must fail")
	eng2.Unlock()
	if !errors.Is(saveErr, ErrStoreReadOnly) {
		t.Fatalf("Save on frozen store: got %v, want ErrStoreReadOnly", saveErr)
	}

	// WithWriteBatch rejects at entry; fn must never run.
	ran := false
	wbErr := eng2.WithWriteBatch("must fail", func(*WriteSession) (bool, error) {
		ran = true
		return true, nil
	})
	if !errors.Is(wbErr, ErrStoreReadOnly) {
		t.Fatalf("WithWriteBatch on frozen store: got %v, want ErrStoreReadOnly", wbErr)
	}
	if ran {
		t.Fatal("WithWriteBatch fn ran on a frozen store")
	}

	// SaveOrLog and FlushAccess short-circuit quietly: no commit
	// lands, observed via a stable HEAD.
	head := eng2.HeadHash()
	if head == "" {
		t.Fatal("frozen store should have the seed commit as HEAD")
	}
	eng2.Lock()
	eng2.SaveOrLog("background save on frozen store")
	eng2.MarkAccessDirty()
	eng2.Unlock()
	eng2.FlushAccess()
	if got := eng2.HeadHash(); got != head {
		t.Fatalf("background write path committed on a frozen store: HEAD %q -> %q", head, got)
	}
	if err := eng2.Close(); err != nil {
		t.Fatalf("close frozen engine: %v", err)
	}

	// Phase 3: thaw, reopen, writable again.
	if err := ThawStore(dir); err != nil {
		t.Fatalf("ThawStore: %v", err)
	}
	eng3 := openReadOnlyTestEngine(t, dir)
	if eng3.ReadOnly() {
		t.Fatal("thawed store should open writable")
	}
	eng3.Lock()
	eng3.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("post-thaw write"),
	})
	_, thawSaveErr := eng3.Save("post-thaw")
	eng3.Unlock()
	if thawSaveErr != nil {
		t.Fatalf("Save after thaw: %v", thawSaveErr)
	}
}

// TestWithReadOnlyOptionForcesReadOnly verifies the option forces
// read-only on a manifest-writable store without touching the
// manifest on disk (a runtime stance, not a freeze).
func TestWithReadOnlyOptionForcesReadOnly(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir, WithReadOnly())
	if !eng.ReadOnly() {
		t.Fatal("WithReadOnly should force ReadOnly() true on a writable store")
	}

	eng.Lock()
	_, err := eng.Save("must fail")
	eng.Unlock()
	if !errors.Is(err, ErrStoreReadOnly) {
		t.Fatalf("Save under WithReadOnly: got %v, want ErrStoreReadOnly", err)
	}

	m, mErr := ReadStoreManifest(dir)
	if mErr != nil {
		t.Fatalf("ReadStoreManifest: %v", mErr)
	}
	if m.ReadOnly {
		t.Fatal("WithReadOnly must not write the on-disk manifest")
	}
}

// TestWithReadOnlyOptionOnFrozenManifest verifies the combination:
// a manifest-frozen store opened with WithReadOnly stays read-only.
// The option can only force read-only, never unfreeze -- there is no
// option shape that clears the flag, and the manifest is read first.
func TestWithReadOnlyOptionOnFrozenManifest(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	if err := FreezeStore(dir, ""); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	eng := openReadOnlyTestEngine(t, dir, WithReadOnly())
	if !eng.ReadOnly() {
		t.Fatal("frozen manifest combined with WithReadOnly should stay read-only")
	}
}

// TestJobSweeperNotStartedOnReadOnlyEngine pins the background-writer
// gate in openFiles: a read-only engine spawns no jobs GC sweeper.
// jobs.db is a derived cache (open-time recovery still writes it),
// but a frozen store must run no PERIODIC background writer of any
// kind -- and no new write jobs can be created on it anyway. The
// writable control keeps the assertion from passing vacuously.
func TestJobSweeperNotStartedOnReadOnlyEngine(t *testing.T) {
	frozenDir := newReadOnlyTestDir(t)
	if err := FreezeStore(frozenDir, ""); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	frozen := openReadOnlyTestEngine(t, frozenDir)
	if !frozen.ReadOnly() {
		t.Fatal("test precondition: engine should be read-only")
	}
	if frozen.cfg.Jobs.SweepInterval <= 0 {
		t.Fatalf("test precondition: default SweepInterval must be > 0, got %v", frozen.cfg.Jobs.SweepInterval)
	}
	if frozen.jobSweepCancel != nil || frozen.jobSweepDone != nil {
		t.Error("jobs GC sweeper started on a read-only engine")
	}

	writableDir := newReadOnlyTestDir(t)
	writable := openReadOnlyTestEngine(t, writableDir)
	if writable.jobSweepCancel == nil || writable.jobSweepDone == nil {
		t.Error("writable engine with SweepInterval > 0 should start the jobs GC sweeper")
	}
}

// TestEngineOpenFailsOnCorruptManifest verifies openFiles aborts on a
// manifest read error, consistent with format-check failure handling:
// a corrupted manifest on a store that might be frozen must not
// silently open writable.
func TestEngineOpenFailsOnCorruptManifest(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "STORE"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithVolatileStorage(),
	})
	if err == nil {
		t.Fatal("engine open should fail loud on a corrupted STORE manifest")
	}
	if !strings.Contains(err.Error(), "store manifest") {
		t.Fatalf("error should surface the manifest context, got: %v", err)
	}
}
