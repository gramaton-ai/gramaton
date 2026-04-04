// Package core provides the shared engine that manages the knowledge
// graph, indexes, embedding, and persistence. Both the HTTP server
// and CLI thin client operate through this engine. The engine is
// safe for concurrent use via an internal RWMutex.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/embed"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
	"github.com/brandonlattin/gramaton/search"
	"github.com/brandonlattin/gramaton/storage"
)

// Engine holds the loaded graph state, indexes, and providers.
// All public methods are safe for concurrent use.
type Engine struct {
	mu sync.RWMutex

	cfg      config.Config
	store    *storage.Store
	graph    *graph.Graph
	propIdx  *index.PropertyIndex
	vecIdx   *index.FlatIndex
	embedder embed.Provider
	searcher *search.Tool
	headHash string
}

// LoadEngine loads config, storage, graph state, and rebuilds indexes.
// The embedder may be nil if no embedding provider is configured.
// Ollama auto-start is NOT performed -- the caller is responsible
// for ensuring the embedding provider is reachable.
func LoadEngine(cfgDir string) (*Engine, error) {
	cfgPath := filepath.Join(cfgDir, "config.yaml")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(cfgDir, "data")
	}

	s, err := storage.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	// Load HEAD commit if it exists.
	var headHash string
	headPath := filepath.Join(cfg.DataDir, "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		headHash = strings.TrimSpace(string(data))
		if headHash != "" {
			if _, err := g.Load(s, headHash); err != nil {
				return nil, fmt.Errorf("load HEAD commit: %w", err)
			}
		}
	}

	rebuildIndexes(g, propIdx, vecIdx)

	emb, err := embed.New(cfg.Embedding)
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}

	searcher := search.New(g, propIdx, vecIdx, emb, cfg)

	return &Engine{
		cfg:      cfg,
		store:    s,
		graph:    g,
		propIdx:  propIdx,
		vecIdx:   vecIdx,
		embedder: emb,
		searcher: searcher,
		headHash: headHash,
	}, nil
}

// Config returns the engine's config. Safe for concurrent read.
func (e *Engine) Config() config.Config {
	return e.cfg
}

// HeadHash returns the current HEAD commit hash.
func (e *Engine) HeadHash() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.headHash
}

// Graph returns the underlying graph. Callers must hold the
// appropriate lock via RLock/RUnlock or Lock/Unlock.
func (e *Engine) Graph() *graph.Graph { return e.graph }

// PropIdx returns the property index.
func (e *Engine) PropIdx() *index.PropertyIndex { return e.propIdx }

// VecIdx returns the vector index.
func (e *Engine) VecIdx() *index.FlatIndex { return e.vecIdx }

// Embedder returns the embedding provider (may be nil).
func (e *Engine) Embedder() embed.Provider { return e.embedder }

// Searcher returns the search tool.
func (e *Engine) Searcher() *search.Tool { return e.searcher }

// Store returns the storage backend.
func (e *Engine) Store() *storage.Store { return e.store }

// RLock acquires a read lock. Use for read operations (search,
// inspect, explore, etc.). Multiple readers can hold the lock
// concurrently.
func (e *Engine) RLock() { e.mu.RLock() }

// RUnlock releases the read lock.
func (e *Engine) RUnlock() { e.mu.RUnlock() }

// Lock acquires a write lock. Use for write operations (capture,
// update, classify, delete, etc.). Exclusive -- blocks all other
// readers and writers.
func (e *Engine) Lock() { e.mu.Lock() }

// Unlock releases the write lock.
func (e *Engine) Unlock() { e.mu.Unlock() }

// Save commits the current graph state and updates HEAD and the
// active branch ref. Caller must hold the write lock.
func (e *Engine) Save(message string) (*graph.Commit, error) {
	commit, err := e.graph.Save(e.store, e.headHash, message, storage.ProllyConfig{
		TargetChunkSize: e.cfg.Storage.ProllyTargetChunkSize,
		SplitBits:       e.cfg.Storage.ProllySplitBits,
	})
	if err != nil {
		return nil, fmt.Errorf("save commit: %w", err)
	}

	headPath := filepath.Join(e.cfg.DataDir, "HEAD")
	if err := AtomicWriteFile(headPath, []byte(commit.Hash), 0o600); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	branch := ActiveBranch(e.cfg.DataDir)
	WriteRef(e.cfg.DataDir, branch, commit.Hash)

	e.headHash = commit.Hash
	return commit, nil
}

// RebuildAllIndexes clears and rebuilds all indexes from graph state.
// Caller must hold the write lock.
func (e *Engine) RebuildAllIndexes() {
	e.propIdx = index.NewPropertyIndex()
	e.vecIdx = index.NewFlatIndex()
	rebuildIndexes(e.graph, e.propIdx, e.vecIdx)
	e.searcher = search.New(e.graph, e.propIdx, e.vecIdx, e.embedder, e.cfg)
}

