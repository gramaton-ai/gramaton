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
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/gramaton-ai/gramaton/chunking"
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/jobs"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/search"
	"github.com/gramaton-ai/gramaton/similarity"
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

	// recentWrites is a bounded ring of recently committed
	// embedding-bearing nodes, used by the save-guard delta re-scan
	// to close the scan-to-lock window (the off-lock similarity scan
	// cannot see records that commit between the scan and the write
	// lock). Guarded by mu: appends happen under the write lock at
	// node-creation sites; reads happen under the write lock in the
	// delta re-scan. recentSeq is monotonically increasing and never
	// reused.
	recentWrites []recentWrite
	recentSeq    uint64

	// volatile disables durability syncs on every write surface
	// (blob store, bbolt commits, HEAD/ref updates, jobs store,
	// mmap index). Settable ONLY via WithVolatileStorage; never
	// from config. Test fixtures use it -- see the option's doc.
	volatile bool

	// readOnly marks the store as logically frozen: Save and
	// WithWriteBatch reject with ErrStoreReadOnly, SaveOrLog
	// short-circuits quietly -- all before any work.
	// Set by openFiles from the STORE manifest, or forced via
	// WithReadOnly. Derived local caches (indexes.db, vec.flat,
	// jobs.db) stay writable: they are rebuilt from the graph at
	// startup by design, so no index-rebuild or file-open path is
	// gated on this flag. Atomic because WithWriteBatch checks it
	// before taking the engine lock while Restore's OpenFiles may
	// rewrite it under the write lock.
	readOnly atomic.Bool

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

	// sidecarDB is the non-versioned bookkeeping bbolt file
	// (sidecar.db): access metadata now, the changelog index next.
	// Separate from indexes.db because backup excludes indexes.db as
	// rebuildable while sidecar contents are primary. accessIdx is
	// the access-metadata sidecar over it; reads update it directly,
	// so reads produce ZERO commits (the access_flush machinery this
	// replaces committed read churn on a timer).
	sidecarDB *bolt.DB
	accessIdx *index.BboltAccessIndex
	// changelog is the per-node logical-version index (also in
	// sidecar.db). Appended at Save after the HEAD write; drift is
	// repaired by the boot gap walk.
	changelog *index.BboltChangelog
	// anc memoizes the current head's ancestor set for as_of branch
	// validation; invalidated on every head move.
	anc ancestry
	// adoptedCommitPending marks that the next Save commits an
	// adopted staged graph (revert/merge): its mutation set is empty,
	// so the Save-time changelog append must NOT advance the marker
	// -- the explicit tree-diff indexing that follows both indexes
	// and advances, and a crash in between stays repairable by the
	// boot gap walk instead of silently losing the commit's versions.
	adoptedCommitPending bool

	// searchSnapshots caches paginated search result sets so cursor
	// calls slice into a stable matched-set without re-running the
	// query. TTL configured via cfg.Search.Pagination.SnapshotTTL.
	// Lifetime tied to the engine; Close stops the eviction loop.
	searchSnapshots *SnapshotStore

	// opts retains the EngineOptions slice from construction so
	// OpenFiles can re-apply them after a CloseFiles. Required because
	// reload operations (Restore) rebuild the indexSet and providers
	// from scratch; without replay, a test using WithVectorIndex would
	// silently get the default mmap'd index after a restore. Each
	// option is an idempotent pointer assignment, so replay is safe.
	opts []EngineOption
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

// WithVolatileStorage disables every durability sync in the engine's
// write path: per-blob fsync in content-addressed storage, bbolt's
// per-commit sync (indexes.db and jobs.db), HEAD/branch-ref fsyncs,
// and mmap index flushes. Writes remain atomic (temp file + rename)
// and all logic is unchanged -- only the wait-for-stable-storage
// guarantee is skipped, which matters solely for surviving power
// loss or kernel panic.
//
// TEST-ONLY. Intended exclusively for test fixtures running against
// throwaway temp-dir stores, where fsync latency dominates suite
// runtime (measured ~15x on the api package). There is deliberately
// no config surface for this option, and a guard test asserts no
// production code path references it. Do not wire it into config.
func WithVolatileStorage() EngineOption {
	return func(e *Engine) { e.volatile = true }
}

