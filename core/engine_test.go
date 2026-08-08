package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/llm"
)

func setupTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	// Inline rather than testutil.RegisterEngineCleanup: testutil
	// imports core, so we'd cycle if we imported back. Same pattern
	// the helper applies. LIFO order means this fires before t.TempDir's
	// auto-RemoveAll, draining bbolt handles in time for Windows.
	t.Cleanup(func() {
		if err := eng.Close(); err != nil {
			t.Logf("setupTestEngine: engine close: %v", err)
		}
	})
	return eng
}

func TestLoadEngine(t *testing.T) {
	eng := setupTestEngine(t)

	if eng.Graph() == nil {
		t.Fatal("graph should not be nil")
	}
	if eng.PropIdx() == nil {
		t.Fatal("propIdx should not be nil")
	}
	if eng.VecIdx() == nil {
		t.Fatal("vecIdx should not be nil")
	}
	if eng.Searcher() == nil {
		t.Fatal("searcher should not be nil")
	}
	if eng.Embedder() != nil {
		t.Fatal("embedder should be nil (no provider configured)")
	}
	if eng.LLM() != nil {
		t.Fatal("llm should be nil (no provider configured)")
	}
}

// stubLLM is a minimal llm.Provider used to test WrapLLM without
// configuring a real provider.
type stubLLM struct{ id string }

func (s *stubLLM) Complete(context.Context, string) (string, error) { return "", nil }
func (s *stubLLM) CompleteWithModel(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *stubLLM) ModelID() string                { return s.id }
func (s *stubLLM) ProviderName() string           { return "stub" }
func (s *stubLLM) SupportsStructuredOutput() bool { return false }
func (s *stubLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

// TestWrapLLMReplacesProvider covers the happy path: a non-nil LLM
// is replaced with the wrapper returned by fn. Also verifies the
// searcher subsystem rebuilt (Searcher() still returns a usable
// tool — would panic if rebuild was skipped).
func TestWrapLLMReplacesProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	inner := &stubLLM{id: "inner"}
	eng, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithLLM(inner),
		WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	// Windows fails t.TempDir's RemoveAll if bbolt files are still
	// open; t.Cleanup is LIFO, so this fires before t.TempDir's
	// auto-cleanup. testutil's helper isn't available in package
	// core (it would create an import cycle), so use the inline form.
	t.Cleanup(func() {
		if err := eng.Close(); err != nil {
			t.Logf("engine close: %v", err)
		}
	})

	if eng.LLM() != inner {
		t.Fatalf("pre-wrap: engine.LLM() = %v, want inner stub", eng.LLM())
	}

	outer := &stubLLM{id: "outer"}
	eng.WrapLLM(func(p llm.Provider) llm.Provider {
		if p != inner {
			t.Errorf("WrapLLM fn received %v, want inner stub", p)
		}
		return outer
	})

	if eng.LLM() != outer {
		t.Errorf("post-wrap: engine.LLM() = %v, want outer stub", eng.LLM())
	}
	if eng.Searcher() == nil {
		t.Error("Searcher() = nil after WrapLLM; rebuild skipped?")
	}
}

