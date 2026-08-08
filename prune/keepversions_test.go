package prune

import (
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

func newPruneTestEngine(t *testing.T) (*core.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Enabled = false
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng, dir
}

func saveVersion(t *testing.T, eng *core.Engine, id, content string) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	eng.SetContentProp(id, "content_full", content)
	if _, err := eng.Save("revise"); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestKeepVersionsPlanAndApply pins the content-depth rule end to
// end: versions beyond the kept depth are swept from the CAS, the
// current state and kept depth survive, the changelog metadata stays
// complete, and the installed tombstone explains the absence.
func TestKeepVersionsPlanAndApply(t *testing.T) {
	eng, _ := newPruneTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("version one of the pruned record"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	saveVersion(t, eng, n.ID, "version two of the pruned record")
	saveVersion(t, eng, n.ID, "version three of the pruned record")

	versions := eng.Changelog().Versions(n.ID)
	if len(versions) != 3 {
		t.Fatalf("fixture versions = %d, want 3", len(versions))
	}
	v1blob := versions[0].NodeHash

	plan, err := PlanKeepVersions(eng, 2, nil)
	if err != nil {
		t.Fatalf("PlanKeepVersions: %v", err)
	}
	if plan.BlobCount != 1 || len(plan.Records) != 1 {
		t.Fatalf("plan = %+v, want exactly v1's blob", plan)
	}
	if plan.Records[0].SweepHashes[0] != v1blob {
		t.Fatalf("candidate = %s, want v1 blob %s", plan.Records[0].SweepHashes[0], v1blob)
	}
	if !plan.Records[0].KeptFromTS.Equal(versions[1].Timestamp) {
		t.Fatalf("kept-from = %v, want v2's timestamp", plan.Records[0].KeptFromTS)
	}

	res, err := ApplyKeepVersions(eng, plan)
	if err != nil {
		t.Fatalf("ApplyKeepVersions: %v", err)
	}
	if res.SweptBlobs != 1 || res.SweepErrors != 0 {
		t.Fatalf("apply result = %+v", res)
	}
	if eng.Store().Has(v1blob) {
		t.Fatal("v1 blob survived the sweep")
	}
	// Current and kept states intact.
	eng.RLock()
	cur, ok := eng.Graph().GetNode(n.ID)
	eng.RUnlock()
	if !ok {
		t.Fatal("record lost")
	}
	if c, _ := cur.Properties.GetString("content_full"); c != "version three of the pruned record" {
		t.Fatalf("live content = %q", c)
	}
	if !eng.Store().Has(versions[1].NodeHash) {
		t.Fatal("kept v2 blob swept")
	}
	// Timeline metadata stays complete: all three versions remain as
	// entries (the prune commit carries no dirty nodes and mints none).
	if got := eng.Changelog().Versions(n.ID); len(got) != 3 {
		t.Fatalf("changelog entries = %d, want 3 (metadata retained)", len(got))
	}
	// Tombstone installed and explanatory.
	floor := eng.HistoryFloor()
	if floor == nil {
		t.Fatal("no history floor after apply")
	}
	if !floor.CoversRecordVersion(n.ID, versions[0].Timestamp) {
		t.Fatal("swept version not covered by the tombstone")
	}
	if floor.CoversRecordVersion(n.ID, versions[1].Timestamp) {
		t.Fatal("kept version wrongly covered")
	}
	// The prune commit carries the tombstone reference.
	if res.Commit.PruneTombstoneRoot != res.TombstoneRoot {
		t.Fatalf("prune commit tombstone = %q, want %q", res.Commit.PruneTombstoneRoot, res.TombstoneRoot)
	}
}

// TestKeepVersionsRefusesBehindMarker pins the miscounting guard: a
// changelog trailing HEAD refuses to plan.
func TestKeepVersionsRefusesBehindMarker(t *testing.T) {
	eng, _ := newPruneTestEngine(t)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	c1, err := eng.Save("create")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	if _, err := eng.Save("revise"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.Unlock()

	if err := eng.Changelog().SetMarker(c1.Hash); err != nil {
		t.Fatalf("SetMarker: %v", err)
	}
	if _, err := PlanKeepVersions(eng, 1, nil); err == nil {
		t.Fatal("plan succeeded with a stale changelog marker")
	}
}
