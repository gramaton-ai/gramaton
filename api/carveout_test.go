package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// carveSourceSecret is an inline embedding API key planted on the source
// fixture. The carve must INHERIT the source's embedder config (so the
// destination stays semantically searchable) but STRIP this secret from
// the destination config.yaml, which is a shareable local artifact.
const carveSourceSecret = "carve-src-secret-DO-NOT-LEAK-abc123"

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
	// A non-empty API-embedder provider WITH an inline secret. openai's
	// client constructs lazily (no network call, no model load at open),
	// so the fixture stays fast while proving the carve inherits a real
	// embedder config and strips the secret. Vectors are supplied verbatim
	// via IndexNode below, so the embedder is never actually invoked.
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.Model = "text-embedding-3-small"
	cfg.Embedding.APIKey = carveSourceSecret
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
	// with zero re-embedding). Six of the seven carried nodes have a
	// vector (coll-1 does not), so the dest vector index must hold exactly
	// six entries -- an exact count, not just "non-empty".
	dest.RLock()
	vecLen := dest.VecIdx().Len()
	// A real nearest-neighbor query against the DESTINATION, using
	// rec-head's own stored geometry as the probe (no query embedding
	// needed). If the copied vectors are faithful, rec-head is its own
	// nearest neighbor.
	nn := dest.VecIdx().Search(carveVec(0.9), 3, nil)
	dest.RUnlock()
	if vecLen != 6 {
		t.Errorf("dest vector index length = %d, want 6 (vectored records copied)", vecLen)
	}
	if len(nn) == 0 || nn[0].NodeID != "rec-head" {
		t.Errorf("dest vector NN for rec-head's vector = %v, want rec-head first", nn)
	}

	// FIX 1: the destination INHERITS the source's embedder (it is not
	// disabled), so the carved store is semantically searchable. Prove the
	// inherited provider is non-empty and the pinned dimension matches the
	// copied-vector dimension.
	destEmb := dest.Config().Embedding
	if destEmb.Provider == "" {
		t.Error("dest embedding provider is empty; the embedder was disabled, not inherited (FIX 1 regressed)")
	}
	if destEmb.Provider != "openai" {
		t.Errorf("dest embedding provider = %q, want the inherited source provider openai", destEmb.Provider)
	}
	if destEmb.Dimension != resp.EmbeddingDim {
		t.Errorf("dest embedding dimension = %d, want copied-vector dimension %d", destEmb.Dimension, resp.EmbeddingDim)
	}

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

// newCarveStore builds a writable source engine + API seeded by seedFn.
// The embedding block uses the given provider/apiKey (pass "" for a
// disabled embedder); dimension is pinned to 4 and vectors are supplied
// verbatim via IndexNode inside seedFn, so the embedder is never actually
// invoked. Returns the API and the source's config-dir/home (config.yaml
// and data both live there) for read-side assertions.
func newCarveStore(t *testing.T, provider, apiKey string, seedFn func(ws *core.WriteSession)) (*API, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = provider
	if provider == "openai" {
		cfg.Embedding.Model = "text-embedding-3-small"
	}
	cfg.Embedding.APIKey = apiKey
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
		seedFn(ws)
		ws.AddAction(graph.CommitAction{Kind: graph.ActionSave})
		return true, nil
	})
	if writeErr != nil {
		t.Fatalf("seed WithWriteBatch: %v", writeErr)
	}

	a := New(Dependencies{Engine: eng, Log: slog.Default(), ConfigDir: dir, StoreName: "source-store"})
	t.Cleanup(a.StopPreparedSweeper)
	return a, dir
}

// carveSegProps shapes a Session SEGMENT node the way api/sessions.go
// does: knowledge_type "segment", with content. content_keywords is
// added by callers that need the segment to match a keyword query.
func carveSegProps(content string) graph.Properties {
	return graph.Properties{
		"knowledge_type": graph.StringProperty("segment"),
		"content":        graph.StringProperty(content),
		"created_at":     graph.TimestampProperty(time.Now().UTC()),
	}
}

// containsDangling reports whether the sample holds the exact
// {source, target, type} boundary edge.
func containsDangling(sample []CarveDangling, src, dst, typ string) bool {
	for _, d := range sample {
		if d.SourceID == src && d.TargetID == dst && d.Type == typ {
			return true
		}
	}
	return false
}

