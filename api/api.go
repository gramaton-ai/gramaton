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
	"log/slog"
	"sync"
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

	// In-memory state that outlives a single request. Moved from the
	// old server.Server per-method ownership so API methods can own
	// their transactions end-to-end.

	preparedMu       sync.Mutex
	preparedSessions map[string]time.Time

	observeSem chan struct{}
	retrieval  *RetrievalTracker
}

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

	// ObserveConcurrency bounds the number of in-flight observed-mode
	// intake goroutines. Zero means 3 (historical default).
	ObserveConcurrency int
}

// New constructs an API. The returned pointer is safe for concurrent
// use -- methods acquire engine locks as needed.
func New(deps Dependencies) *API {
	obs := deps.ObserveConcurrency
	if obs <= 0 {
		obs = 3
	}
	a := &API{
		engine:           deps.Engine,
		runner:           deps.Runner,
		usageTracker:     deps.UsageTracker,
		log:              deps.Log,
		configDir:        deps.ConfigDir,
		preparedSessions: make(map[string]time.Time),
		observeSem:       make(chan struct{}, obs),
		retrieval:        NewRetrievalTracker(),
	}
	if a.log == nil {
		a.log = slog.Default()
	}
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

// Log returns the API's logger. Transports should use this for
// anything emitted under the api component namespace.
func (a *API) Log() *slog.Logger { return a.log }

// ConfigDir returns the configuration directory path (where hook-state
// files and similar artifacts live).
func (a *API) ConfigDir() string { return a.configDir }
