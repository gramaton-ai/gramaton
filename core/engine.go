// Package core provides the shared engine that manages the knowledge
// graph, indexes, embedding, and persistence. Both the HTTP server
// and CLI thin client operate through this engine. The engine is
// safe for concurrent use via an internal RWMutex.
package core

import (
	"context"
	"encoding"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/search"
	"github.com/gramaton-ai/gramaton/storage"
)

// Engine holds the loaded graph state, indexes, and providers.
// All public methods are safe for concurrent use.
type Engine struct {
	mu sync.RWMutex

	cfg      config.Config
	store    *storage.Store
	graph    *graph.Graph
	boltDB   *bolt.DB // shared bbolt database for property index + edge store
	propIdx  index.PropertyIndex
	vecIdx   index.VectorIndex
	bm25Full   index.BM25Index // content_full BM25 (D12: single layer)
	embedder embed.Provider
	llmProv  llm.Provider
	searcher *search.Tool
	headHash string

	// accessDirty is set when access metadata (access_count,
	// last_accessed, activation_boost) has been recorded in memory
	// but not yet persisted to disk. The server flushes this
	// periodically rather than saving on every read.
	accessDirty bool
}

// EngineOption configures an engine at construction time. Options are
// applied after default initialization, overriding config-derived values.
// This is the only supported way to inject dependencies -- the engine
// is immutable after construction.
type EngineOption func(*Engine)

// WithEmbedder overrides the embedding provider. Use in tests to inject
// a mock embedder without requiring a real Ollama/API endpoint.
func WithEmbedder(p embed.Provider) EngineOption {
	return func(e *Engine) { e.embedder = p }
}

// WithLLM overrides the LLM provider. Use in tests to inject a mock
// LLM without requiring a real API key.
func WithLLM(p llm.Provider) EngineOption {
	return func(e *Engine) { e.llmProv = p }
}

// WithVectorIndex overrides the vector index. Use in tests to inject
// an in-memory FlatIndex instead of the disk-backed MmapFlatIndex.
// When set, the engine skips creating/opening the mmap vector file.
func WithVectorIndex(v index.VectorIndex) EngineOption {
	return func(e *Engine) { e.vecIdx = v }
}

// LoadEngine loads config, storage, graph state, and rebuilds indexes.
// The embedder may be nil if no embedding provider is configured.
// Ollama auto-start is NOT performed -- the caller is responsible
// for ensuring the embedding provider is reachable.
//
// If globalCfgDir is provided and differs from cfgDir, the config is
// loaded with fallback: store-specific config first, then global.
// This supports named stores that inherit the global config.
func LoadEngine(cfgDir string, globalCfgDir ...string) (*Engine, error) {
	return LoadEngineWithOptions(cfgDir, globalCfgDir, nil)
}

