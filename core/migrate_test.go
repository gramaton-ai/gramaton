package core

import (
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// migrateFixture sets up a store under cfgDir that can be opened by
// loadEngineWithOptions. Returns (cfgDir, cleanup). The caller uses
// cfgDir both to seed state (via setupAtDir + direct bolt) and to
// pass into MigrateStore. Test engines close automatically.
type migrateFixture struct {
	cfgDir  string
	dataDir string
}

// newMigrateFixture creates an isolated per-test config directory
// with a default config written out; dataDir is derived the same way
// LoadEngine derives it.
func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &migrateFixture{cfgDir: dir, dataDir: cfg.DataDir}
}

// openEngine opens the engine for this fixture. Use when the store is
// at the current version (normal boot path).
func (f *migrateFixture) openEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := LoadEngineWithOptions(f.cfgDir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	return eng
}

// addCommit lets a test drive the engine's write path to produce a
// real commit (and therefore a real tsIndex entry).
func addCommit(t *testing.T, eng *Engine, content string) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(content),
	})
	if _, err := eng.Save(content); err != nil {
		t.Fatalf("Save(%q): %v", content, err)
	}
}

// wipeTSBucket empties the commit_timestamps bucket so we can simulate
// a v1 store that never had the D7 index. Must be called with no
// engine open -- bbolt is single-writer.
func wipeTSBucket(t *testing.T, dataDir string) {
	t.Helper()
	boltPath := filepath.Join(dataDir, "indexes.db")
	db, err := bolt.Open(boltPath, 0o600, nil)
	if err != nil {
		t.Fatalf("open bolt for wipe: %v", err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("commit_timestamps")) != nil {
			if err := tx.DeleteBucket([]byte("commit_timestamps")); err != nil {
				return err
			}
		}
		_, err := tx.CreateBucket([]byte("commit_timestamps"))
		return err
	})
	if err != nil {
		t.Fatalf("wipe ts bucket: %v", err)
	}
}

// tsIndexCount opens the bolt file to count entries without requiring
// an engine (useful after Close).
func tsIndexCount(t *testing.T, dataDir string) int {
	t.Helper()
	boltPath := filepath.Join(dataDir, "indexes.db")
	db, err := bolt.Open(boltPath, 0o600, nil)
	if err != nil {
		t.Fatalf("open bolt for count: %v", err)
	}
	defer db.Close()
	var n int
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("commit_timestamps"))
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n
}

func TestMigrateStoreFreshStoreWritesCurrentVersion(t *testing.T) {
	f := newMigrateFixture(t)

	// No FORMAT file written yet; MigrateStore should treat as fresh
	// and bump straight to current.
	if err := MigrateStore(f.cfgDir, nil); err != nil {
		t.Fatalf("MigrateStore: %v", err)
	}
	v, err := ReadFormatVersion(f.dataDir)
	if err != nil {
		t.Fatalf("read FORMAT: %v", err)
	}
	if v != version.StoreFormatVersion {
		t.Errorf("got FORMAT=%d, want %d", v, version.StoreFormatVersion)
	}
}

func TestMigrateStoreAlreadyCurrentIsNoOp(t *testing.T) {
	f := newMigrateFixture(t)

	// Set FORMAT to current up-front.
	if err := WriteFormatVersion(f.dataDir); err != nil {
		t.Fatal(err)
	}
	// Any commits we lay down now will flow through the live Save hook.
	eng := f.openEngine(t)
	addCommit(t, eng, "a")
	addCommit(t, eng, "b")
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before := tsIndexCount(t, f.dataDir)
	if before != 2 {
		t.Fatalf("pre-migrate ts count = %d, want 2 (Save hook)", before)
	}

	if err := MigrateStore(f.cfgDir, nil); err != nil {
		t.Fatalf("MigrateStore: %v", err)
	}

	after := tsIndexCount(t, f.dataDir)
	if after != before {
		t.Errorf("already-current migrate changed ts count: before=%d after=%d", before, after)
	}
	v, _ := ReadFormatVersion(f.dataDir)
	if v != version.StoreFormatVersion {
		t.Errorf("FORMAT after no-op: got %d, want %d", v, version.StoreFormatVersion)
	}
}

func TestMigrateStoreBackfillsV1Store(t *testing.T) {
	f := newMigrateFixture(t)
	// Build a store with three commits through the normal path so the
	// commit chain is real.
	if err := WriteFormatVersion(f.dataDir); err != nil {
		t.Fatal(err)
	}
	eng := f.openEngine(t)
	addCommit(t, eng, "a")
	addCommit(t, eng, "b")
	addCommit(t, eng, "c")
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := tsIndexCount(t, f.dataDir); got != 3 {
		t.Fatalf("pre-wipe ts count = %d, want 3", got)
	}
	// Simulate a v1 store: empty ts bucket, FORMAT says 1.
	wipeTSBucket(t, f.dataDir)
	if got := tsIndexCount(t, f.dataDir); got != 0 {
		t.Fatalf("post-wipe ts count = %d, want 0", got)
	}
	if err := os.WriteFile(filepath.Join(f.dataDir, "FORMAT"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Verify the refuse-to-boot gate trips now.
	if _, err := LoadEngine(f.cfgDir); err == nil {
		t.Fatal("expected LoadEngine to refuse v1 store")
	}

	// Run the migration.
	if err := MigrateStore(f.cfgDir, nil); err != nil {
		t.Fatalf("MigrateStore: %v", err)
	}

	// Backfill should have restored all three commit entries.
	if got := tsIndexCount(t, f.dataDir); got != 3 {
		t.Errorf("post-migrate ts count = %d, want 3", got)
	}
	// FORMAT should now be current.
	v, _ := ReadFormatVersion(f.dataDir)
	if v != version.StoreFormatVersion {
		t.Errorf("FORMAT after migrate: got %d, want %d", v, version.StoreFormatVersion)
	}

	// And the engine now boots cleanly.
	eng2, err := LoadEngine(f.cfgDir)
	if err != nil {
		t.Fatalf("LoadEngine after migrate: %v", err)
	}
	defer eng2.Close()
}

func TestMigrateStoreIdempotent(t *testing.T) {
	f := newMigrateFixture(t)

	if err := WriteFormatVersion(f.dataDir); err != nil {
		t.Fatal(err)
	}
	eng := f.openEngine(t)
	addCommit(t, eng, "a")
	addCommit(t, eng, "b")
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	wipeTSBucket(t, f.dataDir)
	if err := os.WriteFile(filepath.Join(f.dataDir, "FORMAT"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateStore(f.cfgDir, nil); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	first := tsIndexCount(t, f.dataDir)

	if err := MigrateStore(f.cfgDir, nil); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	second := tsIndexCount(t, f.dataDir)

	if first != second {
		t.Errorf("idempotency broken: first=%d second=%d", first, second)
	}
}

func TestMigrateStoreRejectsNewerFormat(t *testing.T) {
	f := newMigrateFixture(t)
	if err := os.WriteFile(filepath.Join(f.dataDir, "FORMAT"), []byte("999"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := MigrateStore(f.cfgDir, nil)
	if err == nil {
		t.Fatal("expected error for newer-than-binary format")
	}
}