// TestCarveOutInheritsEmbedderStripsSecret proves FIX 1's two halves at
// the on-disk config level: the destination INHERITS the source's
// embedding provider (so the carve stays semantically searchable) but the
// inline API key is STRIPPED (the dest config.yaml is a shareable
// artifact and must never carry a credential).
func TestCarveOutInheritsEmbedderStripsSecret(t *testing.T) {
	a, srcDir := newCarveStore(t, "openai", carveSourceSecret, func(ws *core.WriteSession) {
		ws.Graph().AddNodeWithID("rec-1", carveMemProps("secret carve record"))
		ws.IndexNode("rec-1", "secret carve record", carveVec(0.9))
	})

	// Sanity: the SOURCE config really contains the secret, else the
	// destination-absence assertion below would be vacuous.
	srcCfg, err := os.ReadFile(filepath.Join(srcDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read source config: %v", err)
	}
	if !strings.Contains(string(srcCfg), carveSourceSecret) {
		t.Fatalf("source config.yaml lacks the planted secret; test would be vacuous")
	}

	destHome := filepath.Join(t.TempDir(), "carved")
	destData := filepath.Join(destHome, "data")
	_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"rec-1"},
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}

	destCfg, err := os.ReadFile(filepath.Join(destHome, "config.yaml"))
	if err != nil {
		t.Fatalf("read dest config: %v", err)
	}
	if strings.Contains(string(destCfg), carveSourceSecret) {
		t.Error("dest config.yaml leaked the source embedding API key; StripAPIKeys was not applied")
	}
	// The embedder config itself IS inherited: the provider survives the
	// strip (it's on the allowlist), so the dest opens with a working
	// query-time embedder.
	if !strings.Contains(string(destCfg), "openai") {
		t.Error("dest config.yaml lost the inherited embedding provider")
	}
}

// TestCarveOutQuerySeedExcludesSessions covers the QUERY seed path and
// the memory-only guarantee: a keyword query resolves against a fixture
// holding BOTH a memory record and a session SEGMENT that match the same
// keyword. The memory record is carved; the segment is not. This fails if
// carveResolveQuery's Store="memory" were relaxed to Store="" (which
// would let the segment seed the carve).
func TestCarveOutQuerySeedExcludesSessions(t *testing.T) {
	const kw = "carvequerykw"
	a, _ := newCarveStore(t, "", "", func(ws *core.WriteSession) {
		g := ws.Graph()

		mem := carveMemProps("shared " + kw + " content")
		mem["content_keywords"] = graph.StringListProperty([]string{kw})
		g.AddNodeWithID("qmem", mem)
		ws.IndexNode("qmem", "shared "+kw+" content", carveVec(0.9))

		// A session segment carrying the SAME keyword. With Store="memory"
		// it is filtered out of the query resolution; with Store="" it
		// would match and seed the carve.
		seg := carveSegProps("shared " + kw + " content")
		seg["content_keywords"] = graph.StringListProperty([]string{kw})
		g.AddNodeWithID("qseg", seg)
		ws.IndexNode("qseg", "shared "+kw+" content", nil)
	})

	destHome := filepath.Join(t.TempDir(), "q")
	destData := filepath.Join(destHome, "data")
	resp, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		Keywords:    []string{kw},
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	if resp.SeedCount != 1 || resp.NodeCount != 1 {
		t.Fatalf("seed/node count = %d/%d, want 1/1 (memory record only)", resp.SeedCount, resp.NodeCount)
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "qmem") {
		t.Error("dest should contain the query-matched memory record qmem")
	}
	if nodeExists(t, dest, "qseg") {
		t.Error("dest must NOT contain the session segment qseg (sessions never seed a carve)")
	}
}