// LoadEngineWithOptions is like LoadEngine but accepts functional options
// for dependency injection. Options are applied after all default
// initialization is complete.
func LoadEngineWithOptions(cfgDir string, globalCfgDirs []string, opts []EngineOption) (*Engine, error) {
	cfgPath := filepath.Join(cfgDir, "config.yaml")

	var cfg config.Config
	var err error
	if len(globalCfgDirs) > 0 && globalCfgDirs[0] != "" && globalCfgDirs[0] != cfgDir {
		globalCfgPath := filepath.Join(globalCfgDirs[0], "config.yaml")
		cfg, err = config.LoadWithFallback(cfgPath, globalCfgPath)
	} else {
		cfg, err = config.Load(cfgPath)
	}
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

	if err := CheckFormatVersion(cfg.DataDir); err != nil {
		return nil, fmt.Errorf("store format: %w", err)
	}

	// Open the shared bbolt database for property index and edge store.
	boltPath := filepath.Join(cfg.DataDir, "indexes.db")
	boltDB, err := bolt.Open(boltPath, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}

	propIdx, err := index.NewBboltPropertyIndex(boltDB, index.DefaultIndexedFields)
	if err != nil {
		boltDB.Close()
		return nil, fmt.Errorf("create bbolt property index: %w", err)
	}

	edgeStore, err := graph.NewBboltEdgeStore(boltDB, graph.DefaultEdgeCacheCapacity)
	if err != nil {
		boltDB.Close()
		return nil, fmt.Errorf("create bbolt edge store: %w", err)
	}

	g := graph.NewWithCapacity(graph.DefaultCacheCapacity, graph.WithEdgeStore(edgeStore))
	bm25Full, err := index.NewBboltBM25Index(boltDB, cfg.Search.BM25K1, cfg.Search.BM25B)
	if err != nil {
		boltDB.Close()
		return nil, fmt.Errorf("create bbolt BM25 index: %w", err)
	}

	// Load HEAD commit if it exists.
	var headHash string
	var headCommit *graph.Commit
	headPath := filepath.Join(cfg.DataDir, "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		headHash = strings.TrimSpace(string(data))
		if headHash != "" {
			c, err := g.Load(s, headHash)
			if err != nil {
				return nil, fmt.Errorf("load HEAD commit: %w", err)
			}
			headCommit = c
		}
	}

	emb, err := embed.New(cfg.Embedding)
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}

	llmProv, err := llm.New(cfg.LLM)
	if err != nil {
		// LLM is optional -- log the error but don't fail.
		llmProv = nil
	}

	e := &Engine{
		cfg:      cfg,
		store:    s,
		graph:    g,
		boltDB:   boltDB,
		propIdx:  propIdx,
		bm25Full: bm25Full,
		embedder: emb,
		llmProv:  llmProv,
		headHash: headHash,
	}

	// Apply options before creating the vector index. This lets
	// WithVectorIndex inject an in-memory index for tests,
	// avoiding the disk I/O of MmapFlatIndex creation.
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	// If no option provided a vector index, open the mmap'd flat
	// vector index (D1 revised: flat as v1 default).
	if e.vecIdx == nil {
		vecDim := cfg.Embedding.Dimension
		if vecDim <= 0 {
			vecDim = 384 // MiniLM-L6 default (D3)
		}
		vecPath := filepath.Join(cfg.DataDir, "vec.flat")
		mmapVec, err := index.NewMmapFlatIndex(vecPath, vecDim)
		if err != nil {
			boltDB.Close()
			return nil, fmt.Errorf("open vector index: %w", err)
		}
		e.vecIdx = mmapVec
	}

	// Try to load persisted indexes from commit. Each index that loads
	// successfully is skipped during rebuildIndexes.
	bm25FullLoaded := bm25Full.Len() > 0
	vecLoaded := e.vecIdx.Len() > 0
	propLoaded := propIdx.Count() > 0

	if headCommit != nil {
		// BM25: skip CAS loading (bbolt is authoritative).
		// VecRoot: skip CAS loading (mmap flat file is authoritative).
		// Property index: skip CAS loading (bbolt is authoritative).
		// Edge adjacency: skip CAS loading (bbolt edge store is authoritative).
	}

	rebuildIndexes(boltDB, g, propIdx, e.vecIdx, bm25Full, bm25FullLoaded, vecLoaded, propLoaded)

	// Build searcher after all indexes are finalized.
	e.searcher = search.New(g, propIdx, e.vecIdx, bm25Full, emb, cfg)

	return e, nil
}

// Config returns the engine's config. Safe for concurrent read.
func (e *Engine) Config() config.Config {
	return e.cfg
}

// HeadHash returns the current HEAD commit hash.
// Acquires a read lock -- do NOT call while holding the write lock.
func (e *Engine) HeadHash() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.headHash
}

// HeadHashLocked returns the current HEAD commit hash.
// Caller must already hold at least a read lock.
func (e *Engine) HeadHashLocked() string {
	return e.headHash
}

// Graph returns the underlying graph. Callers must hold the
// appropriate lock via RLock/RUnlock or Lock/Unlock.
func (e *Engine) Graph() *graph.Graph { return e.graph }

// PropIdx returns the property index.
func (e *Engine) PropIdx() index.PropertyIndex { return e.propIdx }

