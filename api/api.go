// Package api is the canonical surface of gramaton's operations.
// Every transport (HTTP, MCP, CLI proxy, future gRPC/websocket) consumes
// this package. The goal is one definition of "what fields an operation
// accepts" and "what it does" -- no transport-specific struct drift.
//
// Each operation lives in its own file (api/<op>.go) and follows the
// same shape:
//
//	type XxxRequest struct { ... with json + jsonschema tags ... }
//	type XxxResponse struct { ... }
//	const XxxDescription = "..."
//	func (a *API) Xxx(ctx context.Context, req XxxRequest) (XxxResponse, *APIError)
//
// Transports read these types and methods via hand-written binding
// tables in their own packages. There is no reflection or codegen.
package api

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/llm"
)

// API is the single operational surface consumed by all transports.
// Construct one per server process; pass it to each transport's
// binding registrar.
type API struct {
	engine       *core.Engine
	runner       *curation.Runner
	usageTracker *llm.UsageTracker
	log          *slog.Logger
	configDir    string
	storeName    string

	// In-memory state that outlives a single request. Moved from the
	// old server.Server per-method ownership so API methods can own
	// their transactions end-to-end.

	preparedMu       sync.Mutex
	preparedSessions map[string]time.Time

	retrieval *RetrievalTracker

	// preparedSweepCancel cancels the prepared-sessions sweeper
	// goroutine on shutdown. Set by startPreparedSweeper.
	preparedSweepCancel context.CancelFunc

	// testHookBackupSnapshotted, when set by tests via
	// SetBackupSnapshotHook, is closed by BackupCreate immediately
	// after phase-1 snapshot completes (before the off-lock
	// compression starts). This lets tests deterministically
	// distinguish "snapshot-time" from "compression-time" without
	// time.Sleep races.
	//
	// faultInjector, when non-nil, is consulted at named phases inside
	// long-running operations. Production runs leave it nil; tests in
	// this package set it via SetFaultInjector to exercise rare error
	// paths (chunk_save failure, jobstore_update failure) without
	// disturbing the underlying storage.
	hooksMu                          sync.Mutex
	testHookBackupSnapshotted        chan struct{}
	testHookHistorySearchSnapshotted chan struct{}
	faultInjector                    FaultInjector

	// asyncMu protects asyncRunners. WaitGroup is goroutine-safe.
	// asyncShutdown gates the spawn of new runners during shutdown.
	asyncMu       sync.Mutex
	asyncRunners  map[string]context.CancelFunc
	asyncWG       sync.WaitGroup
	asyncShutdown atomic.Bool
	// chunkSizeOverride lets tests force a smaller chunk size so the
	// chunked runner can be exercised against tiny inputs. 0 (default)
	// means "use MaxSyncBatchSize". Production never sets this.
	chunkSizeOverride atomic.Int64
}

// FaultInjector is the test-only fault-injection seam. Each
// long-running operation calls Inject at named phases; a non-nil
// returned error short-circuits the operation along its error path.
// The interface is exported so external test packages can provide
// implementations, but the SetFaultInjector setter is intended for
// in-package tests only.
type FaultInjector interface {
	Inject(phase string) error
}

// Phase names recognized by FaultInjector. Defined as constants so
// implementations don't drift from the call sites.
const (
	FaultPhaseChunkSave      = "chunk_save"
	FaultPhaseJobstoreUpdate = "jobstore_update"
	// FaultPhaseEdgeFixup fires inside the L6 chunked runner's
	// post-chunks edge-fixup save. Returning an error simulates the
	// fixup commit failing; the runner rolls back the in-memory edges
	// and marks Job failed/edge_fixup_failed.
	FaultPhaseEdgeFixup = "edge_fixup"
	// FaultPhasePanic is honored only by FaultInjector implementations
	// that can panic on demand. SaveBatch's runner consults this
	// via tests via the panic-injection seam.
	FaultPhasePanic = "panic"
)

// Dependencies holds the collaborators an API needs at construction.
// Keeping this explicit (rather than letting methods reach into a
// god-object) makes it obvious what an operation depends on and gives
// tests a clean surface to stub.
type Dependencies struct {
	Engine       *core.Engine
	Runner       *curation.Runner
	UsageTracker *llm.UsageTracker
	Log          *slog.Logger
	ConfigDir    string
	StoreName    string
}