// TestCarveOutClosureExcludesSessionStructure proves the closure never
// follows session edges. The selected memory record carries OUTBOUND
// extracted_as / segment_of / topic_of edges to session-side nodes --
// directions chosen so they land in carveClosure's EdgesFrom switch,
// which is exactly what the test guards. Selecting the record pulls in the
// record alone; every session-side node stays out. This fails if any
// session edge type were added to carveClosure's switch.
func TestCarveOutClosureExcludesSessionStructure(t *testing.T) {
	a, _ := newCarveStore(t, "", "", func(ws *core.WriteSession) {
		g := ws.Graph()
		g.AddNodeWithID("smem", carveMemProps("promoted memory record"))
		ws.IndexNode("smem", "promoted memory record", carveVec(0.9))

		g.AddNodeWithID("s-seg", carveSegProps("segment text"))
		ws.IndexNode("s-seg", "segment text", nil)
		g.AddNodeWithID("s-topic", graph.Properties{
			"knowledge_type": graph.StringProperty("topic"),
			"topic_name":     graph.StringProperty("A Topic"),
			"created_at":     graph.TimestampProperty(time.Now().UTC()),
		})
		ws.IndexNode("s-topic", "", nil)
		g.AddNodeWithID("s-session", graph.Properties{
			"knowledge_type": graph.StringProperty("session"),
			"created_at":     graph.TimestampProperty(time.Now().UTC()),
		})
		ws.IndexNode("s-session", "", nil)

		mustSeedEdge := func(src, dst, typ string) {
			if _, err := ws.AddEdge(src, dst, typ, 1.0, nil); err != nil {
				t.Fatalf("AddEdge %s->%s (%s): %v", src, dst, typ, err)
			}
		}
		mustSeedEdge("smem", "s-seg", "extracted_as")
		mustSeedEdge("smem", "s-topic", "segment_of")
		mustSeedEdge("smem", "s-session", "topic_of")
	})

	destHome := filepath.Join(t.TempDir(), "sess")
	destData := filepath.Join(destHome, "data")
	resp, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"smem"},
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	if resp.NodeCount != 1 {
		t.Errorf("NodeCount = %d, want 1 (session structure must not be pulled)", resp.NodeCount)
	}
	// All three session edges cross the boundary and are dropped.
	if resp.DroppedTotal != 3 {
		t.Errorf("DroppedTotal = %d, want 3 (the three session edges)", resp.DroppedTotal)
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "smem") {
		t.Error("dest should contain the selected memory record")
	}
	for _, id := range []string{"s-seg", "s-topic", "s-session"} {
		if nodeExists(t, dest, id) {
			t.Errorf("dest must NOT contain session-side node %q (closure followed a session edge)", id)
		}
	}
}

// TestCarveOutDanglingSampleContents asserts the DanglingSample carries
// the exact boundary edge, not merely a correct count.
func TestCarveOutDanglingSampleContents(t *testing.T) {
	a, _ := buildCarveSource(t)
	resp, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"rec-head"},
		DestDataDir: filepath.Join(t.TempDir(), "d", "data"),
		DryRun:      true,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	// rec-head --related_to--> rec-outside crosses the boundary.
	if !containsDangling(resp.DanglingSample, "rec-head", "rec-outside", "related_to") {
		t.Errorf("DanglingSample = %v, want it to contain {rec-head, rec-outside, related_to}", resp.DanglingSample)
	}
}

// TestCarveOutDanglingSampleCap seeds a hub with more boundary-crossing
// edges than the sample cap and asserts the sample is capped at 20 while
// the total count is unbounded.
func TestCarveOutDanglingSampleCap(t *testing.T) {
	const outside = 25
	a, _ := newCarveStore(t, "", "", func(ws *core.WriteSession) {
		g := ws.Graph()
		g.AddNodeWithID("hub", carveMemProps("hub record"))
		ws.IndexNode("hub", "hub record", carveVec(0.9))
		for i := 0; i < outside; i++ {
			id := "out-" + string(rune('a'+i))
			g.AddNodeWithID(id, carveMemProps("outside record"))
			ws.IndexNode(id, "outside record", carveVec(0.5))
			if _, err := ws.AddEdge("hub", id, "related_to", 0.5, nil); err != nil {
				t.Fatalf("AddEdge hub->%s: %v", id, err)
			}
		}
	})

	resp, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"hub"},
		DestDataDir: filepath.Join(t.TempDir(), "cap", "data"),
		DryRun:      true,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	if resp.DroppedTotal != outside {
		t.Errorf("DroppedTotal = %d, want %d", resp.DroppedTotal, outside)
	}
	if len(resp.DanglingSample) != carveDanglingSampleCap {
		t.Errorf("len(DanglingSample) = %d, want %d (capped)", len(resp.DanglingSample), carveDanglingSampleCap)
	}
	if resp.DroppedTotal <= len(resp.DanglingSample) {
		t.Errorf("expected DroppedTotal (%d) > sample size (%d)", resp.DroppedTotal, len(resp.DanglingSample))
	}
}

