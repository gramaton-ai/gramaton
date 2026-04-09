package graph

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/storage"
)

func tempStorage(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.New(filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return s
}

func TestSaveAndLoadEmpty(t *testing.T) {
	g := New()
	s := tempStorage(t)

	commit, err := g.Save(s, "", "empty graph")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if commit.Hash == "" {
		t.Fatal("commit should have a hash")
	}
	if commit.Message != "empty graph" {
		t.Fatalf("expected message 'empty graph', got %q", commit.Message)
	}
	if commit.NodeTreeRoot != "" {
		t.Fatal("empty graph should have empty node tree root")
	}

	// Load into a new graph.
	g2 := New()
	loaded, err := g2.Load(s, commit.Hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Hash != commit.Hash {
		t.Fatal("loaded commit hash mismatch")
	}
	if g2.NodeCount() != 0 {
		t.Fatal("loaded graph should be empty")
	}
}

func TestSaveAndLoadWithData(t *testing.T) {
	g := New()
	s := tempStorage(t)

	ts := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	n1 := g.AddNode(Properties{
		"content":    StringProperty("Kafka decision"),
		"confidence": Float64Property(0.9),
		"created_at": TimestampProperty(ts),
		"keywords":   StringListProperty([]string{"kafka", "rabbitmq"}),
	})
	n2 := g.AddNode(Properties{
		"content": StringProperty("Redis caching"),
	})
	e, _ := g.AddEdge(n1.ID, n2.ID, "related_to", 0.7, Properties{
		"reason": StringProperty("both infrastructure"),
	})

	commit, err := g.Save(s, "", "initial capture")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if commit.Version != 1 {
		t.Fatalf("expected version 1, got %d", commit.Version)
	}
	if commit.NodeTreeRoot == "" {
		t.Fatal("expected non-empty node tree root")
	}
	if commit.EdgeTreeRoot == "" {
		t.Fatal("expected non-empty edge tree root")
	}

	// Load into a fresh graph.
	g2 := New()
	_, err = g2.Load(s, commit.Hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if g2.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", g2.NodeCount())
	}
	if g2.EdgeCount() != 1 {
		t.Fatalf("expected 1 edge, got %d", g2.EdgeCount())
	}

	// Verify node data.
	loadedN1, ok := g2.GetNode(n1.ID)
	if !ok {
		t.Fatal("n1 not found after load")
	}
	if loadedN1.Properties["content"].String() != "Kafka decision" {
		t.Fatal("n1 content mismatch")
	}
	if loadedN1.Properties["confidence"].Float64() != 0.9 {
		t.Fatal("n1 confidence mismatch")
	}
	if !loadedN1.Properties["created_at"].Timestamp().Equal(ts) {
		t.Fatal("n1 created_at mismatch")
	}
	kw := loadedN1.Properties["keywords"].StringList()
	if len(kw) != 2 || kw[0] != "kafka" || kw[1] != "rabbitmq" {
		t.Fatalf("n1 keywords mismatch: %v", kw)
	}

	// Verify edge data.
	loadedE, ok := g2.GetEdge(e.ID)
	if !ok {
		t.Fatal("edge not found after load")
	}
	if loadedE.SourceID != n1.ID {
		t.Fatal("edge source mismatch")
	}
	if loadedE.TargetID != n2.ID {
		t.Fatal("edge target mismatch")
	}
	if loadedE.Type != "related_to" {
		t.Fatal("edge type mismatch")
	}
	if loadedE.Weight != 0.7 {
		t.Fatal("edge weight mismatch")
	}

	// Verify edge indexes rebuilt.
	out := g2.EdgesFrom(n1.ID)
	if len(out) != 1 {
		t.Fatalf("expected 1 outbound edge, got %d", len(out))
	}
	in := g2.EdgesTo(n2.ID)
	if len(in) != 1 {
		t.Fatalf("expected 1 inbound edge, got %d", len(in))
	}
	byType := g2.EdgesByType("related_to")
	if len(byType) != 1 {
		t.Fatalf("expected 1 edge by type, got %d", len(byType))
	}
}

func TestLazyNodeLoading(t *testing.T) {
	g := New()
	s := tempStorage(t)

	// Create and save two nodes.
	n1 := g.AddNode(Properties{
		"content": StringProperty("node one"),
		"score":   Float64Property(0.8),
	})
	n2 := g.AddNode(Properties{
		"content": StringProperty("node two"),
	})
	commit, err := g.Save(s, "", "test commit")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into a fresh graph. With lazy loading, nodes should NOT
	// be in the in-memory cache -- they're loaded on demand.
	g2 := New()
	_, err = g2.Load(s, commit.Hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify nodes are NOT in the cache but count is correct.
	if len(g2.nodes) != 0 {
		t.Fatalf("expected 0 cached nodes after lazy load, got %d", len(g2.nodes))
	}
	if g2.NodeCount() != 2 {
		t.Fatalf("expected NodeCount 2, got %d", g2.NodeCount())
	}

	// GetNode should lazy-load from prolly tree.
	loaded, ok := g2.GetNode(n1.ID)
	if !ok {
		t.Fatal("n1 not found via lazy load")
	}
	if loaded.Properties["content"].String() != "node one" {
		t.Fatal("n1 content mismatch after lazy load")
	}
	if loaded.Properties["score"].Float64() != 0.8 {
		t.Fatal("n1 score mismatch after lazy load")
	}

	// After first access, node should be in the cache.
	if len(g2.nodes) != 1 {
		t.Fatalf("expected 1 cached node after first access, got %d", len(g2.nodes))
	}

	// Second access should hit cache.
	loaded2, ok := g2.GetNode(n1.ID)
	if !ok || loaded2 != loaded {
		t.Fatal("second GetNode should return cached pointer")
	}

	// Access n2 to verify independent loading.
	loaded3, ok := g2.GetNode(n2.ID)
	if !ok || loaded3.Properties["content"].String() != "node two" {
		t.Fatal("n2 not loaded correctly")
	}
	if len(g2.nodes) != 2 {
		t.Fatalf("expected 2 cached nodes, got %d", len(g2.nodes))
	}

	// Nonexistent node should return false.
	_, ok = g2.GetNode("NONEXISTENT")
	if ok {
		t.Fatal("nonexistent node should not be found")
	}
}

func TestLazyNodeIterator(t *testing.T) {
	g := New()
	s := tempStorage(t)

	ids := make(map[string]struct{})
	for i := 0; i < 5; i++ {
		n := g.AddNode(Properties{
			"idx": Int64Property(int64(i)),
		})
		ids[n.ID] = struct{}{}
	}
	commit, _ := g.Save(s, "", "test")

	g2 := New()
	g2.Load(s, commit.Hash)

	// Iterator should visit all 5 nodes via lazy loading.
	visited := make(map[string]struct{})
	it := g2.NodeIterator()
	for it.Next() {
		n := it.Node()
		visited[n.ID] = struct{}{}
	}
	it.Close()

	if len(visited) != 5 {
		t.Fatalf("expected 5 nodes from iterator, got %d", len(visited))
	}
	for id := range ids {
		if _, ok := visited[id]; !ok {
			t.Fatalf("node %s missing from iterator", id)
		}
	}
}

func TestSaveChainedCommits(t *testing.T) {
	g := New()
	s := tempStorage(t)

	g.AddNode(Properties{"x": StringProperty("first")})
	c1, err := g.Save(s, "", "first commit")
	if err != nil {
		t.Fatalf("Save 1: %v", err)
	}

	g.AddNode(Properties{"x": StringProperty("second")})
	c2, err := g.Save(s, c1.Hash, "second commit")
	if err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	if c2.Parent != c1.Hash {
		t.Fatal("second commit should point to first as parent")
	}

	// Load the first commit -- should only have 1 node.
	g2 := New()
	_, err = g2.Load(s, c1.Hash)
	if err != nil {
		t.Fatalf("Load c1: %v", err)
	}
	if g2.NodeCount() != 1 {
		t.Fatalf("c1 should have 1 node, got %d", g2.NodeCount())
	}

	// Load the second commit -- should have 2 nodes.
	g3 := New()
	_, err = g3.Load(s, c2.Hash)
	if err != nil {
		t.Fatalf("Load c2: %v", err)
	}
	if g3.NodeCount() != 2 {
		t.Fatalf("c2 should have 2 nodes, got %d", g3.NodeCount())
	}
}

func TestSaveContentDedup(t *testing.T) {
	g := New()
	s := tempStorage(t)

	// Two nodes with identical content should share storage chunks.
	g.AddNode(Properties{"x": StringProperty("identical")})
	g.AddNode(Properties{"x": StringProperty("identical")})

	_, err := g.Save(s, "", "dedup test")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Both nodes have different IDs (ULIDs), so they serialize differently.
	// But if two nodes had truly identical serialization, CAS would dedup.
	// This test just verifies the save works -- CAS dedup is tested in storage.
}

func TestLoadClearsExistingState(t *testing.T) {
	s := tempStorage(t)

	// Save graph with 1 node.
	g1 := New()
	g1.AddNode(Properties{"x": StringProperty("only")})
	c, _ := g1.Save(s, "", "one node")

	// Create a graph with 5 nodes, then load the 1-node commit.
	g2 := New()
	for i := 0; i < 5; i++ {
		g2.AddNode(nil)
	}
	if g2.NodeCount() != 5 {
		t.Fatal("pre-load should have 5 nodes")
	}

	_, err := g2.Load(s, c.Hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g2.NodeCount() != 1 {
		t.Fatalf("after load should have 1 node, got %d", g2.NodeCount())
	}
}

func TestCommitTimestamp(t *testing.T) {
	g := New()
	s := tempStorage(t)
	before := time.Now().UTC()

	c, _ := g.Save(s, "", "timing")

	after := time.Now().UTC()
	if c.Timestamp.Before(before) || c.Timestamp.After(after) {
		t.Fatal("commit timestamp outside expected range")
	}
}
