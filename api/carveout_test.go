package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// carveVec builds a fixed dimension-4 vector varied by seed so distinct
// records get distinct geometry.
func carveVec(seed float32) []float32 { return []float32{seed, 0.1, 0.2, 0.3} }

func carveMemProps(content string) graph.Properties {
	return graph.Properties{
		"knowledge_type":    graph.StringProperty("semantic"),
		"content_full":      graph.StringProperty(content),
		"temporality":       graph.StringProperty("durable"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"context_about":     graph.StringProperty("carve-test"),
		"author":            graph.StringProperty("Author One <a1@example.com>"),
	}
}

func carveChunkProps(content string) graph.Properties {
	return graph.Properties{
		"knowledge_type":    graph.StringProperty("semantic"),
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	}
}

func carveCollProps(name string) graph.Properties {
	return graph.Properties{
		"knowledge_type":    graph.StringProperty("collection"),
		"collection_name":   graph.StringProperty(name),
		"collection_schema": graph.StringProperty(`{"fields":[{"name":"title","type":"string"}]}`),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	}
}

// buildCarveSource constructs a writable source engine + API with a
// deterministic graph:
//
//	rec-head   --supersedes--> rec-pred        (a supersedes chain)
//	rec-head   --related_to--> rec-outside     (edge to a NON-selected node)
//	chunk-1    --chunk_of----> rec-parent       (a record with a chunk)
//	mem-1      --member_of---> coll-1           (collection member, also
//	mem-2      --member_of---> coll-1            selected explicitly)
//
// All records except coll-1 carry a dim-4 embedding_full vector.
func buildCarveSource(t *testing.T) (*API, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.Embedding.Dimension = 4
	cfg.LLM.Provider = ""
	cfg.Author = config.AuthorConfig{Name: "Source Owner", Email: "owner@example.com"}
	cfg.Backup.Dir = t.TempDir() + "/backups"
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngineWithOptions: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	writeErr := eng.WithWriteBatch("seed", func(ws *core.WriteSession) (bool, error) {
		g := ws.Graph()
		addRec := func(id, content string, seed float32) {
			g.AddNodeWithID(id, carveMemProps(content))
			ws.IndexNode(id, content, carveVec(seed))
		}
		addRec("rec-head", "head record content", 0.9)
		addRec("rec-pred", "predecessor record content", 0.8)
		addRec("rec-parent", "parent record with a chunk", 0.7)
		addRec("rec-outside", "outside record content", 0.5)
		addRec("mem-1", "member one content", 0.4)
		addRec("mem-2", "member two content", 0.3)

		g.AddNodeWithID("chunk-1", carveChunkProps("chunk one text"))
		ws.IndexNode("chunk-1", "chunk one text", carveVec(0.6))

		g.AddNodeWithID("coll-1", carveCollProps("Test Collection"))
		ws.IndexNode("coll-1", "Test Collection", nil)

		mustEdge := func(src, dst, typ string, w float64) {
			if _, err := ws.AddEdge(src, dst, typ, w, nil); err != nil {
				t.Fatalf("AddEdge %s->%s (%s): %v", src, dst, typ, err)
			}
		}
		mustEdge("chunk-1", "rec-parent", "chunk_of", 1.0)
		mustEdge("rec-head", "rec-pred", "supersedes", 1.0)
		mustEdge("rec-head", "rec-outside", "related_to", 0.5)
		mustEdge("mem-1", "coll-1", "member_of", 1.0)
		mustEdge("mem-2", "coll-1", "member_of", 1.0)

		ws.AddAction(graph.CommitAction{Kind: graph.ActionSave})
		return true, nil
	})
	if writeErr != nil {
		t.Fatalf("seed WithWriteBatch: %v", writeErr)
	}

	a := New(Dependencies{
		Engine:    eng,
		Log:       slog.Default(),
		ConfigDir: dir,
		StoreName: "source-store",
	})
	t.Cleanup(a.StopPreparedSweeper)
	return a, eng
}

// openStore opens an already-materialized store at its home dir for
// read-side assertions and closes it on cleanup.
func openStore(t *testing.T, home string) *core.Engine {
	t.Helper()
	eng, err := core.LoadEngine(home)
	if err != nil {
		t.Fatalf("open dest store %s: %v", home, err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func hasEdge(t *testing.T, eng *core.Engine, src, dst, typ string) bool {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesFrom(src) {
		if e.TargetID == dst && e.Type == typ {
			return true
		}
	}
	return false
}

func nodeExists(t *testing.T, eng *core.Engine, id string) bool {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	_, ok := eng.Graph().GetNode(id)
	return ok
}

// TestCarveOutFaithfulCopy is the end-to-end integration test: it
// selects a subset of the source (ids + a collection), runs a
// read-only carve, and asserts the destination is a faithful,
// ULID-preserving copy with the correct closure and dangling report.
func TestCarveOutFaithfulCopy(t *testing.T) {
	a, _ := buildCarveSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "carved")
	destData := filepath.Join(destHome, "data")

	resp, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs:         []string{"rec-head", "rec-parent", "mem-1"},
		Collections: []string{"Test Collection"},
		DestName:    "carved",
		DestDataDir: destData,
		ReadOnly:    true,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}

	// Report: closure pulled rec-pred (supersedes) and chunk-1 (chunk_of)
	// on top of the 5 resolved seeds = 7 nodes; 4 interior edges; the
	// related_to edge to the non-selected rec-outside was dropped.
	if resp.NodeCount != 7 {
		t.Errorf("NodeCount = %d, want 7", resp.NodeCount)
	}
	if resp.SeedCount != 5 {
		t.Errorf("SeedCount = %d, want 5", resp.SeedCount)
	}
	if resp.InteriorEdges != 4 {
		t.Errorf("InteriorEdges = %d, want 4", resp.InteriorEdges)
	}
	if resp.DroppedTotal != 1 || resp.DroppedByType["related_to"] != 1 {
		t.Errorf("dangling report = total %d %v, want total 1 {related_to:1}", resp.DroppedTotal, resp.DroppedByType)
	}
	if resp.EmbeddingDim != 4 {
		t.Errorf("EmbeddingDim = %d, want 4", resp.EmbeddingDim)
	}
	if !resp.ReadOnly {
		t.Error("ReadOnly should be true in the response")
	}

	// --- Destination assertions ---
	dest := openStore(t, destHome)

	// IDs preserved: every closed-selection node present, rec-outside not.
	for _, id := range []string{"rec-head", "rec-pred", "rec-parent", "chunk-1", "coll-1", "mem-1", "mem-2"} {
		if !nodeExists(t, dest, id) {
			t.Errorf("dest missing node %q", id)
		}
	}
	if nodeExists(t, dest, "rec-outside") {
		t.Error("dest should NOT contain the non-selected rec-outside")
	}

	dest.RLock()
	head, _ := dest.Graph().GetNode("rec-head")
	coll, _ := dest.Graph().GetNode("coll-1")
	dest.RUnlock()

	// embedding_full carried (faithful copy, not the lossy export path).
	if _, ok := head.Properties.GetVector("embedding_full"); !ok {
		t.Error("rec-head lost embedding_full in the carve")
	}
	// origin_store stamped.
	if got, _ := head.Properties.GetString("origin_store"); got != "source-store" {
		t.Errorf("origin_store = %q, want source-store", got)
	}
	// Full property set preserved (not the safePropTypes allowlist).
	if got, _ := head.Properties.GetString("context_about"); got != "carve-test" {
		t.Errorf("context_about = %q, want carve-test", got)
	}
	if got, _ := head.Properties.GetString("author"); got != "Author One <a1@example.com>" {
		t.Errorf("author = %q, want Author One <a1@example.com>", got)
	}
	// Collection node + schema present.
	if kt, _ := coll.Properties.GetString("knowledge_type"); kt != "collection" {
		t.Errorf("coll-1 knowledge_type = %q, want collection", kt)
	}
	if _, ok := coll.Properties.GetString("collection_schema"); !ok {
		t.Error("coll-1 lost collection_schema in the carve")
	}

	// Vector index rebuilt from the copied embedding_full (search works
	// with zero re-embedding): 6 records carried vectors.
	dest.RLock()
	vecLen := dest.VecIdx().Len()
	dest.RUnlock()
	if vecLen == 0 {
		t.Error("dest vector index is empty; vector search would not work")
	}
	t.Logf("dest vector index length = %d", vecLen)

	// Interior edges preserved (structural edges are NOT dropped, unlike
	// the export path).
	if !hasEdge(t, dest, "chunk-1", "rec-parent", "chunk_of") {
		t.Error("dest missing chunk_of edge chunk-1 -> rec-parent")
	}
	if !hasEdge(t, dest, "rec-head", "rec-pred", "supersedes") {
		t.Error("dest missing supersedes edge rec-head -> rec-pred")
	}
	if !hasEdge(t, dest, "mem-1", "coll-1", "member_of") {
		t.Error("dest missing member_of edge mem-1 -> coll-1")
	}
	if !hasEdge(t, dest, "mem-2", "coll-1", "member_of") {
		t.Error("dest missing member_of edge mem-2 -> coll-1")
	}
	// The boundary-crossing edge must NOT have been created.
	if hasEdge(t, dest, "rec-head", "rec-outside", "related_to") {
		t.Error("dest should not carry the dangling related_to edge")
	}

	// ReadOnly=true froze the destination STORE manifest as the last step.
	if !dest.ReadOnly() {
		t.Error("dest engine should report read-only after a frozen carve")
	}
	m, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("dest STORE manifest should be frozen")
	}
	if m.Owner != "Source Owner <owner@example.com>" {
		t.Errorf("frozen owner = %q, want the source author identity", m.Owner)
	}
}

// TestCarveOutDryRun asserts a dry run resolves + reports the same
// selection but creates nothing.
func TestCarveOutDryRun(t *testing.T) {
	a, _ := buildCarveSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "dry")
	destData := filepath.Join(destHome, "data")

	resp, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs:         []string{"rec-head", "rec-parent", "mem-1"},
		Collections: []string{"Test Collection"},
		DestDataDir: destData,
		ReadOnly:    true,
		DryRun:      true,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut dry run: %v", apiErr)
	}
	if !resp.DryRun {
		t.Error("resp.DryRun should be true")
	}
	if resp.NodeCount != 7 || resp.InteriorEdges != 4 || resp.DroppedByType["related_to"] != 1 {
		t.Errorf("dry-run report wrong: nodes=%d edges=%d dropped=%v", resp.NodeCount, resp.InteriorEdges, resp.DroppedByType)
	}
	if resp.ReadOnly {
		t.Error("dry run should report ReadOnly=false (nothing was frozen)")
	}
	if resp.DestDataDir != "" {
		t.Errorf("dry run should not report a DestDataDir, got %q", resp.DestDataDir)
	}
	// Nothing created on disk.
	if _, err := os.Stat(destHome); !os.IsNotExist(err) {
		t.Errorf("dry run created %s (err=%v); it must write nothing", destHome, err)
	}
}