// TestWrapLLMNilIsNoOp verifies the defensive branch: an engine
// with no LLM should not panic or wrap a nil provider (fn would
// receive nil, which most wrappers refuse to accept).
func TestWrapLLMNilIsNoOp(t *testing.T) {
	eng := setupTestEngine(t) // no LLM configured

	called := false
	eng.WrapLLM(func(p llm.Provider) llm.Provider {
		called = true
		return p
	})

	if called {
		t.Error("WrapLLM fn was invoked despite engine.LLM() == nil")
	}
	if eng.LLM() != nil {
		t.Errorf("engine.LLM() = %v after WrapLLM no-op, want nil", eng.LLM())
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	config.Save(cfg, dir+"/config.yaml")

	// Create engine and add a node.
	eng, _ := LoadEngine(dir)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test content"),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	nodeID := n.ID

	// Close first engine to release bbolt file lock before reload.
	if err := eng.Close(); err != nil {
		t.Fatalf("close first engine: %v", err)
	}

	// Reload engine from same directory.
	eng2, err := LoadEngine(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Same Windows-tempdir-cleanup reason as setupTestEngine: drain
	// eng2's bbolt handles before t.TempDir's RemoveAll fires.
	t.Cleanup(func() {
		if err := eng2.Close(); err != nil {
			t.Logf("engine close: %v", err)
		}
	})

	eng2.RLock()
	defer eng2.RUnlock()

	loaded, ok := eng2.Graph().GetNode(nodeID)
	if !ok {
		t.Fatal("node should exist after reload")
	}
	content, ok := loaded.Properties.GetString("content_full")
	if !ok || content != "test content" {
		t.Fatalf("expected 'test content', got %q", content)
	}
}

// TestSaveWritesTimestampIndex confirms engine.Save() populates the
// D7 commit_timestamps bucket for every commit, so temporal queries
// can find commits by wall-clock time without walking the parent
// chain. Phase 1's write-path hook.
func TestSaveWritesTimestampIndex(t *testing.T) {
	eng := setupTestEngine(t)

	if eng.TSIndex() == nil {
		t.Fatal("TSIndex should be non-nil after engine load")
	}
	if got := eng.TSIndex().Count(); got != 0 {
		t.Fatalf("fresh engine: tsIndex count = %d, want 0", got)
	}

	// First save.
	before1 := time.Now().UTC().Add(-time.Second)
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("first"),
	})
	c1, err := eng.Save("first")
	eng.Unlock()
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	after1 := time.Now().UTC().Add(time.Second)

	if got := eng.TSIndex().Count(); got != 1 {
		t.Errorf("after first save: count = %d, want 1", got)
	}
	// CommitAt at save timestamp should return the commit we just made.
	if h, ok := eng.TSIndex().CommitAt(c1.Timestamp); !ok || h != c1.Hash {
		t.Errorf("CommitAt(c1.Timestamp) = (%q, %v), want (%q, true)", h, ok, c1.Hash)
	}
	// CommitsBetween covering save window includes it.
	hashes := eng.TSIndex().CommitsBetween(before1, after1)
	if len(hashes) != 1 || hashes[0] != c1.Hash {
		t.Errorf("CommitsBetween around first save: got %v, want [%q]", hashes, c1.Hash)
	}

	// Second save.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("second"),
	})
	c2, err := eng.Save("second")
	eng.Unlock()
	if err != nil {
		t.Fatalf("second save: %v", err)
	}

	if got := eng.TSIndex().Count(); got != 2 {
		t.Errorf("after second save: count = %d, want 2", got)
	}
	// Both commits retrievable by their own timestamps.
	if h, _ := eng.TSIndex().CommitAt(c1.Timestamp); h != c1.Hash {
		t.Errorf("c1 lookup after c2 save: got %q, want %q", h, c1.Hash)
	}
	if h, _ := eng.TSIndex().CommitAt(c2.Timestamp); h != c2.Hash {
		t.Errorf("c2 lookup: got %q, want %q", h, c2.Hash)
	}
}

func TestHeadHash(t *testing.T) {
	eng := setupTestEngine(t)

	// Empty store has empty head hash.
	if h := eng.HeadHash(); h != "" {
		t.Fatalf("expected empty head hash, got %q", h)
	}

	// After save, head hash should be non-empty.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test"),
	})
	eng.Save("test")
	eng.Unlock()

	if h := eng.HeadHash(); h == "" {
		t.Fatal("head hash should be non-empty after save")
	}
}

func TestHeadHashLocked(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test"),
	})
	eng.Save("test")

	// HeadHashLocked should work while holding Lock.
	h := eng.HeadHashLocked()
	eng.Unlock()

	if h == "" {
		t.Fatal("HeadHashLocked should return non-empty hash")
	}

	// Should match HeadHash.
	if h != eng.HeadHash() {
		t.Fatal("HeadHashLocked and HeadHash should return same value")
	}
}

func TestSetProp(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()

	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test"),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}

	eng.SetProp(n.ID, "temporality", graph.StringProperty("durable"))
	eng.Unlock()

	eng.RLock()
	defer eng.RUnlock()
	node, _ := eng.Graph().GetNode(n.ID)
	temp, ok := node.Properties.GetString("temporality")
	if !ok || temp != "durable" {
		t.Fatalf("expected 'durable', got %q", temp)
	}
}

