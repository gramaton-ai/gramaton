package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// TestDurableSaveSmoke keeps the synced write path exercised: every
// other test in the suite runs under WithVolatileStorage, so without
// this test a regression in the fsync branches (storage blob sync,
// HEAD/ref sync, fsyncDir) would only surface in production. One
// engine, full durability, one save, reload, assert the record
// survived.
func TestDurableSaveSmoke(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	// No WithVolatileStorage: this is the point of the test.
	eng, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("durable smoke record"),
	})
	if _, err := eng.Save("durable smoke"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	eng2, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer eng2.Close()
	eng2.RLock()
	defer eng2.RUnlock()
	got, ok := eng2.Graph().GetNode(n.ID)
	if !ok {
		t.Fatalf("record %s missing after durable save + reload", n.ID)
	}
	if c, _ := got.Properties.GetString("content_full"); c != "durable smoke record" {
		t.Errorf("content_full = %q after reload", c)
	}
}

// TestVolatileStorageStaysOutOfProduction pins the test-only contract
// of WithVolatileStorage: no non-test production file may reference
// it. If this fails, someone wired the durability switch into a code
// path a user could reach -- see the option's doc comment for why
// that must not happen.
func TestVolatileStorageStaysOutOfProduction(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("repo root not found from test dir: %v", err)
	}

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "testutil" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The tripwire is a CALL, not a mention: doc comments on the
		// SetNoSync implementations legitimately point readers at
		// the option by name.
		if !strings.Contains(string(data), "WithVolatileStorage()") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// The definition site lives in core; SetNoSync
		// implementations are inert without the option.
		if rel == filepath.Join("core", "engine.go") {
			return nil
		}
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("WithVolatileStorage referenced in production files: %v\n"+
			"The durability switch is test-only. Fixtures (in _test.go files or testutil/) may use it; nothing else may.", offenders)
	}
}