// TestCarveOutHeadsOnly asserts HeadsOnly skips the supersedes closure,
// leaving the predecessor out (and its edge in the dangling report).
func TestCarveOutHeadsOnly(t *testing.T) {
	a, _ := buildCarveSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "heads")
	destData := filepath.Join(destHome, "data")

	resp, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs:         []string{"rec-head", "rec-parent", "mem-1"},
		Collections: []string{"Test Collection"},
		DestDataDir: destData,
		HeadsOnly:   true,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut heads-only: %v", apiErr)
	}
	if resp.NodeCount != 6 {
		t.Errorf("NodeCount = %d, want 6 (no predecessor)", resp.NodeCount)
	}
	// The supersedes edge now crosses the boundary (rec-pred excluded).
	if resp.DroppedByType["supersedes"] != 1 || resp.DroppedByType["related_to"] != 1 {
		t.Errorf("dangling report = %v, want {supersedes:1, related_to:1}", resp.DroppedByType)
	}

	dest := openStore(t, destHome)
	if nodeExists(t, dest, "rec-pred") {
		t.Error("heads-only carve must NOT pull the superseded predecessor")
	}
	if nodeExists(t, dest, "rec-head") == false {
		t.Error("heads-only carve should still contain the head record")
	}
	if dest.ReadOnly() {
		t.Error("non-frozen carve should be writable")
	}
}