// WithReadOnly forces the engine into store-level read-only mode
// regardless of the STORE manifest. Intended for callers that attach
// to a store they must not mutate (e.g. a future attach flow reading
// someone else's published store).
//
// The option can only FORCE read-only, never unfreeze: openFiles
// seeds the flag from the manifest before options are applied, and
// this option only ever sets it to true. A manifest-frozen store
// therefore stays read-only whether or not the option is present.
func WithReadOnly() EngineOption {
	return func(e *Engine) { e.readOnly.Store(true) }
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
//
// Split into config/storage/providers (this function) and openFiles
// (the bolt+indexes+graph+jobs init). Restore re-uses openFiles after
// closeFiles + on-disk swap; the prov/store/cfg/opts state survives
// across the cycle so they live above the split.
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

	prov, err := newProviders(cfg)
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}

	e := &Engine{
		cfg:   cfg,
		store: s,
		prov:  prov,
		opts:  opts,
	}

	if err := e.openFiles(skipFormatCheck); err != nil {
		return nil, err
	}
	return e, nil
}

// OpenFiles opens the engine's file-backed resources (bbolt indexes,
// mmap vec index, jobs store) and rebuilds the dependent in-memory
// state (graph from HEAD, searcher subsystem, in-flight job recovery,
// background sweeper, search-snapshot store). EngineOptions retained
// from construction are re-applied so test-injected indexes/providers
// survive a reload.
//
// Used during initial construction and after Restore swaps the data
// directory. Caller MUST hold the engine write lock and MUST have
// closed the prior file-backed state via CloseFiles first; openFiles
// trusts the relevant Engine fields to be nil.
//
// On error, all partially-opened resources are closed before returning,
// so the engine's file-backed fields are guaranteed nil and a
// subsequent CloseFiles is a no-op.
func (e *Engine) OpenFiles() error {
	return e.openFiles(false)
}