func TestCheckDedup(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()

	// Add two identical records with embeddings.
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("duplicate content"),
	})
	eng.VecIdx().Add(n1.ID, []float32{1.0, 0.0, 0.0})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}

	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("duplicate content again"),
		"embedding_full": graph.VectorProperty([]float32{0.99, 0.01, 0.0}),
	})
	eng.VecIdx().Add(n2.ID, []float32{0.99, 0.01, 0.0})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}

	out := eng.ScanSimilar(n2.ID)
	eng.Unlock()

	if out.Hold == nil {
		t.Fatal("should detect near-duplicate")
	}
	if out.Hold.NodeID != n1.ID {
		t.Fatalf("expected dup of %s, got %s", n1.ID, out.Hold.NodeID)
	}
	if out.Hold.Similarity < 0.9 {
		t.Fatalf("similarity should be high, got %f", out.Hold.Similarity)
	}
}

func TestCheckDedupNoDuplicate(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()

	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("topic A"),
	})
	eng.VecIdx().Add(n1.ID, []float32{1.0, 0.0, 0.0})

	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("topic B"),
		"embedding_full": graph.VectorProperty([]float32{0.0, 1.0, 0.0}),
	})
	eng.VecIdx().Add(n2.ID, []float32{0.0, 1.0, 0.0})

	out := eng.ScanSimilar(n2.ID)
	eng.Unlock()

	if out.Hold != nil || out.Advisory != nil {
		t.Fatal("should not detect duplicate for orthogonal vectors")
	}
}

func TestPreChunkAndApplyChunks(t *testing.T) {
	eng := setupTestEngine(t)
	cfg := eng.Config()

	// Create content longer than the chunking trigger.
	longContent := ""
	for i := 0; i < cfg.Chunking.Threshold+100; i++ {
		longContent += "word "
	}

	pre := eng.PreChunk(context.Background(), longContent, "")
	if pre == nil {
		t.Fatal("PreChunk should return result for long content")
	}
	if len(pre.Texts) == 0 && len(pre.Sections) == 0 {
		t.Fatal("PreChunk should produce chunks or sections")
	}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(longContent),
	})
	numChunks, err := eng.ApplyChunks(n.ID, pre, n.Properties)
	eng.Unlock()
	if err != nil {
		t.Fatalf("ApplyChunks: %v", err)
	}
	if numChunks == 0 {
		t.Fatal("ApplyChunks should create child nodes")
	}

	// Verify child nodes have chunk_of or section_of edges and carry
	// the machine-derived discriminator the exclusion surfaces key on.
	eng.RLock()
	defer eng.RUnlock()
	edges := eng.Graph().EdgesTo(n.ID)
	childEdges := 0
	for _, e := range edges {
		if e.Type != "chunk_of" && e.Type != "section_of" {
			continue
		}
		childEdges++
		child, ok := eng.Graph().GetNode(e.SourceID)
		if !ok {
			t.Fatalf("child node %s missing", e.SourceID)
		}
		nt, _ := child.Properties.GetString("node_type")
		if nt != "chunk" && nt != "section" {
			t.Fatalf("child %s node_type = %q, want chunk or section", e.SourceID, nt)
		}
		if _, has := child.Properties["section_index"]; !has {
			t.Fatalf("child %s missing section_index ordinal", e.SourceID)
		}
	}
	if childEdges != numChunks {
		t.Fatalf("expected %d child edges, got %d", numChunks, childEdges)
	}
}

func TestPreChunkShortContent(t *testing.T) {
	eng := setupTestEngine(t)
	pre := eng.PreChunk(context.Background(), "short content", "")
	if pre != nil {
		t.Fatal("PreChunk should return nil for short content")
	}
	// Content above the window floor but at or below the configured
	// threshold must also stay unchunked: the raised threshold is the
	// user-facing trigger.
	mid := strings.Repeat("word ", eng.Config().Chunking.Threshold/5)
	if pre := eng.PreChunk(context.Background(), mid, ""); pre != nil {
		t.Fatalf("PreChunk should return nil at threshold (len=%d, threshold=%d)",
			len(mid), eng.Config().Chunking.Threshold)
	}
}