// VecIdx returns the vector index.
func (e *Engine) VecIdx() index.VectorIndex { return e.vecIdx }

// Embedder returns the embedding provider (may be nil).
func (e *Engine) Embedder() embed.Provider { return e.embedder }

// LLM returns the LLM provider (may be nil if not configured).
func (e *Engine) LLM() llm.Provider { return e.llmProv }

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
// active branch ref. Caller must hold the write lock. Clears the
// accessDirty flag since all in-memory state is now persisted.
//
// Persists indexes (BM25, vector, property) alongside the commit
// so startup can skip expensive rebuilds.
func (e *Engine) Save(message string) (*graph.Commit, error) {
	// Flush buffered vector writes to disk before committing.
	if f, ok := e.vecIdx.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return nil, fmt.Errorf("flush vector index: %w", err)
		}
	}

	// BM25: BboltBM25Index persists to bbolt, not CAS. This block
	// is kept for backward compat with BinaryMarshaler implementations.
	var bm25FullRoot string
	if m, ok := e.bm25Full.(encoding.BinaryMarshaler); ok {
		data, err := m.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal BM25 index: %w", err)
		}
		bm25FullRoot, err = e.store.Write(data)
		if err != nil {
			return nil, fmt.Errorf("write BM25 index: %w", err)
		}
	}

	// Vector index: MmapFlatIndex persists to its own file, not CAS.
	// This block is kept for backward compat with implementations that
	// support BinaryMarshaler (none currently active in v1).
	var vecRoot string
	if m, ok := e.vecIdx.(encoding.BinaryMarshaler); ok {
		vecData, err := m.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal vector index: %w", err)
		}
		vecRoot, err = e.store.Write(vecData)
		if err != nil {
			return nil, fmt.Errorf("write vector index: %w", err)
		}
	}

	// Persist the property index (only for MemoryPropertyIndex).
	var propRoot string
	if memIdx, ok := e.propIdx.(*index.MemoryPropertyIndex); ok {
		propData, err := memIdx.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal property index: %w", err)
		}
		propRoot, err = e.store.Write(propData)
		if err != nil {
			return nil, fmt.Errorf("write property index: %w", err)
		}
	}

	commit, err := e.graph.Save(e.store, e.headHash, message, storage.ProllyConfig{
		TargetChunkSize: e.cfg.Storage.ProllyTargetChunkSize,
		SplitBits:       e.cfg.Storage.ProllySplitBits,
	})
	if err != nil {
		return nil, fmt.Errorf("save commit: %w", err)
	}

	// Persist edge adjacency maps (only for MemoryEdgeStore).
	// BboltEdgeStore persists adjacency directly, no CAS snapshot needed.
	var edgeAdjRoot string
	if edgeAdjData, err := e.graph.MarshalEdgeAdjacency(); err == nil {
		edgeAdjRoot, err = e.store.Write(edgeAdjData)
		if err != nil {
			return nil, fmt.Errorf("write edge adjacency: %w", err)
		}
	}

	// Attach index roots and re-serialize the commit.
	commit.BM25FullRoot = bm25FullRoot
	// BM25MediumRoot and BM25ShortRoot left empty (D12: single BM25 layer).
	commit.VecRoot = vecRoot
	commit.PropRoot = propRoot
	commit.EdgeAdjRoot = edgeAdjRoot
	commit, err = graph.RewriteCommit(e.store, commit)
	if err != nil {
		return nil, fmt.Errorf("rewrite commit with indexes: %w", err)
	}

	headPath := filepath.Join(e.cfg.DataDir, "HEAD")
	if err := AtomicWriteFile(headPath, []byte(commit.Hash), 0o600); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	branch := ActiveBranch(e.cfg.DataDir)
	WriteRef(e.cfg.DataDir, branch, commit.Hash)

	e.headHash = commit.Hash
	e.accessDirty = false
	return commit, nil
}