// GenerateEmbeddings creates embeddings for a node's content properties.
// Caller must hold the write lock.
func (e *Engine) GenerateEmbeddings(ctx context.Context, nodeID string) error {
	if e.embedder == nil {
		return nil
	}

	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return fmt.Errorf("node %s not found", nodeID)
	}

	type embedTarget struct {
		sourceKey string
		embedKey  string
	}
	targets := []embedTarget{
		{"content_keywords", "embedding_keywords"},
		{"content_short", "embedding_short"},
		{"content_abstract", "embedding_abstract"},
		{"content_full", "embedding_full"},
	}

	var texts []string
	var keys []string
	for _, t := range targets {
		var text string
		if sl, ok := n.Properties.GetStringList(t.sourceKey); ok {
			text = strings.Join(sl, " ")
		} else if s, ok := n.Properties.GetString(t.sourceKey); ok {
			text = s
		}
		if text != "" {
			texts = append(texts, text)
			keys = append(keys, t.embedKey)
		}
	}

	if len(texts) == 0 {
		return nil
	}

	vectors, err := e.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("generate embeddings: %w", err)
	}

	for i, vec := range vectors {
		prop := graph.VectorProperty(vec)
		e.graph.SetNodeProperty(nodeID, keys[i], prop)
		e.propIdx.Add(nodeID, keys[i], prop)
	}

	if len(vectors) > 0 {
		e.vecIdx.Add(nodeID, vectors[len(vectors)-1])
	}

	modelProp := graph.StringProperty(e.embedder.ModelID())
	e.graph.SetNodeProperty(nodeID, "embedding_model", modelProp)
	e.propIdx.Add(nodeID, "embedding_model", modelProp)

	return nil
}

// CheckDedup checks if a node's embedding is too similar to existing
// records. Caller must hold at least a read lock.
func (e *Engine) CheckDedup(nodeID string) (string, float64) {
	if e.vecIdx.Len() < 2 {
		return "", 0
	}

	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return "", 0
	}

	var vec []float32
	for _, key := range []string{"embedding_full", "embedding_abstract", "embedding_short", "embedding_keywords"} {
		if v, ok := n.Properties.GetVector(key); ok {
			vec = v
			break
		}
	}
	if vec == nil {
		return "", 0
	}

	results := e.vecIdx.Search(vec, 2, nil)
	for _, r := range results {
		if r.NodeID == nodeID {
			continue
		}
		if float64(r.Similarity) >= e.cfg.Dedup.SimilarityThreshold {
			return r.NodeID, float64(r.Similarity)
		}
	}
	return "", 0
}

// ChunkIfNeeded splits a node's content into chunk child nodes if
// it exceeds the configured threshold. Caller must hold the write lock.
func (e *Engine) ChunkIfNeeded(ctx context.Context, nodeID string) (int, error) {
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return 0, fmt.Errorf("node %s not found", nodeID)
	}

	contentProp, ok := n.Properties["content_full"]
	if !ok {
		return 0, nil
	}
	content := contentProp.String()

	chunks := graph.ChunkText(
		content,
		e.cfg.Chunking.Threshold,
		e.cfg.Chunking.ChunkSize,
		e.cfg.Chunking.Overlap,
	)
	if len(chunks) == 0 {
		return 0, nil
	}

	for _, chunkText := range chunks {
		chunkNode := e.graph.AddNode(graph.Properties{
			"content_full": graph.StringProperty(chunkText),
		})
		e.graph.AddEdge(chunkNode.ID, nodeID, "chunk_of", 1.0, nil)
		for k, v := range chunkNode.Properties {
			e.propIdx.Add(chunkNode.ID, k, v)
		}
		if err := e.GenerateEmbeddings(ctx, chunkNode.ID); err != nil {
			continue
		}
	}

	return len(chunks), nil
}

// SetProp sets a property on a node and updates the property index.
// Caller must hold the write lock.
func (e *Engine) SetProp(nodeID, key string, val graph.Property) {
	if n, ok := e.graph.GetNode(nodeID); ok {
		if old, ok := n.Properties[key]; ok {
			e.propIdx.Remove(nodeID, key, old)
		}
	}
	e.graph.SetNodeProperty(nodeID, key, val)
	e.propIdx.Add(nodeID, key, val)
}

// NodeCount returns the number of nodes. Safe without lock for
// approximate counts.
func (e *Engine) NodeCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph.NodeCount()
}

// EdgeCount returns the number of edges. Safe without lock for
// approximate counts.
func (e *Engine) EdgeCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph.EdgeCount()
}

// rebuildIndexes populates indexes from graph state.
func rebuildIndexes(g *graph.Graph, propIdx *index.PropertyIndex, vecIdx *index.FlatIndex) {
	for _, id := range g.AllNodeIDs() {
		n, _ := g.GetNode(id)
		for k, v := range n.Properties {
			propIdx.Add(id, k, v)
		}
		for _, embKey := range []string{"embedding_full", "embedding_abstract", "embedding_short", "embedding_keywords"} {
			if v, ok := n.Properties.GetVector(embKey); ok {
				vecIdx.Add(id, v)
				break
			}
		}
	}
}