// TestApplyChunksInheritsAuthor pins author propagation through the
// structural-split path: section sub-nodes inherit the parent
// record's `author` via chunking's inheritedMetadataKeys -- they are
// never re-stamped from config.
func TestApplyChunksInheritsAuthor(t *testing.T) {
	eng := setupTestEngine(t)
	const author = "Ada Lovelace <ada@example.com>"

	// Markdown-structured content so SplitSections detects sections
	// (the metadata-inheriting path); long enough to exceed the
	// chunking threshold.
	// Enough body per section to clear the raised chunking threshold
	// while keeping each section under SectionMax.
	var sb strings.Builder
	for i := 0; i < 4; i++ {
		sb.WriteString("## Section heading ")
		sb.WriteString(strings.Repeat("x", i+1))
		sb.WriteString("\n\n")
		for j := 0; j < 60; j++ {
			sb.WriteString("Body sentence for the author inheritance test. ")
		}
		sb.WriteString("\n\n")
	}
	content := sb.String()

	pre := eng.PreChunk(context.Background(), content, "")
	if pre == nil {
		t.Fatalf("PreChunk should return a result for long content (len=%d)", len(content))
	}
	if len(pre.Sections) == 0 {
		t.Fatal("expected structural sections (the author-inheriting path); markdown headings were not detected")
	}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(content),
		"author":       graph.StringProperty(author),
	})
	numChunks, err := eng.ApplyChunks(n.ID, pre, n.Properties)
	eng.Unlock()
	if err != nil {
		t.Fatalf("ApplyChunks: %v", err)
	}
	if numChunks == 0 {
		t.Fatal("ApplyChunks should create section nodes")
	}

	eng.RLock()
	defer eng.RUnlock()
	sections := 0
	for _, e := range eng.Graph().EdgesTo(n.ID) {
		if e.Type != "section_of" {
			continue
		}
		sections++
		child, ok := eng.Graph().GetNode(e.SourceID)
		if !ok {
			t.Fatalf("section node %s missing", e.SourceID)
		}
		got, has := child.Properties.GetString("author")
		if !has {
			t.Errorf("section %s has no author property, want inherited %q", e.SourceID, author)
			continue
		}
		if got != author {
			t.Errorf("section %s author = %q, want inherited %q", e.SourceID, got, author)
		}
	}
	if sections != numChunks {
		t.Fatalf("expected %d section_of children, got %d", numChunks, sections)
	}
}

func TestRebuildAllIndexes(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()

	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test"),
		"temporality":  graph.StringProperty("durable"),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, []float32{0.5, 0.5, 0.0})

	// Rebuild should preserve the data.
	eng.RebuildAllIndexes()

	// Verify index works after rebuild.
	ids := eng.PropIdx().Lookup("temporality", graph.StringProperty("durable"))
	eng.Unlock()

	if len(ids) != 1 {
		t.Fatalf("expected 1 durable record after rebuild, got %d", len(ids))
	}
}

// TestRebuildPrimaryIfMissingAlwaysRebuildsProp pins the fix for the
// partial-load bug at core/indexes.go: the original
// `propLoaded := s.propIdx.Count() > 0` short-circuit skipped rebuild
// whenever the index had ANY data, leaving newly-added or previously-
// unindexed keys missing forever. The test simulates that state by
// indexing only one of two fields on a node and then re-running
// rebuildPrimaryIfMissing; both fields must appear in the index.
func TestRebuildPrimaryIfMissingAlwaysRebuildsProp(t *testing.T) {
	eng := setupTestEngine(t)
	eng.Lock()
	defer eng.Unlock()

	g := eng.Graph()
	propIdx := eng.PropIdx()

	n := g.AddNode(graph.Properties{
		"temporality":    graph.StringProperty("durable"),
		"knowledge_type": graph.StringProperty("semantic"),
	})
	// Index only one of the two fields, mimicking a partial load
	// where temporality was indexed but knowledge_type predates the
	// indexing change for that key.
	propIdx.Add(n.ID, "temporality", graph.StringProperty("durable"))

	if got := propIdx.Lookup("knowledge_type", graph.StringProperty("semantic")); len(got) != 0 {
		t.Fatalf("precondition: knowledge_type should be unindexed, got %v", got)
	}

	eng.indexes.rebuildPrimaryIfMissing(g)

	if got := propIdx.Lookup("temporality", graph.StringProperty("durable")); len(got) != 1 {
		t.Fatalf("temporality not indexed after rebuild, got %v", got)
	}
	if got := propIdx.Lookup("knowledge_type", graph.StringProperty("semantic")); len(got) != 1 {
		t.Fatalf("knowledge_type not indexed after rebuild (the bug), got %v", got)
	}
}