// MarkAccessDirty records that access metadata has been modified
// in memory but not yet persisted. Caller must hold the write lock.
func (e *Engine) MarkAccessDirty() {
	e.accessDirty = true
}

// IsAccessDirty reports whether access metadata needs flushing.
// Caller must hold at least a read lock.
func (e *Engine) IsAccessDirty() bool {
	return e.accessDirty
}

// FlushAccess saves the current graph state if access metadata is
// dirty. Acquires the write lock internally. Safe to call from a
// background goroutine.
func (e *Engine) FlushAccess() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accessDirty {
		return
	}
	e.Save("access_flush")
}

// RebuildAllIndexes clears and rebuilds all indexes from graph state.
// Caller must hold the write lock.
func (e *Engine) RebuildAllIndexes() {
	// BM25 is still in-memory, so we can replace it.
	// All indexes are now disk-backed (bbolt + mmap). Rebuild adds
	// on top of existing data (idempotent). For a clean rebuild, the
	// caller should clear the indexes first (e.g., delete indexes.db
	// and vec.flat, then re-init).
	rebuildIndexes(e.boltDB, e.graph, e.propIdx, e.vecIdx, e.bm25Full, false, false, false)
	e.searcher = search.New(e.graph, e.propIdx, e.vecIdx, e.bm25Full, e.embedder, e.cfg)
}

// BM25Full returns the BM25 index for content_full.
func (e *Engine) BM25Full() index.BM25Index { return e.bm25Full }

// Close releases resources held by the engine (bbolt DB, mmap files).
// Flushes buffered vectors and closes the bbolt database.
func (e *Engine) Close() error {
	var vecErr error
	if c, ok := e.vecIdx.(interface{ Close() error }); ok {
		vecErr = c.Close()
	}
	if e.boltDB != nil {
		if err := e.boltDB.Close(); err != nil {
			return err
		}
	}
	return vecErr
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
		{"content_medium", "embedding_medium"},
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
// records. A Jaccard verification step prevents false positives on
// long documents with similar structure but different content.
// Caller must hold at least a read lock.
func (e *Engine) CheckDedup(nodeID string) (string, float64) {
	if e.vecIdx.Len() < 2 {
		return "", 0
	}

	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return "", 0
	}

	var vec []float32
	for _, key := range []string{"embedding_full", "embedding_medium", "embedding_short", "embedding_keywords"} {
		if v, ok := n.Properties.GetVector(key); ok {
			vec = v
			break
		}
	}
	if vec == nil {
		return "", 0
	}

	// Request extra candidates since one will be self (skipped) and
	// others may fail the Jaccard guard.
	results := e.vecIdx.Search(vec, 5, nil)
	for _, r := range results {
		if r.NodeID == nodeID {
			continue
		}
		if float64(r.Similarity) >= e.cfg.Dedup.SimilarityThreshold {
			if !e.verifyJaccard(n, r.NodeID) {
				continue
			}
			return r.NodeID, float64(r.Similarity)
		}
	}
	return "", 0
}

// verifyJaccard confirms a cosine-similarity duplicate match by
// checking word-level Jaccard similarity on actual content. Returns
// false (reject) if the texts are too dissimilar, preventing false
// positives on structurally similar but semantically different docs.
func (e *Engine) verifyJaccard(node *graph.Node, candidateID string) bool {
	candidate, ok := e.graph.GetNode(candidateID)
	if !ok {
		return false
	}

	textA := nodeContentText(node)
	textB := nodeContentText(candidate)

	// Skip Jaccard check for very short content where cosine alone
	// is reliable. The false positive problem is specific to long docs.
	if len(textA) < 200 && len(textB) < 200 {
		return true
	}

	tokA := index.Tokenize(textA)
	tokB := index.Tokenize(textB)
	return index.JaccardSimilarity(tokA, tokB) >= dedupJaccardMin
}

// dedupJaccardMin is the minimum Jaccard similarity required to
// confirm a cosine-based duplicate match. Set conservatively low --
// true duplicates easily exceed this even with minor edits.
const dedupJaccardMin = 0.3

