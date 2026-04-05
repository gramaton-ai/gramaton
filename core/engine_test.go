package core

import (
	"context"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
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
	eng, err := LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
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

	// Reload engine from same directory.
	eng2, err := LoadEngine(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

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

	dupID, sim := eng.CheckDedup(n2.ID)
	eng.Unlock()

	if dupID == "" {
		t.Fatal("should detect near-duplicate")
	}
	if dupID != n1.ID {
		t.Fatalf("expected dup of %s, got %s", n1.ID, dupID)
	}
	if sim < 0.9 {
		t.Fatalf("similarity should be high, got %f", sim)
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

	dupID, _ := eng.CheckDedup(n2.ID)
	eng.Unlock()

	if dupID != "" {
		t.Fatal("should not detect duplicate for orthogonal vectors")
	}
}

func TestPreChunkAndApplyChunks(t *testing.T) {
	eng := setupTestEngine(t)
	cfg := eng.Config()

	// Create content longer than chunk threshold.
	longContent := ""
	for i := 0; i < cfg.Chunking.Threshold+100; i++ {
		longContent += "word "
	}

	pre := eng.PreChunk(context.Background(), longContent)
	if pre == nil {
		t.Fatal("PreChunk should return result for long content")
	}
	if len(pre.Texts) == 0 {
		t.Fatal("PreChunk should produce chunks")
	}

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty(longContent),
	})
	numChunks := eng.ApplyChunks(n.ID, pre)
	eng.Unlock()

	if numChunks == 0 {
		t.Fatal("ApplyChunks should create chunk nodes")
	}

	// Verify chunk nodes have chunk_of edges.
	eng.RLock()
	defer eng.RUnlock()
	edges := eng.Graph().EdgesTo(n.ID)
	chunkEdges := 0
	for _, e := range edges {
		if e.Type == "chunk_of" {
			chunkEdges++
		}
	}
	if chunkEdges != numChunks {
		t.Fatalf("expected %d chunk_of edges, got %d", numChunks, chunkEdges)
	}
}

func TestPreChunkShortContent(t *testing.T) {
	eng := setupTestEngine(t)
	pre := eng.PreChunk(context.Background(), "short content")
	if pre != nil {
		t.Fatal("PreChunk should return nil for short content")
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

func TestConfig(t *testing.T) {
	eng := setupTestEngine(t)
	cfg := eng.Config()

	if cfg.Scoring.WeightSimilarity != 0.35 {
		t.Fatalf("expected default weight_similarity 0.35, got %f", cfg.Scoring.WeightSimilarity)
	}
	if cfg.Dedup.SimilarityThreshold != 0.92 {
		t.Fatalf("expected default dedup threshold 0.92, got %f", cfg.Dedup.SimilarityThreshold)
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