func TestConfig(t *testing.T) {
	eng := setupTestEngine(t)
	cfg := eng.Config()

	if cfg.Scoring.WeightSimilarity != 0.55 {
		t.Fatalf("expected default weight_similarity 0.55, got %f", cfg.Scoring.WeightSimilarity)
	}
	if cfg.SaveGuard.SimilarHoldThreshold != 0.94 {
		t.Fatalf("expected default hold threshold 0.94, got %f", cfg.SaveGuard.SimilarHoldThreshold)
	}
}

func TestCheckDedupJaccardGuard(t *testing.T) {
	eng := setupTestEngine(t)

	// Create a fake 8-dim embedding. We'll reuse the same vector to
	// guarantee cosine >= threshold, isolating the Jaccard guard.
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	eng.Lock()

	// Record A: article about functionalism.
	nA := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(
			"Functionalism is a theory about the nature of mental states. " +
				"According to functionalism, mental states are identified by what they do " +
				"rather than by what they are made of. Memory trace decay is defined " +
				"by its functional role in cognitive systems. The identity theory " +
				"argues that mental states are identical to brain states."),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.PropIdx().Add(nA.ID, "content_full", nA.Properties["content_full"])
	eng.VecIdx().Add(nA.ID, vec)

	// Record B: completely different article about time, but same embedding
	// (simulates the false positive scenario).
	nB := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(
			"Time is one of the most fundamental concepts in physics and " +
				"philosophy. The nature of time has been debated for millennia. " +
				"Some philosophers argue that time is an illusion, while others " +
				"maintain it is a fundamental feature of the universe. Temporal " +
				"experience shapes our understanding of causation and change."),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.PropIdx().Add(nB.ID, "content_full", nB.Properties["content_full"])
	eng.VecIdx().Add(nB.ID, vec)

	eng.Unlock()

	// The scan for B should NOT hold against A: Jaccard similarity
	// between the two articles is low (the false-positive guard).
	eng.RLock()
	outB := eng.ScanSimilar(nB.ID)
	eng.RUnlock()

	if outB.Hold != nil {
		t.Fatalf("Jaccard guard should reject false positive, but got hold=%+v", outB.Hold)
	}

	// Now add a genuine duplicate with slightly different wording.
	eng.Lock()
	nC := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(
			"Functionalism is a theory about the nature of mental states. " +
				"According to functionalism, mental states are identified by what they do " +
				"rather than by what they are made of. Memory trace decay is defined " +
				"by its functional role in cognitive systems. The identity theory " +
				"claims that mental states are identical to brain states."),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.PropIdx().Add(nC.ID, "content_full", nC.Properties["content_full"])
	eng.VecIdx().Add(nC.ID, vec)
	eng.Unlock()

	// The scan for C should hold: A and C's content are near-identical.
	eng.RLock()
	outC := eng.ScanSimilar(nC.ID)
	eng.RUnlock()

	if outC.Hold == nil {
		t.Fatal("Jaccard guard should allow genuine duplicate, but got no hold")
	}
}

func TestCheckDedupShortContentSkipsJaccard(t *testing.T) {
	eng := setupTestEngine(t)
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	eng.Lock()
	// Two short records with same embedding but different content.
	// Jaccard check should be skipped for short content.
	nA := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("user prefers dark mode"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.PropIdx().Add(nA.ID, "content_full", nA.Properties["content_full"])
	eng.VecIdx().Add(nA.ID, vec)

	nB := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("user likes light theme"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.PropIdx().Add(nB.ID, "content_full", nB.Properties["content_full"])
	eng.VecIdx().Add(nB.ID, vec)
	eng.Unlock()

	// Short content skips Jaccard, so cosine alone determines match.
	eng.RLock()
	out := eng.ScanSimilar(nB.ID)
	eng.RUnlock()

	if out.Hold == nil {
		t.Fatal("short content should skip Jaccard guard and hold on cosine alone")
	}
}

func TestNodeAndEdgeCount(t *testing.T) {
	eng := setupTestEngine(t)

	if eng.NodeCount() != 0 {
		t.Fatalf("expected 0 nodes, got %d", eng.NodeCount())
	}

	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("a"),
	})
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("b"),
	})
	eng.Graph().AddEdge(n1.ID, n2.ID, "related_to", 0.5, nil)
	eng.Unlock()

	if eng.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", eng.NodeCount())
	}
	if eng.EdgeCount() != 1 {
		t.Fatalf("expected 1 edge, got %d", eng.EdgeCount())
	}
}