// TestCarveOutRequiresSeed rejects a request with no seeds.
func TestCarveOutRequiresSeed(t *testing.T) {
	a, _ := buildCarveSource(t)
	_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		DestDataDir: filepath.Join(t.TempDir(), "x", "data"),
	})
	if apiErr == nil || apiErr.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %v", apiErr)
	}
}

// TestCarveOutUnknownID rejects an explicit id that does not exist.
func TestCarveOutUnknownID(t *testing.T) {
	a, _ := buildCarveSource(t)
	_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"does-not-exist"},
		DestDataDir: filepath.Join(t.TempDir(), "x", "data"),
	})
	if apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
}

// TestCarveOutRejectsMixedEmbeddingDims proves the dimension guard: a
// source selection carrying two different embedding dimensions is
// refused rather than silently truncated.
func TestCarveOutRejectsMixedEmbeddingDims(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngineWithOptions: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	// Set embedding_full directly as a property (bypassing the vec index)
	// so the two records carry genuinely different vector lengths.
	writeErr := eng.WithWriteBatch("seed-mixed", func(ws *core.WriteSession) (bool, error) {
		g := ws.Graph()
		p4 := carveMemProps("dim four record")
		p4["embedding_full"] = graph.VectorProperty([]float32{0.1, 0.2, 0.3, 0.4})
		g.AddNodeWithID("rec-4", p4)
		ws.IndexNode("rec-4", "dim four record", nil)

		p8 := carveMemProps("dim eight record")
		p8["embedding_full"] = graph.VectorProperty([]float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8})
		g.AddNodeWithID("rec-8", p8)
		ws.IndexNode("rec-8", "dim eight record", nil)
		ws.AddAction(graph.CommitAction{Kind: graph.ActionSave})
		return true, nil
	})
	if writeErr != nil {
		t.Fatalf("seed WithWriteBatch: %v", writeErr)
	}

	a := New(Dependencies{Engine: eng, Log: slog.Default(), ConfigDir: dir})
	t.Cleanup(a.StopPreparedSweeper)

	_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"rec-4", "rec-8"},
		DestDataDir: filepath.Join(t.TempDir(), "mixed", "data"),
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for mixed embedding dims, got %v", apiErr)
	}
}