func (e *Engine) openFiles(skipFormatCheck bool) error {
	if !skipFormatCheck {
		if err := CheckFormatVersion(e.cfg.DataDir); err != nil {
			return fmt.Errorf("store format: %w", err)
		}
	}

	// Read the store manifest and seed the read-only flag before any
	// file is opened. A read error aborts the open, consistent with
	// the format-check handling above: a corrupted manifest on a
	// store that might be frozen must not silently open writable.
	// Read even under skipFormatCheck (migrate path) -- the frozen
	// flag is orthogonal to the format-version gate. WithReadOnly is
	// applied in the options loop below and can only force true, so
	// a manifest-frozen store can never be unfrozen by an option.
	manifest, err := ReadStoreManifest(e.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("store manifest: %w", err)
	}
	e.readOnly.Store(manifest.ReadOnly)

	// Open the shared bbolt database for property index and edge store.
	boltPath := filepath.Join(e.cfg.DataDir, "indexes.db")
	boltDB, err := bolt.Open(boltPath, 0600, nil)
	if err != nil {
		return fmt.Errorf("open bbolt: %w", err)
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
		// their backing store. After cleanup the engine's file-backed
		// fields are guaranteed nil so a follow-up CloseFiles is safe.
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
		e.boltDB = nil
		e.indexes = nil
		e.graph = nil
		e.searcher = nil
		e.headHash = ""
		e.jobStore = nil
		e.jobSweepCancel = nil
		e.jobSweepDone = nil
		e.searchSnapshots = nil
	}()

	idx, err := newIndexSet(boltDB, e.cfg)
	if err != nil {
		return err
	}

	// The access sidecar lives in its own bbolt file, NOT indexes.db:
	// indexes.db is derived state that backup excludes and restore
	// rebuilds, while access metadata is primary bookkeeping that
	// must survive a backup/restore cycle.
	sidecarPath := filepath.Join(e.cfg.DataDir, "sidecar.db")
	sidecarDB, err := bolt.Open(sidecarPath, 0600, nil)
	if err != nil {
		return fmt.Errorf("open sidecar: %w", err)
	}
	cleanups = append(cleanups, func() { sidecarDB.Close() })
	accessIdx, err := index.NewBboltAccessIndex(sidecarDB)
	if err != nil {
		return err
	}
	changelog, err := index.NewBboltChangelog(sidecarDB)
	if err != nil {
		return err
	}
	e.sidecarDB = sidecarDB
	e.accessIdx = accessIdx
	e.changelog = changelog

	g := graph.NewWithCapacity(graph.DefaultCacheCapacity, graph.WithEdgeStore(idx.edgeStore))
	// Installed before Load so eagerly-loaded nodes overlay too.
	g.SetNodeLoadHook(e.overlayAccess)

	// Load HEAD commit if it exists.
	var headHash string
	headPath := filepath.Join(e.cfg.DataDir, "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		headHash = strings.TrimSpace(string(data))
		if headHash != "" {
			if _, err := g.Load(e.store, headHash); err != nil {
				return fmt.Errorf("load HEAD commit: %w", err)
			}
		}
	}

	// Publish indexes/graph onto the engine before option apply so
	// WithVectorIndex (which assigns to e.indexes.vecIdx) sees the new
	// indexSet rather than a stale prior one.
	e.boltDB = boltDB
	e.indexes = idx
	e.graph = g
	e.headHash = headHash

	// Repair changelog marker drift now that HEAD is known -- a
	// crash between a commit's HEAD write and its changelog append
	// left the marker behind; the walk re-derives what's missing.
	e.changelogGapWalk()

	// Apply options before creating the vector index. This lets
	// WithVectorIndex inject an in-memory index for tests, avoiding
	// the disk I/O of MmapFlatIndex creation. On reload the same
	// option list is replayed so test injections survive Restore.
	for _, opt := range e.opts {
		if opt != nil {
			opt(e)
		}
	}

	// Volatile-storage propagation (test-only): options are settled
	// above, so flip the sync switches on the surfaces opened before
	// this point. Later surfaces (vector index, jobs store) receive
	// the flag at their construction below.
	if e.volatile {
		boltDB.NoSync = true
		sidecarDB.NoSync = true
		e.store.SetNoSync(true)
	}

	// If no option provided a vector index, open the mmap'd flat
	// vector index.
	vecCleanup, err := idx.openDefaultVecIdx(e.cfg, e.volatile)
	if err != nil {
		return err
	}
	if vecCleanup != nil {
		cleanups = append(cleanups, vecCleanup)
	}

	// Try to load persisted indexes from commit; each populated index
	// is skipped during the rebuild walk.
	idx.rebuildPrimaryIfMissing(g)

	// Build searcher after all indexes are finalized.
	e.searcher = &searcherSubsystem{}
	e.searcher.rebuild(g, idx.propIdx, idx.vecIdx, idx.bm25Full, idx.secIdx, e.prov.embedder, e.prov.llm, e.cfg)

	// Open the F1 jobs store. Separate bbolt file from indexes.db so
	// it survives backup/restore (indexes.db is excluded as derived
	// state). Restart recovery: any in-flight job from a prior run
	// is flipped to failed/server_restart before any HTTP listener
	// can accept calls.
	jobsPath := filepath.Join(e.cfg.DataDir, "jobs.db")
	jobStore, err := jobs.New(jobsPath)
	if err != nil {
		return fmt.Errorf("open jobs store: %w", err)
	}
	cleanups = append(cleanups, func() { _ = jobStore.Close() })
	if e.volatile {
		jobStore.SetNoSync(true)
	}
	if err := recoverInFlightJobs(jobStore); err != nil {
		return fmt.Errorf("jobs restart recovery: %w", err)
	}
	e.jobStore = jobStore

	// Spawn the GC sweeper goroutine if enabled. SweepInterval=0
	// disables the sweeper; jobs then accumulate until manually
	// pruned. We use a dedicated context so Engine.Close can cancel
	// it cleanly; jobSweepDone closes when the goroutine exits.
	//
	// A read-only engine gets no sweeper: jobs.db is a derived cache
	// (still writable for open-time recovery above), but a frozen
	// store should be inert -- no periodic background writer of any
	// kind. New write jobs can't be created on it anyway. The options
	// loop has already run, so ReadOnly() reflects both the manifest
	// and WithReadOnly. Engine.Close handles the nil cancel/done pair
	// the same as the SweepInterval=0 case.
	if e.cfg.Jobs.SweepInterval > 0 && !e.ReadOnly() {
		sweepCtx, cancel := context.WithCancel(context.Background())
		e.jobSweepCancel = cancel
		e.jobSweepDone = make(chan struct{})
		go runJobSweeper(sweepCtx, e.jobSweepDone, jobStore, e.cfg.Jobs)
	}

	// Search snapshot store for paginated gramaton_search. Owns its
	// own background eviction goroutine; Close stops it.
	e.searchSnapshots = NewSnapshotStore(e.cfg.Search.Pagination.SnapshotTTL)
	cleanups = append(cleanups, func() { e.searchSnapshots.Stop() })

	success = true // disarm the deferred cleanup
	return nil
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

// ReadOnly reports whether the engine is in store-level read-only
// mode -- frozen via the STORE manifest or forced via WithReadOnly.
// Safe for concurrent use without holding the engine lock.
func (e *Engine) ReadOnly() bool {
	return e.readOnly.Load()
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

// SetHeadLocked moves the in-memory HEAD pointer. Branch checkout
// must call this after writing the HEAD file: leaving the stale
// value in place made the NEXT save on the checked-out branch parent
// on the ABANDONED lineage's head, grafting the branches together --
// and the ancestry-validated features (as_of, changelog rebuild)
// then walked the corrupted chain. Caller must hold the write lock.
func (e *Engine) SetHeadLocked(hash string) {
	e.headHash = hash
	e.invalidateAncestry()
}

// Graph returns the underlying graph. Callers must hold the
// appropriate lock via RLock/RUnlock or Lock/Unlock.
func (e *Engine) Graph() *graph.Graph { return e.graph }

// SwapGraph replaces the engine's graph with g. The replacement is
// a single pointer assignment; the old graph becomes GC-eligible as
// soon as no one retains a reference. Caller MUST hold the write
// lock (Lock/Unlock).
//
// Prefer AdoptGraph for state-changing loads (revert, checkout,
// merge): build the replacement off-lock via graph.LoadStaged, then
// AdoptGraph under the lock. Loading directly into a graph that
// shares the engine's populated BboltEdgeStore makes Load skip edge
// reload and keeps the OLD state's edges under the NEW state's
// nodes; a bare graph.New() instead installs a MemoryEdgeStore whose
// subsequent edge writes silently bypass bbolt. AdoptGraph threads
// that needle. Direct SwapGraph remains for callers that already
// hold a correctly-backed graph.
//
// Incremental-commit state (lastNodeTreeRoot/lastEdgeTreeRoot) is
// carried on the graph itself and was set by Load, so subsequent
// saves on the swapped-in graph commit correctly.
func (e *Engine) SwapGraph(g *graph.Graph) { e.graph = g }

// AdoptGraph installs a staged graph (produced by graph.LoadStaged,
// memory-backed edges) as the live graph: the staged edge set
// replaces the shared bbolt edge store's contents wholesale, and the
// staged graph takes ownership of that store so subsequent edge
// writes persist. This is the correct swap for state-changing loads
// (revert, checkout, merge) -- handing the shared store to Load
// directly makes Load skip edge reload entirely because the store is
// already populated with the OLD state, resurrecting or dropping
// edges wholesale. Caller must hold the engine write lock and must
// call RebuildAllIndexes afterwards.
func (e *Engine) AdoptGraph(staged *graph.Graph) {
	staged.MigrateEdgesTo(e.indexes.edgeStore)
	// The staged graph was built hook-less; the adopted live graph
	// must overlay sidecar access values on every materialization.
	staged.SetNodeLoadHook(e.overlayAccess)
	e.graph = staged
	e.adoptedCommitPending = true
	e.invalidateAncestry()
}

// EdgeStore returns the engine's persistent edge store. Mostly
// internal since AdoptGraph took over the state-changing swap path;
// remains exported for direct store inspection (tests, diagnostics).
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

// overlayAccess is the node load hook: it replaces a materialized
// node's stale blob bookkeeping values with the sidecar's live ones.
// Runs on every lazy load, iterator scan, and eager load, so readers
// (search filters, sorts, scoring, exports) keep reading node
// properties and never learn the sidecar exists. Absent sidecar
// entry means the blob's legacy values stand -- the seed source for
// stores migrating from versioned access props.
func (e *Engine) overlayAccess(n *graph.Node) {
	m, ok := e.accessIdx.Get(n.ID)
	if !ok {
		return
	}
	if m.Count > 0 {
		n.Properties["access_count"] = graph.Int64Property(m.Count)
	}
	if !m.LastAccessed.IsZero() {
		n.Properties["last_accessed"] = graph.TimestampProperty(m.LastAccessed)
	}
	if m.EmbedAttempts > 0 {
		n.Properties["embed_attempts"] = graph.Int64Property(m.EmbedAttempts)
	}
}

// RecordAccess bumps one record's access bookkeeping: sidecar first
// (the durable authority), then the cached in-memory node WITHOUT
// dirtying it -- reads must never mint commits. Caller must hold the
// write lock. First access after a migration seeds the sidecar from
// the blob's legacy values. The last_accessed TIME INDEX is
// deliberately not updated per read (that would be one indexes.db
// transaction per read under the write lock); it refreshes at
// startup rebuild from the overlaid props, matching the staleness
// the old flush cadence already had. Filters read live node props,
// which ARE current.
func (e *Engine) RecordAccess(nodeID string, now time.Time) {
	if m, ok := e.bumpAccess(nodeID, now); ok {
		e.accessIdx.Put(nodeID, m)
	}
}

// RecordAccessAll bumps many records in ONE sidecar transaction --
// the search path calls this over a whole result page under the
// write lock, and per-record fsyncs there would stall every reader
// for the batch. Caller must hold the write lock.
func (e *Engine) RecordAccessAll(nodeIDs []string, now time.Time) {
	batch := make(map[string]index.AccessMeta, len(nodeIDs))
	for _, id := range nodeIDs {
		if m, ok := e.bumpAccess(id, now); ok {
			batch[id] = m
		}
	}
	e.accessIdx.PutBatch(batch)
}

// bumpAccess applies one access to the cache-visible state (sidecar
// cache value computed, in-memory props updated without dirtying)
// and returns the metadata to persist.
func (e *Engine) bumpAccess(nodeID string, now time.Time) (index.AccessMeta, bool) {
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return index.AccessMeta{}, false
	}
	m, has := e.accessIdx.Get(nodeID)
	if !has {
		if v, ok := n.Properties.GetInt64("access_count"); ok {
			m.Count = v
		}
		if v, ok := n.Properties.GetInt64("embed_attempts"); ok {
			m.EmbedAttempts = v
		}
	}
	m.Count++
	m.LastAccessed = now
	n.Properties["access_count"] = graph.Int64Property(m.Count)
	n.Properties["last_accessed"] = graph.TimestampProperty(now)
	return m, true
}

// SetEmbedAttempts records reembed retry bookkeeping in the sidecar
// and mirrors it onto the cached node without dirtying it. Caller
// must hold the write lock.
func (e *Engine) SetEmbedAttempts(nodeID string, attempts int64) {
	m, _ := e.accessIdx.Get(nodeID)
	m.EmbedAttempts = attempts
	e.accessIdx.Put(nodeID, m)
	if n, ok := e.graph.GetNode(nodeID); ok {
		if attempts == 0 {
			delete(n.Properties, "embed_attempts")
		} else {
			n.Properties["embed_attempts"] = graph.Int64Property(attempts)
		}
	}
}

// AccessIdx exposes the access sidecar (deletion cleanup, tests).
func (e *Engine) AccessIdx() *index.BboltAccessIndex { return e.accessIdx }

// Changelog exposes the per-node logical-version index (timeline,
// as_of resolution, backfill).
func (e *Engine) Changelog() *index.BboltChangelog { return e.changelog }

// CurationAuthor is the commit and node attribution identity for
// writes the curation subsystem makes on its own initiative.
// curation.NodeAuthor aliases it so node-level and commit-level
// attribution can never drift apart.
const CurationAuthor = "curation"

// commitAuthor resolves the identity a commit is attributed to.
// Curation cycles label their commits with the "curation:" message
// prefix (the same convention gramaton_log's exclude_curation
// filters on); everything else is the operator's composed author
// config -- empty when unconfigured, and Author is omitempty on the
// wire, matching the record-level attribution behavior.
func (e *Engine) commitAuthor(message string) string {
	if strings.HasPrefix(message, "curation:") {
		return CurationAuthor
	}
	return e.cfg.Author.String()
}

// Save commits the current graph state and updates HEAD and the
// active branch ref. Caller must hold the write lock.
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
	// Read-only backstop: reject before any work (vector flush,
	// index marshal, CAS writes). The api layer gates logical writes
	// up front; this catches anything that slips through.
	if e.ReadOnly() {
		return nil, ErrStoreReadOnly
	}

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
	// produced.
	// Snapshot the mutation set BEFORE PrepareCommit rebuilds the
	// hash caches: the changelog's logical-version comparison needs
	// each changed node's PRE-commit blob pointer.
	clDirty, clDeleted, clPrevHash := e.graph.PendingChanges()

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
	commit.Author = e.commitAuthor(message)
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
	if err := atomicWriteFile(headPath, []byte(commit.Hash), 0o600, !e.volatile); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	branch := ActiveBranch(e.cfg.DataDir)
	if err := writeRef(e.cfg.DataDir, branch, commit.Hash, !e.volatile); err != nil {
		return nil, fmt.Errorf("write ref %s: %w", branch, err)
	}

	e.headHash = commit.Hash
	e.advanceAncestry(commit.Hash)

	// Changelog append is ordered after the HEAD write per the
	// durability contract; a crash before it leaves marker != HEAD
	// for the boot gap walk.
	e.appendChangelog(commit, clDirty, clDeleted, clPrevHash)
	return commit, nil
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
	// Read-only short-circuit WITHOUT log spam: this is the
	// fire-and-forget background path, and a read-only store with a
	// still-running background caller should be quiet. Debug only --
	// falling through to Save's ErrStoreReadOnly would emit an Error
	// line on every tick.
	if e.ReadOnly() {
		slog.Debug("save skipped: store is read-only",
			"component", "engine",
			"message", message)
		return
	}
	if _, err := e.Save(message, actions...); err != nil {
		slog.Error("save failed",
			"component", "engine",
			"message", message,
			"err", err)
	}
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
// of their work. fn receives a *WriteSession, matching the
// WithWriteBatch closure shape.
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
// indexes.
func (e *Engine) WithWriteBatch(message string, fn func(*WriteSession) (mutated bool, err error)) error {
	// Read-only backstop: reject at entry, before taking the write
	// lock or opening the bbolt transaction. No work should start on
	// a frozen store.
	if e.ReadOnly() {
		return fmt.Errorf("withwritebatch %q: %w", message, ErrStoreReadOnly)
	}

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
//
// Equivalent to CloseFiles for terminal shutdown. After Close the
// engine struct still owns cfg/store/prov/opts but its file-backed
// state is gone; callers should not reuse it.
func (e *Engine) Close() error {
	return e.CloseFiles()
}

// CloseFiles releases just the file-backed resources (bbolt indexes,
// mmap vec idx, jobs store) and stops their dependent goroutines (job
// sweeper, snapshot eviction). Used by both terminal Close and the
// Restore lifecycle (where it is paired with a subsequent OpenFiles
// against the post-swap data directory). Caller MUST hold the engine
// write lock.
//
// Returns the first error encountered; all resources are closed
// regardless. Idempotent: every field is nil-checked and re-nilled,
// so a second call is a no-op.
//
// Drops in-memory state that depends on the closed file-backed
// resources (graph, searcher, headHash) so OpenFiles starts from a
// clean slate. cfg, store, prov, opts survive across CloseFiles.
func (e *Engine) CloseFiles() error {
	var firstErr error

	// Stop the search-snapshot eviction loop; idempotent.
	if e.searchSnapshots != nil {
		e.searchSnapshots.Stop()
		e.searchSnapshots = nil
	}

	// Stop the job sweeper first so it can't fire mid-close. The
	// goroutine respects sweepCtx and exits cleanly; we wait for
	// it via jobSweepDone. Idempotent: cancel func is set to nil
	// after first call so a second CloseFiles skips the wait.
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
	if e.sidecarDB != nil {
		if err := e.sidecarDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.sidecarDB = nil
		e.accessIdx = nil
		e.changelog = nil
	}

	// Drop in-memory state tied to the now-closed file-backed
	// resources. graph holds the bbolt-backed edge store; searcher
	// holds graph + indexes refs. Nil them so OpenFiles starts from
	// a clean state and stale reads after a botched lifecycle fail
	// loudly rather than silently returning corrupt data.
	e.graph = nil
	e.searcher = nil
	e.headHash = ""

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

// ScanSimilarVec runs the save-guard scan (hold + advisory bands) for
// a not-yet-inserted record described by its embedding and raw
// content. Save runs this under a read lock before acquiring the
// write lock so the O(N) candidate scan stays out of the write
// critical section. The result is advisory with respect to
// concurrency: pair it with WriteSeq before releasing the read lock
// and a SimilarInDelta re-check under the write lock to close the
// scan-to-lock window.
// Caller must hold at least a read lock.
func (e *Engine) ScanSimilarVec(vec []float32, content string) similarity.Outcome {
	return similarity.Scan(e.graph, e.indexes.vecIdx, e.cfg.SaveGuard, vec, content, "")
}

// ScanSimilar runs the save-guard scan for an already-inserted record
// (post-insert re-checks: deferred-embedding saves whose vectors
// arrive via reembed). Caller must hold at least a read lock.
func (e *Engine) ScanSimilar(nodeID string) similarity.Outcome {
	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return similarity.Outcome{}
	}
	vec, content := similarity.NodeEmbeddingAndContent(n)
	if vec == nil {
		return similarity.Outcome{}
	}
	return similarity.Scan(e.graph, e.indexes.vecIdx, e.cfg.SaveGuard, vec, content, nodeID)
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
