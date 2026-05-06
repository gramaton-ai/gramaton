// Package core provides the shared engine that manages the knowledge
// graph, indexes, embedding, and persistence. Both the HTTP server
// and CLI thin client operate through this engine. The engine is
// safe for concurrent use via an internal RWMutex.
package core

import (
	"context"
	"encoding"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/gramaton-ai/gramaton/chunking"
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/dedup"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/jobs"
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
	boltDB   *bolt.DB // shared bbolt database; closed in Close after indexes
	indexes  *indexSet
	prov     *providers
	searcher *searcherSubsystem
	headHash string

	// jobStore is the F1 async-operation tracking store. Owns its own
	// jobs.db file (separate from indexes.db). nil if engine init
	// failed before jobStore opened. Close in Engine.Close before
	// boltDB to keep error reporting tidy.
	jobStore *jobs.Store

	// jobSweepCancel stops the background TTL-based GC sweeper for
	// terminal jobs. Set to a real cancel func when the sweeper is
	// running; nil when SweepInterval is 0 (sweeper disabled). The
	// sweeper goroutine respects this ctx and exits on Engine.Close.
	jobSweepCancel context.CancelFunc
	jobSweepDone   chan struct{} // closed when the sweeper goroutine exits

	// accessDirty is set when access metadata (access_count,
	// last_accessed, activation_boost) has been recorded in memory
	// but not yet persisted to disk. The server flushes this
	// periodically rather than saving on every read.
	accessDirty bool

	// accessFlushFailures counts consecutive FlushAccess save
	// failures. Reset to 0 on the next successful flush. Used to
	// dedup log noise when a stuck disk causes the 30s flusher to
	// retry indefinitely; we log the first failure at Warn and then
	// every 10th attempt at Error rather than every attempt.
	accessFlushFailures int

	// searchSnapshots caches paginated search result sets so cursor
	// calls slice into a stable matched-set without re-running the
	// query. TTL configured via cfg.Search.Pagination.SnapshotTTL.
	// Lifetime tied to the engine; Close stops the eviction loop.
	searchSnapshots *SnapshotStore
}

// EngineOption configures an engine at construction time. Options are
// applied after default initialization, overriding config-derived values.
// This is the only supported way to inject dependencies -- the engine's
// wiring (indexes, embedder) is immutable after construction.
type EngineOption func(*Engine)

// WithEmbedder overrides the embedding provider. Use in tests to inject
// a mock embedder without requiring a real Ollama/API endpoint.
func WithEmbedder(p embed.Provider) EngineOption {
	return func(e *Engine) { e.prov.embedder = p }
}

// WithLLM overrides the LLM provider. Use in tests to inject a mock
// LLM without requiring a real API key.
func WithLLM(p llm.Provider) EngineOption {
	return func(e *Engine) { e.prov.llm = p }
}

// WithVectorIndex overrides the vector index. Use in tests to inject
// an in-memory FlatIndex instead of the disk-backed MmapFlatIndex.
// When set, the engine skips creating/opening the mmap vector file.
func WithVectorIndex(v index.VectorIndex) EngineOption {
	return func(e *Engine) { e.indexes.vecIdx = v }
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
	return loadEngineWithOptions(cfgDir, globalCfgDirs, opts, false)
}