// nodeContentText returns the best available text content for a node,
// preferring content_full over content_short.
func nodeContentText(n *graph.Node) string {
	if n == nil {
		return ""
	}
	if s, ok := n.Properties.GetString("content_full"); ok {
		return s
	}
	if s, ok := n.Properties.GetString("content_short"); ok {
		return s
	}
	return ""
}

// PreChunkResult holds section/chunk data and their pre-computed
// embeddings, ready to be applied under the write lock without I/O.
type PreChunkResult struct {
	Sections  []graph.Section // structural sections (preferred)
	Texts     []string        // fallback: dumb chunk texts
	Vectors   [][]float32     // one embedding per section/chunk, may be nil
	Model     string
	ParentVec []float32 // fallback embedding for parent (truncated content)
}

// embedContextWindow returns the effective context window in tokens
// for the configured embedding provider. Priority: config override >
// auto-detected from provider > default.
func (e *Engine) embedContextWindow() int {
	if e.cfg.Embedding.MaxTokens > 0 {
		return e.cfg.Embedding.MaxTokens
	}
	if e.embedder != nil {
		if cw := e.embedder.ContextWindow(); cw > 0 {
			return cw
		}
	}
	return embed.DefaultContextWindow
}

// IsContextLengthError reports whether an embedding error indicates
// the input exceeded the model's context window.
func IsContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context length") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "maximum context") ||
		strings.Contains(msg, "token limit")
}

// maxChunkRetries limits how many times PreChunk will reduce chunk
// size before giving up on embedding.
const maxChunkRetries = 4

// PreChunk determines if content needs sectioning/chunking and pre-embeds
// outside the lock. Uses structural splitting first (SplitSections),
// falls back to dumb chunking (ChunkText) if no structure is detected.
//
// If embedding fails due to context length, PreChunk reduces the chunk
// size and re-splits until the chunks fit the model. This adapts to
// any embedding model's context window without configuration.
//
// Call this BEFORE acquiring the write lock. Returns nil if no splitting needed.
// PreChunk splits content into sections/chunks and pre-embeds them.
// medium is the content_medium text (~1500 chars) used as the preferred
// source for the parent embedding when available (better than truncated
// content_full). summary is content_short (~200 chars), used as fallback.
func (e *Engine) PreChunk(ctx context.Context, content, medium, summary string) *PreChunkResult {
	cfg := e.cfg.Chunking

	// Use the model's context window to determine threshold in chars.
	// Conservative: 3 chars/token to avoid exceeding the real limit.
	ctxTokens := e.embedContextWindow()
	charsPerToken := 3
	thresholdChars := ctxTokens * charsPerToken
	if len(content) <= thresholdChars {
		return nil
	}

	// Try structural splitting first.
	sections := graph.SplitSections(content, cfg.SectionMin, cfg.SectionMax)
	if sections != nil {
		return e.preChunkSections(ctx, content, medium, summary, sections)
	}

	// Fallback to dumb chunking with adaptive sizing.
	return e.preChunkAdaptive(ctx, content, medium, summary, cfg)
}

// preChunkSections embeds structurally-split sections. If any section
// exceeds the model's context, falls back to adaptive dumb chunking.
func (e *Engine) preChunkSections(ctx context.Context, content, medium, summary string, sections []graph.Section) *PreChunkResult {
	result := &PreChunkResult{Sections: sections}

	if e.embedder == nil {
		return result
	}

	texts := make([]string, len(sections))
	for i, s := range sections {
		texts[i] = s.Text
	}

	vecs, err := e.embedder.Embed(ctx, texts)
	if err == nil {
		result.Vectors = vecs
		result.Model = e.embedder.ModelID()
		e.preChunkParent(ctx, result, content, medium, summary)
		return result
	}

	if IsContextLengthError(err) {
		// Sections are too large for the model. Fall back to
		// adaptive dumb chunking which will find the right size.
		return e.preChunkAdaptive(ctx, content, medium, summary, e.cfg.Chunking)
	}

	// Non-context error (network, etc.) -- return sections without embeddings.
	return result
}