// New constructs an API. The returned pointer is safe for concurrent
// use -- methods acquire engine locks as needed.
func New(deps Dependencies) *API {
	a := &API{
		engine:           deps.Engine,
		runner:           deps.Runner,
		usageTracker:     deps.UsageTracker,
		log:              deps.Log,
		configDir:        deps.ConfigDir,
		storeName:        deps.StoreName,
		preparedSessions: make(map[string]time.Time),
		retrieval:        NewRetrievalTracker(),
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	// Restore prepared-session flags from disk so a restart between
	// prepare and commit doesn't break the flow.
	a.loadPreparedSessions()
	return a
}

// Engine returns the underlying engine. Exposed for transport-layer
// wiring that needs direct access (e.g. MCP request bindings that
// want to acquire engine locks around registered tool metadata).
// Prefer calling API methods over reaching through this accessor.
func (a *API) Engine() *core.Engine { return a.engine }

// UsageTracker returns the LLM usage tracker. Exposed for transports
// that need to surface cost information outside of a specific
// operation call (e.g. /v1/llm/stats).
func (a *API) UsageTracker() *llm.UsageTracker { return a.usageTracker }

// Runner returns the curation runner. Exposed for transports that
// need to trigger or inspect curation state directly.
func (a *API) Runner() *curation.Runner { return a.runner }

// SetRunner installs the curation runner after construction. The
// runner is created later in the server lifecycle (after New returns)
// so it can't be passed via Dependencies at construction time; this
// setter bridges that gap. Safe to call once before any session or
// curation operations are invoked.
func (a *API) SetRunner(r *curation.Runner) { a.runner = r }

// Logger returns the API's logger. Transports should use this for
// anything emitted under the api component namespace.
func (a *API) Logger() *slog.Logger { return a.log }

// ConfigDir returns the configuration directory path (where hook-state
// files and similar artifacts live).
func (a *API) ConfigDir() string { return a.configDir }

// SetHistorySearchSnapshotHook installs a two-way handshake channel
// fired between HistorySearch's phase-1 snapshot (read lock
// released) and the off-lock blob matching: the operation SENDS one
// value, then RECEIVES one before proceeding. A test receives, takes
// the engine write lock, sends, and joins the search while still
// holding the lock -- if the match phase touched any engine lock it
// would deadlock into the test's watchdog instead of passing by
// luck. Pass nil to clear.
func (a *API) SetHistorySearchSnapshotHook(ch chan struct{}) {
	a.hooksMu.Lock()
	defer a.hooksMu.Unlock()
	a.testHookHistorySearchSnapshotted = ch
}

func (a *API) fireHistorySearchSnapshotHook() {
	a.hooksMu.Lock()
	ch := a.testHookHistorySearchSnapshotted
	a.hooksMu.Unlock()
	if ch != nil {
		ch <- struct{}{}
		<-ch
		a.hooksMu.Lock()
		if a.testHookHistorySearchSnapshotted == ch {
			a.testHookHistorySearchSnapshotted = nil
		}
		a.hooksMu.Unlock()
	}
}

// SetBackupSnapshotHook installs a channel that BackupCreate closes
// after phase-1 snapshot returns. Tests use this to race a
// concurrent capture in between snapshot and compression with no
// timing assumptions. Pass nil to clear.
func (a *API) SetBackupSnapshotHook(ch chan struct{}) {
	a.hooksMu.Lock()
	defer a.hooksMu.Unlock()
	a.testHookBackupSnapshotted = ch
}

func (a *API) fireBackupSnapshotHook() {
	a.hooksMu.Lock()
	ch := a.testHookBackupSnapshotted
	a.hooksMu.Unlock()
	if ch != nil {
		close(ch)
		a.hooksMu.Lock()
		// Clear so a re-trigger doesn't double-close.
		if a.testHookBackupSnapshotted == ch {
			a.testHookBackupSnapshotted = nil
		}
		a.hooksMu.Unlock()
	}
}

// SetFaultInjector installs the FaultInjector consulted at named
// phases of long-running operations. Pass nil to clear. Production
// must never set this; in-package tests only.
func (a *API) SetFaultInjector(fi FaultInjector) {
	a.hooksMu.Lock()
	defer a.hooksMu.Unlock()
	a.faultInjector = fi
}

// SetChunkSizeForTests overrides the chunked async runner's chunk
// size so tests can exercise multi-chunk behavior without seeding
// 1000+ items. Pass 0 to clear (production behavior:
// MaxSyncBatchSize). Production must never set this; in-package
// tests only.
func (a *API) SetChunkSizeForTests(size int) {
	a.chunkSizeOverride.Store(int64(size))
}

// chunkSize returns the chunk size to use for the async runner.
func (a *API) chunkSize() int {
	if v := a.chunkSizeOverride.Load(); v > 0 {
		return int(v)
	}
	return MaxSyncBatchSize
}

func (a *API) injectFault(phase string) error {
	a.hooksMu.Lock()
	fi := a.faultInjector
	a.hooksMu.Unlock()
	if fi == nil {
		return nil
	}
	return fi.Inject(phase)
}

// StopPreparedSweeper cancels the sweeper goroutine started by
// StartPreparedSweeper. Safe to call even if the sweeper never
// started (no-op).
func (a *API) StopPreparedSweeper() {
	a.preparedMu.Lock()
	cancel := a.preparedSweepCancel
	a.preparedSweepCancel = nil
	a.preparedMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// registerAsyncRunner records a running async-batch goroutine so
// SaveBatchCancel can signal it via ctx.Done() and ShutdownAsync
// can wait for in-flight runners to exit. Returns false if the API
// is shutting down -- callers must skip spawning the runner.
func (a *API) registerAsyncRunner(jobID string, cancel context.CancelFunc) bool {
	a.asyncMu.Lock()
	defer a.asyncMu.Unlock()
	if a.asyncShutdown.Load() {
		return false
	}
	if a.asyncRunners == nil {
		a.asyncRunners = make(map[string]context.CancelFunc)
	}
	a.asyncRunners[jobID] = cancel
	a.asyncWG.Add(1)
	return true
}

// unregisterAsyncRunner removes a runner entry on goroutine exit.
func (a *API) unregisterAsyncRunner(jobID string) {
	a.asyncMu.Lock()
	delete(a.asyncRunners, jobID)
	a.asyncMu.Unlock()
	a.asyncWG.Done()
}

// signalAsyncRunner cancels the named runner's context. Safe to call
// for unknown jobIDs (no-op).
func (a *API) signalAsyncRunner(jobID string) {
	a.asyncMu.Lock()
	cancel := a.asyncRunners[jobID]
	a.asyncMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ShutdownAsync prevents new async runners from spawning, cancels
// every in-flight runner's context, and waits for all of them to
// exit (or until ctx.Done()). Call this before closing the engine
// so runners don't touch a closed bbolt handle.
func (a *API) ShutdownAsync(ctx context.Context) error {
	a.asyncShutdown.Store(true)
	a.asyncMu.Lock()
	for _, cancel := range a.asyncRunners {
		cancel()
	}
	a.asyncMu.Unlock()
	done := make(chan struct{})
	go func() { a.asyncWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