// loadEngineWithOptions is the internal body of LoadEngineWithOptions.
// skipFormatCheck bypasses the store-format gate and is reserved for
// the `gramaton migrate` code path -- no other caller should set it.
// A v1 store can only be opened by migration; everything else must
// refuse to boot so temporal queries never run against unindexed
// history.
func loadEngineWithOptions(cfgDir string, globalCfgDirs []string, opts []EngineOption, skipFormatCheck bool) (*Engine, error) {
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

	if !skipFormatCheck {
		if err := CheckFormatVersion(cfg.DataDir); err != nil {
			return nil, fmt.Errorf("store format: %w", err)
		}
	}

	// Open the shared bbolt database for property index and edge store.
	boltPath := filepath.Join(cfg.DataDir, "indexes.db")
	boltDB, err := bolt.Open(boltPath, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}
	// Deferred cleanup-on-failure: every resource opened below is
	// registered here so that any subsequent error returns a clean
	// state. Previously some paths (g.Load, embed.New, mmap index
	// creation) leaked boltDB or other handles depending on which
	// step failed.
	success := false
	cleanups := []func(){
		func() { boltDB.Close() },
	}
	defer func() {
		if success {
			return
		}
		// Run in reverse order (LIFO) so dependents close before
		// their backing store.
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	idx, err := newIndexSet(boltDB, cfg)
	if err != nil {
		return nil, err
	}

	g := graph.NewWithCapacity(graph.DefaultCacheCapacity, graph.WithEdgeStore(idx.edgeStore))

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

	prov, err := newProviders(cfg)
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}

	e := &Engine{
		cfg:      cfg,
		store:    s,
		graph:    g,
		boltDB:   boltDB,
		indexes:  idx,
		prov:     prov,
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
	// vector index.
	vecCleanup, err := idx.openDefaultVecIdx(cfg)
	if err != nil {
		return nil, err
	}
	if vecCleanup != nil {
		cleanups = append(cleanups, vecCleanup)
	}

	// Try to load persisted indexes from commit; each populated index
	// is skipped during the rebuild walk.
	idx.rebuildPrimaryIfMissing(g)

	// Build searcher after all indexes are finalized.
	e.searcher = &searcherSubsystem{}
	e.searcher.rebuild(g, idx.propIdx, idx.vecIdx, idx.bm25Full, idx.secIdx, e.prov.embedder, e.prov.llm, cfg)

	// Open the F1 jobs store. Separate bbolt file from indexes.db so
	// it survives backup/restore (indexes.db is excluded as derived
	// state). Restart recovery: any in-flight job from a prior run
	// is flipped to failed/server_restart before any HTTP listener
	// can accept calls.
	jobsPath := filepath.Join(cfg.DataDir, "jobs.db")
	jobStore, err := jobs.New(jobsPath)
	if err != nil {
		return nil, fmt.Errorf("open jobs store: %w", err)
	}
	cleanups = append(cleanups, func() { _ = jobStore.Close() })
	if err := recoverInFlightJobs(jobStore); err != nil {
		return nil, fmt.Errorf("jobs restart recovery: %w", err)
	}
	e.jobStore = jobStore

	// Spawn the GC sweeper goroutine if enabled. SweepInterval=0
	// disables the sweeper; jobs then accumulate until manually
	// pruned. We use a dedicated context so Engine.Close can cancel
	// it cleanly; jobSweepDone closes when the goroutine exits.
	if cfg.Jobs.SweepInterval > 0 {
		sweepCtx, cancel := context.WithCancel(context.Background())
		e.jobSweepCancel = cancel
		e.jobSweepDone = make(chan struct{})
		go runJobSweeper(sweepCtx, e.jobSweepDone, jobStore, cfg.Jobs)
	}

	// Search snapshot store for paginated gramaton_search. Owns its
	// own background eviction goroutine; Close stops it.
	e.searchSnapshots = NewSnapshotStore(cfg.Search.Pagination.SnapshotTTL)
	cleanups = append(cleanups, func() { e.searchSnapshots.Stop() })

	success = true // disarm the deferred cleanup
	return e, nil
}

// recoverInFlightJobs flips any pending/running job from a prior
// run to failed with reason "server_restart". Called during engine
// init BEFORE the HTTP listener is bound, so callers cannot observe
// stale running jobs after a crash + restart.
//
// Per-job failures are logged at Warn but don't abort recovery —
// one corrupt or unwriteable job entry must not prevent the
// engine from booting. Only fails the engine if listing the
// in-flight set itself errors (suggests a deeper bbolt problem).
func recoverInFlightJobs(s *jobs.Store) error {
	inflight, err := s.ListInFlight()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var recovered, failed int
	for _, j := range inflight {
		j.Status = jobs.StatusFailed
		j.FailureReason = "server_restart"
		j.CompletedAt = now
		if err := s.Update(j); err != nil {
			slog.Warn("failed to recover individual job",
				"component", "engine", "job_id", j.ID, "err", err)
			failed++
			continue
		}
		recovered++
	}
	if recovered > 0 || failed > 0 {
		slog.Info("in-flight job recovery complete",
			"component", "engine",
			"recovered", recovered, "failed", failed)
	}
	return nil
}

// runJobSweeper runs the periodic TTL-based GC sweeper. Exits on
// ctx cancellation. Closes done when it returns so Engine.Close
// can wait for clean shutdown.
func runJobSweeper(ctx context.Context, done chan struct{}, s *jobs.Store, cfg config.JobsConfig) {
	defer close(done)
	ret := jobs.RetentionPolicy{
		Completed: cfg.Retention.Completed,
		Failed:    cfg.Retention.Failed,
		Cancelled: cfg.Retention.Cancelled,
	}
	t := time.NewTicker(cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deleted, err := s.RunGC(ctx, time.Now().UTC(), ret)
			if err != nil {
				// Ctx-cancellation isn't an "error" worth flagging —
				// it's just shutdown. RunGC returns ctx.Err() when
				// cancelled mid-walk; loop exits via the next case.
				if ctx.Err() != nil {
					return
				}
				slog.Warn("job sweep error",
					"component", "engine", "err", err)
				continue
			}
			if deleted > 0 {
				slog.Info("job sweep deleted terminal jobs",
					"component", "engine", "count", deleted)
			}
		}
	}
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

// SwapGraph replaces the engine's graph with g. The replacement is
// a single pointer assignment; the old graph becomes GC-eligible as
// soon as no one retains a reference. Caller MUST hold the write
// lock (Lock/Unlock).
//
// This is the primitive for "load a new state off-lock, then apply
// under lock" -- callers construct a fresh *graph.Graph via
// graph.NewWithCapacity(cap, graph.WithEdgeStore(engine.EdgeStore()))
// + Load(store, hash) outside the lock, then take the write lock,
// call SwapGraph, write HEAD/refs, call RebuildAllIndexes, release.
// BranchCheckout/Merge use this to keep the expensive parse off-lock.
//
// IMPORTANT: the new graph must share the engine's BboltEdgeStore.
// If you build it with the default graph.New() it gets a fresh
// MemoryEdgeStore and any subsequent edge writes silently bypass
// bbolt persistence. Use EdgeStore() to grab the engine's store
// and inject via graph.WithEdgeStore.
//
// Incremental-commit state (lastNodeTreeRoot/lastEdgeTreeRoot) is
// carried on the graph itself and was set by Load, so subsequent
// saves on the swapped-in graph commit correctly.
func (e *Engine) SwapGraph(g *graph.Graph) { e.graph = g }

// EdgeStore returns the engine's persistent edge store. Used by
// SwapGraph callers (BranchCheckout/Merge) to construct a
// replacement graph that shares the engine's BboltEdgeStore.
func (e *Engine) EdgeStore() *graph.BboltEdgeStore { return e.indexes.edgeStore }

// PropIdx returns the property index.
func (e *Engine) PropIdx() index.PropertyIndex { return e.indexes.propIdx }

// VecIdx returns the vector index.
func (e *Engine) VecIdx() index.VectorIndex { return e.indexes.vecIdx }

// Embedder returns the embedding provider (may be nil).
func (e *Engine) Embedder() embed.Provider { return e.prov.embedder }

// LLM returns the LLM provider (may be nil if not configured).
func (e *Engine) LLM() llm.Provider { return e.prov.llm }

// WrapLLM replaces the engine's LLM provider with the value returned
// by fn (passed the current provider), then rebuilds the searcher
// subsystem so search-time consumers (reranker, decompose) pick up
// the new reference. No-op when the engine has no LLM configured.
//
// Intended for one-time setup of middleware wrappers — e.g. wrapping
// with llm.Metered so every consumer records into the UsageTracker
// instead of bypassing it. Takes the write lock internally; safe at
// construction because no other callers hold it yet. Do NOT use at
// runtime with RPCs in flight — the wrap would block until every
// reader released, then the rebuild would invalidate their
// searcher references.
//
// fn is invoked under the write lock. It MUST be fast and MUST NOT
// perform I/O (network calls, disk writes). Stick to struct
// composition / wrapper construction.
func (e *Engine) WrapLLM(fn func(llm.Provider) llm.Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prov.llm == nil {
		return
	}
	e.prov.llm = fn(e.prov.llm)
	e.searcher.rebuild(e.graph, e.indexes.propIdx, e.indexes.vecIdx, e.indexes.bm25Full, e.indexes.secIdx, e.prov.embedder, e.prov.llm, e.cfg)
}

// Searcher returns the search tool.
func (e *Engine) Searcher() *search.Tool { return e.searcher.tool }

// Store returns the storage backend.
func (e *Engine) Store() *storage.Store { return e.store }

// RLock acquires a read lock. Use for read operations (search,
// inspect, explore, etc.). Multiple readers can hold the lock
// concurrently.
func (e *Engine) RLock() { e.mu.RLock() }

// RUnlock releases the read lock.
func (e *Engine) RUnlock() { e.mu.RUnlock() }

// TryRLock attempts to acquire a read lock without blocking. Returns
// true on success (caller MUST RUnlock) and false if the write lock
// is held by any goroutine (including the caller -- RWMutex is not
// reentrant). Use when a background refresh should be skipped rather
// than blocked when the engine is in a write phase.
func (e *Engine) TryRLock() bool { return e.mu.TryRLock() }

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
//
// actions is the optional D3 structured action descriptor list.
// Empty variadic = no structured actions (commit still filterable
// via Message-prefix matching for pre-D3 consumers). Cluster
// migration to explicit action emission lands incrementally per
// the Phase 3 build plan.
func (e *Engine) Save(message string, actions ...graph.CommitAction) (*graph.Commit, error) {
	// Flush buffered vector writes to disk before committing.
	if f, ok := e.indexes.vecIdx.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return nil, fmt.Errorf("flush vector index: %w", err)
		}
	}

	// BM25: BboltBM25Index persists to bbolt, not CAS. This block
	// is kept for backward compat with BinaryMarshaler implementations.
	var bm25FullRoot string
	if m, ok := e.indexes.bm25Full.(encoding.BinaryMarshaler); ok {
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
	if m, ok := e.indexes.vecIdx.(encoding.BinaryMarshaler); ok {
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
	if memIdx, ok := e.indexes.propIdx.(*index.MemoryPropertyIndex); ok {
		propData, err := memIdx.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal property index: %w", err)
		}
		propRoot, err = e.store.Write(propData)
		if err != nil {
			return nil, fmt.Errorf("write property index: %w", err)
		}
	}

	// PrepareCommit persists nodes/edges/trees and returns a *Commit
	// with NodeTreeRoot+EdgeTreeRoot populated but the commit chunk
	// not yet written. We attach engine-managed index roots before
	// the single chunk write in WriteCommit -- this avoids the
	// per-save orphan chunk that the prior write+rewrite flow
	// produced. (P2-02 sub-fix 6.)
	commit, err := e.graph.PrepareCommit(e.store, e.headHash, message, actions, storage.ProllyConfig{
		TargetChunkSize: e.cfg.Storage.ProllyTargetChunkSize,
		SplitBits:       e.cfg.Storage.ProllySplitBits,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare commit: %w", err)
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

	// Attach engine-managed index roots, then write the single
	// commit chunk.
	commit.BM25FullRoot = bm25FullRoot
	// BM25MediumRoot and BM25ShortRoot left empty (D12: single BM25 layer).
	commit.VecRoot = vecRoot
	commit.PropRoot = propRoot
	commit.EdgeAdjRoot = edgeAdjRoot
	commit, err = e.graph.WriteCommit(e.store, commit)
	if err != nil {
		return nil, fmt.Errorf("write commit: %w", err)
	}

	// Index the commit's timestamp for D7 temporal queries. Fires on
	// every Save so new commits are always reachable by CommitAt /
	// CommitsBetween without walking the parent chain. A rare failure
	// here leaves the commit saved but unindexed; `gramaton migrate`
	// is idempotent and can backfill gaps.
	if e.indexes.tsIndex != nil {
		if err := e.indexes.tsIndex.Put(commit); err != nil {
			return nil, fmt.Errorf("write timestamp index: %w", err)
		}
	}

	headPath := filepath.Join(e.cfg.DataDir, "HEAD")
	if err := AtomicWriteFile(headPath, []byte(commit.Hash), 0o600); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	branch := ActiveBranch(e.cfg.DataDir)
	if err := WriteRef(e.cfg.DataDir, branch, commit.Hash); err != nil {
		return nil, fmt.Errorf("write ref %s: %w", branch, err)
	}

	e.headHash = commit.Hash
	e.accessDirty = false
	return commit, nil
}

// MarkAccessDirty records that access metadata has been modified
// in memory but not yet persisted. Caller must hold the write lock.
func (e *Engine) MarkAccessDirty() {
	e.accessDirty = true
}

// SaveOrLog wraps Save for callers that have no meaningful path to
// surface a persistence failure (background flushes, fire-and-forget
// curation writes, mid-loop saves). Errors are logged at Error level
// with the message label so operators can see silent persistence
// failures that would otherwise vanish.
//
// Callers that CAN handle the error (HTTP handlers returning 5xx,
// import operations that should abort) MUST use Save directly.
//
// Caller must hold the write lock. Accepts variadic CommitAction
// values matching Save's signature so curation passes can emit
// per-record action descriptors alongside the cycle's batch save.
func (e *Engine) SaveOrLog(message string, actions ...graph.CommitAction) {
	if _, err := e.Save(message, actions...); err != nil {
		slog.Error("save failed",
			"component", "engine",
			"message", message,
			"err", err)
	}
}

// FlushAccess saves the current graph state if access metadata is
// dirty. Acquires the write lock internally. Safe to call from a
// background goroutine.
//
// Save failures are counted and logged with dedup: first failure at
// Warn, suppressed at Debug for runs 2-9, every 10th failure at
// Error with the consecutive count. A stuck disk under the 30s
// flusher would otherwise emit one Error log + full err every 30s
// indefinitely. Counter resets on the next success.
func (e *Engine) FlushAccess() {
	// Lifecycle steps stay at DEBUG -- this fires every 30s under
	// normal operation. The end-of-flush INFO captures the only
	// state change a user cares about: a save actually happened.
	slog.Debug("access flush: acquiring write lock", "component", "engine")
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accessDirty {
		slog.Debug("access flush: nothing dirty, skipping", "component", "engine")
		return
	}
	slog.Debug("access flush: saving", "component", "engine")
	start := time.Now()
	// access_flush is a periodic background save of dirty access
	// metadata (last_accessed, access_count). It's not a logical
	// mutation -- the records were already mutated; this just
	// persists the in-memory state. Emitting an action would be
	// noise: every record touched by any read would generate a
	// curation-shaped log entry.
	//gramaton:saveactions:exempt
	if _, err := e.Save("access_flush"); err != nil {
		e.accessFlushFailures++
		switch {
		case e.accessFlushFailures == 1:
			slog.Warn("access flush: save failed",
				"component", "engine",
				"err", err)
		case e.accessFlushFailures%10 == 0:
			slog.Error("access flush: save still failing",
				"component", "engine",
				"consecutive_failures", e.accessFlushFailures,
				"err", err)
		default:
			slog.Debug("access flush: save failed (suppressed)",
				"component", "engine",
				"consecutive_failures", e.accessFlushFailures,
				"err", err)
		}
		return
	}
	if e.accessFlushFailures > 0 {
		slog.Info("access flush: save recovered",
			"component", "engine",
			"prior_failures", e.accessFlushFailures)
		e.accessFlushFailures = 0
	}
	slog.Info("access flush: done", "component", "engine", "save_ms", time.Since(start).Milliseconds())
}

// RebuildAllIndexes rebuilds all indexes from graph state and refreshes
// the searcher to point at them. Idempotent. Caller must hold the
// write lock.
func (e *Engine) RebuildAllIndexes() {
	e.indexes.rebuildAll(e.graph)
	e.searcher.rebuild(e.graph, e.indexes.propIdx, e.indexes.vecIdx, e.indexes.bm25Full, e.indexes.secIdx, e.prov.embedder, e.prov.llm, e.cfg)
}

// BM25Full returns the BM25 index for content_full.
func (e *Engine) BM25Full() index.BM25Index { return e.indexes.bm25Full }

// SecIdx returns the secondary index (time, edge counts, field existence).
// May be nil in tests that don't create one.
func (e *Engine) SecIdx() *index.BboltSecondaryIndex { return e.indexes.secIdx }

// CollCache returns the collection membership cache.
// May be nil in tests that don't create one.
func (e *Engine) CollCache() *index.BboltCollectionCache { return e.indexes.collCache }

// TSIndex returns the commit-timestamp index (D7). Used by temporal
// queries (Phase 2+) and the `gramaton migrate` backfill path.
func (e *Engine) TSIndex() *graph.TSIndex { return e.indexes.tsIndex }

// BatchIndexWrites executes fn within a single bbolt write transaction
// shared across all bbolt-backed indexes (PropIdx, BM25, SecIdx,
// EdgeStore). Use this when creating many nodes at once (e.g.,
// observation extraction) to avoid per-node fsync overhead.
// Caller must hold the engine write lock.
//
// Returns any error from the underlying bbolt transaction. A non-nil
// return means the entire batch was rolled back; index writes inside
// fn did not persist. Callers must check the error -- silently
// ignoring it loses every write inside the closure.
//
// Prefer WithWriteBatch for write-phase callers that also need Lock +
// Save. BatchIndexWrites remains the right call for code paths that
// are already under the write lock and want to batch a sub-section
// of their work. (P2-06: fn now receives a *WriteSession, matching
// the WithWriteBatch closure shape.)
func (e *Engine) BatchIndexWrites(fn func(*WriteSession)) error {
	return e.indexes.batch(e, func(ws *WriteSession) error {
		fn(ws)
		return nil
	})
}

// WithWriteBatch runs fn under the engine write lock with bbolt index
// writes batched into a single transaction, then Saves under the label
// `message` when fn reports mutations.
//
// Standardises the three-step write-phase recipe (Lock -> batched
// writes -> Save) so callers don't drift on error handling, logging,
// or the "skip save when nothing changed" gate. Caller MUST NOT hold
// the engine lock. Caller is responsible for short-circuiting *before*
// calling when there is no work to do -- WithWriteBatch always takes
// the write lock.
//
// fn returns (mutated, err). When err is non-nil, Save is skipped and
// the error is wrapped with the message label. When mutated is false,
// Save is skipped (no-op commits waste bbolt fsync + HEAD writes).
// When both are clean, a single Save fires with the message as the
// commit label.
//
// Logs batch_ms and save_ms at Info so lock-hold duration is
// observable per phase. fn receives a *WriteSession with the
// session's tx and companion caches; call ws.SetProp, ws.AddEdge,
// ws.IndexNode etc. inside to thread tx through the bbolt-backed
// indexes. (T-06, P2-06.)
func (e *Engine) WithWriteBatch(message string, fn func(*WriteSession) (mutated bool, err error)) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	lockStart := time.Now()
	var mutated bool
	var fnErr error
	var collectedActions []graph.CommitAction
	batchErr := e.indexes.batch(e, func(ws *WriteSession) error {
		mutated, fnErr = fn(ws)
		collectedActions = ws.actions
		return fnErr
	})
	batchDur := time.Since(lockStart)

	if fnErr != nil {
		return fmt.Errorf("withwritebatch %q: %w", message, fnErr)
	}
	if batchErr != nil {
		return fmt.Errorf("withwritebatch %q: batch: %w", message, batchErr)
	}

	if !mutated {
		slog.Debug("write batch complete (no-op)",
			"component", "engine",
			"message", message,
			"batch_ms", batchDur.Milliseconds())
		return nil
	}

	saveStart := time.Now()
	if _, err := e.Save(message, collectedActions...); err != nil {
		return fmt.Errorf("withwritebatch %q: save: %w", message, err)
	}
	slog.Info("write batch complete",
		"component", "engine",
		"message", message,
		"batch_ms", batchDur.Milliseconds(),
		"save_ms", time.Since(saveStart).Milliseconds())
	return nil
}

// Close releases resources held by the engine (bbolt DB, mmap files).
// Flushes buffered vectors and closes the bbolt database.
// Returns the first error encountered; all resources are closed regardless.
func (e *Engine) Close() error {
	var firstErr error

	// Stop the search-snapshot eviction loop; idempotent.
	if e.searchSnapshots != nil {
		e.searchSnapshots.Stop()
		e.searchSnapshots = nil
	}

	// Stop the job sweeper first so it can't fire mid-close. The
	// goroutine respects sweepCtx and exits cleanly; we wait for
	// it via jobSweepDone. Idempotent: cancel func is set to nil
	// after first call so a second Close skips the wait.
	if e.jobSweepCancel != nil {
		e.jobSweepCancel()
		<-e.jobSweepDone
		e.jobSweepCancel = nil
		e.jobSweepDone = nil
	}

	// Each Close on a sub-resource is idempotent in its own
	// implementation (jobs.Store.Close, bbolt.Close); we still
	// nil our refs to avoid second-call work.
	if e.jobStore != nil {
		if err := e.jobStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.jobStore = nil
	}
	if e.indexes != nil {
		if err := e.indexes.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.indexes = nil
	}
	if e.boltDB != nil {
		if err := e.boltDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.boltDB = nil
	}
	return firstErr
}

// JobStore returns the engine's job store. F1 capture_batch and
// future async operations use this for tracking. Returns nil if
// the engine was constructed in a way that bypassed jobs init
// (test harnesses).
func (e *Engine) JobStore() *jobs.Store {
	return e.jobStore
}

// SearchSnapshots returns the engine's search snapshot store. Used
// by api.Search to populate snapshots after a fresh query and to
// look them up on cursor-paginated calls. May be nil only if the
// engine was constructed in a way that bypassed standard init.
func (e *Engine) SearchSnapshots() *SnapshotStore {
	return e.searchSnapshots
}

// CheckDedup checks if a node's embedding is too similar to existing
// records. Delegates to dedup.Check; the Engine method exists to
// provide the natural entry point and preserve the historical API.
// Caller must hold at least a read lock.
func (e *Engine) CheckDedup(nodeID string) (string, float64) {
	return dedup.Check(e.graph, e.indexes.vecIdx, e.cfg.Dedup, nodeID)
}

// PreChunkResult is an alias for chunking.Result, preserved so
// callers that reference the historical name continue to compile.
type PreChunkResult = chunking.Result

// IsContextLengthError reports whether an embedding error indicates
// the input exceeded the model's context window. Delegates to the
// chunking package which owns the detection logic.
func IsContextLengthError(err error) bool {
	return chunking.IsContextLengthError(err)
}

// PreChunk determines whether content needs splitting and pre-embeds
// the pieces. Runs OUTSIDE the engine lock (embedding is I/O-bound);
// call ApplyChunks with the result under the write lock. Returns nil
// when content fits in a single embedding.
func (e *Engine) PreChunk(ctx context.Context, content, medium, summary string) *PreChunkResult {
	return chunking.PreChunk(ctx, e.prov.embedder, e.cfg.Chunking, e.cfg.Embedding, content, medium, summary)
}

// ApplyChunks creates section/chunk nodes from pre. Caller must hold
// the engine write lock.
func (e *Engine) ApplyChunks(parentID string, pre *PreChunkResult, parentProps graph.Properties) int {
	return chunking.Apply(e, parentID, pre, parentProps)
}

// IndexNode populates all indexes for a node already added to the
// graph. When vec is non-nil it is also written back as the
// embedding_full property -- the vector index is a derived structure
// and the property is the source of truth. The cross-index update is
// delegated to indexSet.applyToNode so a future index gets picked up
// automatically. Caller must hold the write lock.
func (e *Engine) IndexNode(nodeID, content string, vec []float32) {
	if vec != nil {
		e.graph.SetNodeProperty(nodeID, "embedding_full", graph.VectorProperty(vec))
	}
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return
	}
	e.indexes.applyToNode(n, content, vec)
}

// SetProp sets a property on a node and updates the property index.
// Caller must hold the write lock.
func (e *Engine) SetProp(nodeID, key string, val graph.Property) {
	e.indexes.setProp(e.graph, nodeID, key, val)
}

// SetContentProp updates a string property and refreshes the BM25
// index if the property is content_full. Use this instead of SetProp
// when changing content fields to keep BM25 in sync (D12: single
// BM25 layer, content_full only). Caller must hold the write lock.
func (e *Engine) SetContentProp(nodeID, key, content string) {
	e.indexes.setContentProp(e.graph, nodeID, key, content)
}

// NodeCount returns the number of nodes. Acquires a read lock.
func (e *Engine) NodeCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph.NodeCount()
}

// EdgeCount returns the number of edges. Acquires a read lock.
func (e *Engine) EdgeCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph.EdgeCount()
}