// preChunkAdaptive splits content into overlapping chunks and embeds
// them. If chunks exceed the model's context window, reduces chunk
// size and re-splits until they fit.
func (e *Engine) preChunkAdaptive(ctx context.Context, content, medium, summary string, cfg config.ChunkingConfig) *PreChunkResult {
	// Start with chunk_size from config, but cap to the model's
	// context window so initial chunks are likely to fit.
	chunkSize := cfg.ChunkSize
	ctxTokens := e.embedContextWindow()
	if chunkSize > ctxTokens {
		chunkSize = ctxTokens
	}
	overlap := cfg.Overlap

	for attempt := 0; attempt <= maxChunkRetries; attempt++ {
		chunks := graph.ChunkText(content, cfg.Threshold, chunkSize, overlap)
		if len(chunks) == 0 {
			return nil
		}

		result := &PreChunkResult{Texts: chunks}

		if e.embedder == nil {
			return result
		}

		// Test the longest chunk first (canary).
		longest := chunks[0]
		for _, c := range chunks[1:] {
			if len(c) > len(longest) {
				longest = c
			}
		}

		_, err := e.embedder.Embed(ctx, []string{longest})
		if err != nil && IsContextLengthError(err) {
			// Chunks too large -- reduce by 25% and retry.
			chunkSize = chunkSize * 3 / 4
			if chunkSize < 64 {
				chunkSize = 64
			}
			overlap = overlap * 3 / 4
			continue
		}

		// Canary passed (or non-context error). Embed all chunks.
		vecs, err := e.embedder.Embed(ctx, chunks)
		if err == nil {
			result.Vectors = vecs
			result.Model = e.embedder.ModelID()
		}

		e.preChunkParent(ctx, result, content, medium, summary)
		return result
	}

	// Exhausted retries -- return chunks without embeddings.
	chunks := graph.ChunkText(content, cfg.Threshold, chunkSize, overlap)
	return &PreChunkResult{Texts: chunks}
}

// preChunkParent computes the parent embedding for chunked records.
// Preference order: content_medium (purpose-built for the model's
// context window), then content_short, then truncated content_full.
func (e *Engine) preChunkParent(ctx context.Context, result *PreChunkResult, content, medium, summary string) {
	if e.embedder == nil {
		return
	}
	// Prefer content_medium: it's purpose-built to represent the
	// document's identity within the model's context window.
	parentText := medium
	if parentText == "" {
		parentText = summary
	}
	if parentText == "" {
		// Last resort: use first portion of content_full.
		parentText = content
		if len(parentText) > 2000 {
			parentText = parentText[:2000]
		}
	}
	if parentText == "" {
		return
	}
	for attempt := 0; attempt <= maxChunkRetries; attempt++ {
		pvecs, err := e.embedder.Embed(ctx, []string{parentText})
		if err == nil && len(pvecs) > 0 {
			result.ParentVec = pvecs[0]
			return
		}
		if !IsContextLengthError(err) {
			return // non-context error, give up
		}
		parentText = parentText[:len(parentText)/2]
		if len(parentText) == 0 {
			return
		}
	}
}

// ApplyChunks creates section/chunk nodes from a PreChunkResult. Caller
// must hold the write lock. parentProps provides metadata to inherit for
// section nodes. This is fast (no I/O, no embedding calls).
func (e *Engine) ApplyChunks(parentID string, pre *PreChunkResult, parentProps graph.Properties) int {
	if pre == nil {
		return 0
	}

	// Apply parent embedding from the chunk pipeline. When chunking is
	// triggered, the content exceeded the embedding model's context
	// window, so any embedding_full set by preEmbedContent is degraded
	// (silently truncated by the model). The chunk pipeline's ParentVec
	// is computed from the summary or first 2000 chars -- intentionally
	// sized for the model and a better representation of the document's
	// identity. Always prefer it over a degraded pre-embed result.
	if pre.ParentVec != nil {
		if old, hasEmbed := parentProps.GetVector("embedding_full"); hasEmbed {
			e.propIdx.Remove(parentID, "embedding_full", graph.VectorProperty(old))
			e.vecIdx.Remove(parentID)
		}
		prop := graph.VectorProperty(pre.ParentVec)
		e.graph.SetNodeProperty(parentID, "embedding_full", prop)
		e.propIdx.Add(parentID, "embedding_full", prop)
		e.vecIdx.Add(parentID, pre.ParentVec)
		if pre.Model != "" {
			modelProp := graph.StringProperty(pre.Model)
			e.graph.SetNodeProperty(parentID, "embedding_model", modelProp)
			e.propIdx.Add(parentID, "embedding_model", modelProp)
		}
	}

	if len(pre.Sections) > 0 {
		return e.applySections(parentID, pre, parentProps)
	}
	return e.applyLegacyChunks(parentID, pre)
}