// TestCarveOutPartialCollectionMember seeds a SINGLE collection member by
// id (not the whole collection). The collection node is pulled in via the
// member's outbound member_of closure; the sibling member is NOT. The
// sibling's member_of edge is NOT reported as dangling: dangling is
// defined as "an edge from a SELECTED node to a non-selected node", and
// the sibling is never selected, so its edges are never walked. This
// asymmetry is intentional -- a member brings its collection, but a
// collection does not drag in its other members.
func TestCarveOutPartialCollectionMember(t *testing.T) {
	a, _ := newCarveStore(t, "", "", func(ws *core.WriteSession) {
		g := ws.Graph()
		g.AddNodeWithID("pcoll", carveCollProps("Partial Collection"))
		ws.IndexNode("pcoll", "Partial Collection", nil)
		g.AddNodeWithID("pm-1", carveMemProps("member one"))
		ws.IndexNode("pm-1", "member one", carveVec(0.9))
		g.AddNodeWithID("pm-2", carveMemProps("member two"))
		ws.IndexNode("pm-2", "member two", carveVec(0.8))
		mustSeedEdge := func(src, dst string) {
			if _, err := ws.AddEdge(src, dst, "member_of", 1.0, nil); err != nil {
				t.Fatalf("AddEdge %s->%s: %v", src, dst, err)
			}
		}
		mustSeedEdge("pm-1", "pcoll")
		mustSeedEdge("pm-2", "pcoll")
	})

	destHome := filepath.Join(t.TempDir(), "partial")
	destData := filepath.Join(destHome, "data")
	resp, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
		IDs:         []string{"pm-1"},
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	// pm-1 + pcoll (via member_of closure) = 2 nodes; the pm-1->pcoll edge
	// is interior; NO dangling edges (pm-2's edge is never walked).
	if resp.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2 (member + its collection)", resp.NodeCount)
	}
	if resp.DroppedTotal != 0 {
		t.Errorf("DroppedTotal = %d, want 0; the sibling's member_of edge must not be reported (it is never walked)", resp.DroppedTotal)
	}
	if resp.DroppedByType["member_of"] != 0 {
		t.Errorf("DroppedByType[member_of] = %d, want 0", resp.DroppedByType["member_of"])
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "pcoll") {
		t.Error("dest should contain the collection node (pulled via member_of closure)")
	}
	if !nodeExists(t, dest, "pm-1") {
		t.Error("dest should contain the selected member pm-1")
	}
	if nodeExists(t, dest, "pm-2") {
		t.Error("dest must NOT contain the non-selected sibling member pm-2")
	}
}

// TestCarveOutMaterializeErrors covers the destination-side validation
// and the failure-cleanup guarantee.
func TestCarveOutMaterializeErrors(t *testing.T) {
	// (a) A destination whose home already exists -> conflict.
	t.Run("conflict_existing_home", func(t *testing.T) {
		a, _ := buildCarveSource(t)
		home := filepath.Join(t.TempDir(), "existing")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatalf("pre-create home: %v", err)
		}
		_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
			IDs:         []string{"rec-head"},
			DestDataDir: filepath.Join(home, "data"),
		})
		if apiErr == nil || apiErr.Code != "conflict" {
			t.Fatalf("expected conflict, got %v", apiErr)
		}
	})

	// (b) A relative dest_data_dir -> input error.
	t.Run("relative_dest", func(t *testing.T) {
		a, _ := buildCarveSource(t)
		_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
			IDs:         []string{"rec-head"},
			DestDataDir: filepath.Join("relative", "data"),
		})
		if apiErr == nil || apiErr.Code != "input_error" {
			t.Fatalf("expected input_error, got %v", apiErr)
		}
	})

	// (c) An invalid dest_name -> input error.
	t.Run("invalid_name", func(t *testing.T) {
		a, _ := buildCarveSource(t)
		_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
			IDs:         []string{"rec-head"},
			DestName:    "bad name!",
			DestDataDir: filepath.Join(t.TempDir(), "n", "data"),
		})
		if apiErr == nil || apiErr.Code != "input_error" {
			t.Fatalf("expected input_error, got %v", apiErr)
		}
	})

	// (d) A forced failure AFTER the store home is created must leave no
	// trace. DestDataDir points at <home>/config.yaml, so MkdirAll creates
	// a DIRECTORY exactly where config.Save then tries to write the config
	// file -- a deterministic post-MkdirAll failure. The RemoveAll(home)
	// cleanup must erase the freshly-created home. This fails if that
	// cleanup were removed.
	t.Run("failure_leaves_no_trace", func(t *testing.T) {
		a, _ := buildCarveSource(t)
		home := filepath.Join(t.TempDir(), "trace")
		destData := filepath.Join(home, "config.yaml")
		_, apiErr := a.CarveOut(context.Background(), CarveOutRequest{
			IDs:         []string{"rec-head"},
			DestDataDir: destData,
		})
		if apiErr == nil {
			t.Fatal("expected the carve to fail")
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("failed carve left a trace: home %s still exists (err=%v)", home, err)
		}
	})
}
