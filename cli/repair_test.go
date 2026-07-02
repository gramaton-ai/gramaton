package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// TestRepairRefusesFrozenStore pins the CLI read-only gate. Repair's
// write path is durable BEFORE Save (DeleteEdge persists straight to
// the bbolt edge store), so the engine's Save backstop alone would
// leave the mutation stuck while rejecting the commit; the command
// must refuse up front and leave the frozen store byte-identical in
// behavior.
//
// The fixture manufactures the dangling edge exactly the way a real
// one is born: node A is committed, then node B and edge A->B are
// added and the engine closes WITHOUT saving. The edge persisted to
// indexes.db immediately (bbolt edge store), B never reached a
// commit, so every reopen sees an edge to a missing node.
func TestRepairRefusesFrozenStore(t *testing.T) {
	base := newFreezeTestBase(t)
	dataDir := filepath.Join(base, "data")

	eng, err := core.LoadEngine(base, base)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	props := graph.Properties{
		"content_full":      graph.StringProperty("repair gate fixture"),
		"processing_status": graph.StringProperty("processed"),
	}
	eng.Lock()
	a := eng.Graph().AddNode(props)
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	b := eng.Graph().AddNode(props)
	if _, err := eng.Graph().AddEdge(a.ID, b.ID, "related_to", 0.5, nil); err != nil {
		eng.Unlock()
		t.Fatalf("add edge: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	// Both mutating invocations refuse with the api guards' message
	// shape and a thaw hint.
	for _, args := range [][]string{
		{"repair", "--config-dir", base},
		{"repair", "--content-quality", "--config-dir", base},
	} {
		_, err := runCmd(t, args...)
		if err == nil {
			t.Fatalf("%v on a frozen store should refuse", args)
		}
		if !strings.Contains(err.Error(), "store is read-only") || !strings.Contains(err.Error(), "gramaton store thaw") {
			t.Errorf("%v error = %q, want the read-only refusal pointing at gramaton store thaw", args, err)
		}
	}

	// The read-only dry run stays allowed, like `gramaton validate`.
	if _, err := runCmd(t, "repair", "--dry-run", "--config-dir", base); err != nil {
		t.Errorf("repair --dry-run on a frozen store should be allowed, got %v", err)
	}

	// Store untouched: the dangling edge is still in the edge store.
	// Had Repair run, DeleteEdge would have removed it durably.
	eng2, err := core.LoadEngine(base, base)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer eng2.Close()
	eng2.RLock()
	edges := eng2.Graph().EdgesFrom(a.ID)
	eng2.RUnlock()
	if len(edges) != 1 || edges[0].TargetID != b.ID {
		t.Errorf("dangling edge gone after refused repair: EdgesFrom(%s) = %+v", a.ID, edges)
	}
}

// TestRepairContentQualityFlagRegistered protects the CLI wiring
// for the --content-quality flag. If a refactor accidentally drops
// the `repairCmd.Flags().BoolVar(&repairContentQuality, ...)` line
// in init(), this test catches it. The self-heal itself is covered
// by curation/self_heal_test.go — this test only proves the flag
// exists and the boolean is wired.
func TestRepairContentQualityFlagRegistered(t *testing.T) {
	f := repairCmd.Flags().Lookup("content-quality")
	if f == nil {
		t.Fatal("content-quality flag not registered on repairCmd")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("content-quality flag type = %q, want bool", f.Value.Type())
	}
}

func TestRepairDryRunFlagStillRegistered(t *testing.T) {
	// Sanity: the --dry-run flag should remain after we added
	// --content-quality alongside it (regression test for an
	// init-function rewrite accidentally dropping the older flag).
	if repairCmd.Flags().Lookup("dry-run") == nil {
		t.Fatal("dry-run flag disappeared from repairCmd")
	}
}