// applySections creates section_of nodes with inherited metadata.
func (e *Engine) applySections(parentID string, pre *PreChunkResult, parentProps graph.Properties) int {
	// Metadata keys to inherit from parent.
	inheritKeys := []string{
		"temporality", "confidence", "knowledge_type", "epistemic_status",
		"content_keywords", "source_ref", "processing_status",
	}

	for i, sec := range pre.Sections {
		props := graph.Properties{
			"content_full": graph.StringProperty(sec.Text),
		}

		// Set section heading as content_short.
		if sec.Heading != "" {
			props["content_short"] = graph.StringProperty(sec.Heading)
		} else if len(sec.Text) > 200 {
			props["content_short"] = graph.StringProperty(sec.Text[:200])
		}

		// Inherit parent metadata.
		for _, key := range inheritKeys {
			if v, ok := parentProps[key]; ok {
				props[key] = v
			}
		}

		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}

		node := e.graph.AddNode(props)
		e.graph.AddEdge(node.ID, parentID, "section_of", 1.0, nil)
		e.IndexNode(node.ID, sec.Text, vec)

		if vec != nil && pre.Model != "" {
			e.SetProp(node.ID, "embedding_model", graph.StringProperty(pre.Model))
		}
	}

	return len(pre.Sections)
}

// applyLegacyChunks creates chunk_of nodes (backward-compatible dumb chunks).
func (e *Engine) applyLegacyChunks(parentID string, pre *PreChunkResult) int {
	for i, chunkText := range pre.Texts {
		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}

		chunkNode := e.graph.AddNode(graph.Properties{
			"content_full": graph.StringProperty(chunkText),
		})
		e.graph.AddEdge(chunkNode.ID, parentID, "chunk_of", 1.0, nil)
		e.IndexNode(chunkNode.ID, chunkText, vec)

		if vec != nil && pre.Model != "" {
			e.SetProp(chunkNode.ID, "embedding_model", graph.StringProperty(pre.Model))
		}
	}

	return len(pre.Texts)
}

// ChunkIfNeeded splits a node's content into chunk child nodes if
// it exceeds the configured threshold. Caller must hold the write lock.
// DEPRECATED: Use PreChunk + ApplyChunks to avoid embedding under lock.
func (e *Engine) ChunkIfNeeded(ctx context.Context, nodeID string) (int, error) {
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return 0, fmt.Errorf("node %s not found", nodeID)
	}

	contentProp, ok := n.Properties["content_full"]
	if !ok {
		return 0, nil
	}

	medium, _ := n.Properties.GetString("content_medium")
	pre := e.PreChunk(ctx, contentProp.String(), medium, "")
	return e.ApplyChunks(nodeID, pre, n.Properties), nil
}