func TestIndexNode(t *testing.T) {
	eng := setupTestEngine(t)
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("test content about memory"),
		"temporality":       graph.StringProperty("durable"),
		"processing_status": graph.StringProperty("processed"),
	})
	eng.IndexNode(n.ID, "test content about memory", vec)
	eng.Unlock()

	// PropIdx should have the properties.
	eng.RLock()
	ids := eng.PropIdx().Lookup("temporality", graph.StringProperty("durable"))
	eng.RUnlock()
	found := false
	for _, id := range ids {
		if id == n.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("PropIdx should contain the node")
	}

	// VecIdx should have the vector.
	if eng.VecIdx().Len() != 1 {
		t.Fatalf("VecIdx should have 1 entry, got %d", eng.VecIdx().Len())
	}

	// BM25 should find the content.
	results := eng.BM25Full().Search([]string{"memory"}, 10, nil)
	if len(results) != 1 || results[0].NodeID != n.ID {
		t.Fatalf("BM25 should find the node, got %v", results)
	}
}

func TestIndexNodeSkipsEmptyContent(t *testing.T) {
	eng := setupTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("some content"),
	})
	eng.IndexNode(n.ID, "", nil) // empty content, nil vec
	eng.Unlock()

	// BM25 should be empty (no content indexed).
	if eng.BM25Full().Len() != 0 {
		t.Fatalf("BM25 should be empty for empty content, got %d", eng.BM25Full().Len())
	}
	// VecIdx should be empty.
	if eng.VecIdx().Len() != 0 {
		t.Fatal("VecIdx should be empty for nil vec")
	}
}

func TestSetContentPropUpdatesBM25(t *testing.T) {
	eng := setupTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("original content about cats"),
	})
	eng.IndexNode(n.ID, "original content about cats", nil)

	// Verify BM25 finds "cats".
	results := eng.BM25Full().Search([]string{"cats"}, 10, nil)
	if len(results) != 1 {
		t.Fatal("BM25 should find 'cats' initially")
	}

	// Update content via SetContentProp.
	eng.SetContentProp(n.ID, "content_full", "updated content about dogs")

	// BM25 should now find "dogs" but not "cats".
	results = eng.BM25Full().Search([]string{"dogs"}, 10, nil)
	if len(results) != 1 {
		t.Fatal("BM25 should find 'dogs' after update")
	}
	results = eng.BM25Full().Search([]string{"cats"}, 10, nil)
	if len(results) != 0 {
		t.Fatal("BM25 should not find 'cats' after content update")
	}

	eng.Unlock()
}

func TestIndexNodeSetsEmbeddingFullProperty(t *testing.T) {
	eng := setupTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("test content"),
	})
	vec := []float32{0.1, 0.2, 0.3}
	eng.IndexNode(n.ID, "test content", vec)
	eng.Unlock()

	// The embedding_full property should be set on the node.
	eng.RLock()
	defer eng.RUnlock()
	loaded, ok := eng.Graph().GetNode(n.ID)
	if !ok {
		t.Fatal("node not found")
	}
	storedVec, ok := loaded.Properties.GetVector("embedding_full")
	if !ok {
		t.Fatal("embedding_full property not set on node after IndexNode")
	}
	if len(storedVec) != 3 || storedVec[0] != 0.1 {
		t.Fatalf("embedding_full value mismatch: %v", storedVec)
	}
}

func TestSetContentPropUpdatesBM25Layers(t *testing.T) {
	eng := setupTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("full content about databases"),
		"content_medium": graph.StringProperty("databases overview"),
		"content_short":  graph.StringProperty("databases"),
	})
	eng.IndexNode(n.ID, "full content about databases", nil)

	// Manually add to medium and short BM25 indexes via SetContentProp.
	eng.SetContentProp(n.ID, "content_medium", "databases overview")
	eng.SetContentProp(n.ID, "content_short", "databases")
	eng.Unlock()

	// BM25 index should have entries (single layer, D12).
	if eng.BM25Full().Len() != 1 {
		t.Fatalf("BM25Full should have 1 entry, got %d", eng.BM25Full().Len())
	}

	if len(eng.BM25Full().Search([]string{"databases"}, 10, nil)) != 1 {
		t.Fatal("BM25Full should find 'databases'")
	}
}