// SetProp sets a property on a node and updates the property index.
// Caller must hold the write lock.
// IndexNode populates all indexes for a node that has already been
// added to the graph. Handles PropIdx (all properties), BM25 (if
// content is non-empty), and VecIdx (if vec is non-nil). Call this
// once after AddNode instead of manually updating each index.
// Caller must hold the write lock.
func (e *Engine) IndexNode(nodeID, content string, vec []float32) {
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return
	}
	for k, v := range n.Properties {
		e.propIdx.Add(nodeID, k, v)
	}
	// Add to BM25 index (D12: single layer, content_full only).
	if content != "" {
		e.bm25Full.Add(nodeID, content)
	}
	if vec != nil {
		e.vecIdx.Add(nodeID, vec)
		// Set embedding_full as a node property so it's persisted and
		// visible to inspection/export. The vector index is a derived
		// structure; the property is the source of truth.
		prop := graph.VectorProperty(vec)
		e.graph.SetNodeProperty(nodeID, "embedding_full", prop)
		e.propIdx.Add(nodeID, "embedding_full", prop)
	}
}

func (e *Engine) SetProp(nodeID, key string, val graph.Property) {
	if n, ok := e.graph.GetNode(nodeID); ok {
		if old, ok := n.Properties[key]; ok {
			e.propIdx.Remove(nodeID, key, old)
		}
	}
	e.graph.SetNodeProperty(nodeID, key, val)
	e.propIdx.Add(nodeID, key, val)
}

// SetContentProp updates a string property and refreshes the BM25
// index if the property is content_full. Use this instead of SetProp
// when changing content fields to keep BM25 in sync (D12: single
// BM25 layer, content_full only). Caller must hold the write lock.
func (e *Engine) SetContentProp(nodeID, key, content string) {
	e.SetProp(nodeID, key, graph.StringProperty(content))
	if key == "content_full" {
		e.bm25Full.Remove(nodeID)
		e.bm25Full.Add(nodeID, content)
	}
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

// rebuildIndexes populates indexes from graph state. Each *Loaded flag
// indicates that the corresponding index was restored from a persisted
// snapshot and should be skipped. When all are true, this is a no-op.
func rebuildIndexes(db *bolt.DB, g graph.NodeReader, propIdx index.PropertyIndex, vecIdx index.VectorIndex, bm25Full index.BM25Index, bm25FullLoaded, vecLoaded, propLoaded bool) {
	if bm25FullLoaded && vecLoaded && propLoaded {
		return
	}

	// Batch all bbolt writes in a single transaction. Both propIdx and
	// bm25Full may share the same bbolt DB; opening separate write
	// transactions would deadlock. Open one transaction and share it.
	type batchSetter interface {
		SetBatch(tx *bolt.Tx)
		ClearBatch()
	}
	var setters []batchSetter
	if ps, ok := propIdx.(batchSetter); ok && !propLoaded {
		setters = append(setters, ps)
	}
	if bs, ok := bm25Full.(batchSetter); ok && !bm25FullLoaded {
		setters = append(setters, bs)
	}
	if len(setters) > 0 && db != nil {
		db.Update(func(tx *bolt.Tx) error {
			for _, s := range setters {
				s.SetBatch(tx)
			}
			defer func() {
				for _, s := range setters {
					s.ClearBatch()
				}
			}()
			rebuildIndexesInner(g, propIdx, vecIdx, bm25Full, bm25FullLoaded, vecLoaded, propLoaded)
			return nil
		})
		return
	}
	rebuildIndexesInner(g, propIdx, vecIdx, bm25Full, bm25FullLoaded, vecLoaded, propLoaded)
}


func rebuildIndexesInner(g graph.NodeReader, propIdx index.PropertyIndex, vecIdx index.VectorIndex, bm25Full index.BM25Index, bm25FullLoaded, vecLoaded, propLoaded bool) {
	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		if !propLoaded {
			for k, v := range n.Properties {
				propIdx.Add(n.ID, k, v)
			}
		}
		if !vecLoaded {
			for _, embKey := range []string{"embedding_full", "embedding_medium", "embedding_short", "embedding_keywords"} {
				if v, ok := n.Properties.GetVector(embKey); ok {
					vecIdx.Add(n.ID, v)
					break
				}
			}
		}
		if !bm25FullLoaded {
			if text, ok := n.Properties.GetString("content_full"); ok {
				bm25Full.Add(n.ID, text)
			}
		}
	}
}