// TestWithWriteBatchSaves verifies the helper takes the lock, runs fn
// under a single bbolt transaction, and persists state when mutated
// is true. Caller must observe the record after release.
func TestWithWriteBatchSaves(t *testing.T) {
	eng := setupTestEngine(t)
	defer eng.Close()

	var newID string
	err := eng.WithWriteBatch("test: mutated", func(ws *WriteSession) (bool, error) {
		n := ws.AddNode(graph.Properties{
			"content_full": graph.StringProperty("hello"),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
		})
		newID = n.ID
		ws.IndexNode(n.ID, "hello", nil)
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(newID); !ok {
		t.Fatal("node should exist after WithWriteBatch")
	}
	if eng.headHash == "" {
		t.Fatal("headHash should be set after Save")
	}
}

// TestWithWriteBatchSkipsSaveOnNoMutations verifies the "mutated=false"
// branch does not call Save. Checked by observing that headHash
// stays empty (no commit landed) while fn still ran under the lock.
func TestWithWriteBatchSkipsSaveOnNoMutations(t *testing.T) {
	eng := setupTestEngine(t)
	defer eng.Close()

	prevHead := eng.headHash
	ran := false
	err := eng.WithWriteBatch("test: noop", func(_ *WriteSession) (bool, error) {
		ran = true
		return false, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}
	if !ran {
		t.Fatal("fn should have run")
	}
	if eng.headHash != prevHead {
		t.Fatalf("headHash should not change on no-op, got %q (was %q)", eng.headHash, prevHead)
	}
}

// TestWithWriteBatchPropagatesFnError confirms fn errors are wrapped
// with the message label and Save is skipped.
func TestWithWriteBatchPropagatesFnError(t *testing.T) {
	eng := setupTestEngine(t)
	defer eng.Close()

	sentinel := errors.New("boom")
	err := eng.WithWriteBatch("test: err", func(_ *WriteSession) (bool, error) {
		return true, sentinel
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel in chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "test: err") {
		t.Fatalf("expected label in error, got %v", err)
	}
	if eng.headHash != "" {
		t.Fatal("headHash should stay empty on fn error")
	}
}

// TestSaveStampsCommitAuthor pins commit-level attribution: user
// commits carry the configured author, curation-labeled commits
// carry the curation identity, and both survive the round trip
// through the commit chunk.
func TestSaveStampsCommitAuthor(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Author.Name = "Commit Stamp Tester"
	if err := config.Save(cfg, cfgPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("author stamp fixture")})
	userCommit, err := eng.Save("save record")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("curation fixture")})
	curCommit, err := eng.Save("curation: cycle work")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save curation: %v", err)
	}
	eng.Unlock()

	if userCommit.Author != eng.Config().Author.String() || userCommit.Author == "" {
		t.Fatalf("user commit author = %q, want the configured author", userCommit.Author)
	}
	if curCommit.Author != CurationAuthor {
		t.Fatalf("curation commit author = %q, want %q", curCommit.Author, CurationAuthor)
	}
}

// TestApplyChunksFailedBatchUndoesInMemoryState pins the undo path:
// a failed batch commit must leave no zombie children in the
// in-memory graph or vector index -- the bbolt rollback cannot reach
// those, and a subsequent engine.Save would otherwise persist
// edge-less pseudo-records repair cannot detect.
func TestApplyChunksFailedBatchUndoesInMemoryState(t *testing.T) {
	eng := setupTestEngine(t)
	longContent := strings.Repeat("word ", eng.Config().Chunking.Threshold/4)

	pre := eng.PreChunk(context.Background(), longContent, "")
	if pre == nil {
		t.Fatal("PreChunk returned nil for long content")
	}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(longContent),
	})
	applyChunksFaultForTests = func() error { return fmt.Errorf("injected batch failure") }
	created, err := eng.ApplyChunks(n.ID, pre, n.Properties)
	applyChunksFaultForTests = nil
	eng.Unlock()

	if err == nil {
		t.Fatal("expected the injected failure to surface")
	}
	if created != 0 {
		t.Fatalf("failed apply reported %d children", created)
	}

	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesTo(n.ID) {
		if e.Type == "section_of" || e.Type == "chunk_of" {
			t.Fatalf("child edge %s survived the rollback", e.ID)
		}
	}
	zombies := 0
	for id := range eng.Graph().NodeIDSet() {
		if child, ok := eng.Graph().GetNode(id); ok && graph.IsSectionOrChunk(child.Properties) {
			zombies++
		}
	}
	if zombies != 0 {
		t.Fatalf("%d zombie children survived the failed batch", zombies)
	}
}
