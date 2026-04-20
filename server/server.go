// Package server provides the Gramaton HTTP server (daemon).
// It wraps a core.Engine with HTTP handlers, concurrency control
// via the engine's RWMutex, and lifecycle management (idle timeout,
// graceful shutdown).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/version"
	"github.com/gramaton-ai/gramaton/llm"
)

// Config holds server configuration.
type Config struct {
	Port        int           `yaml:"port"`
	Bind        string        `yaml:"bind"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	ConfigDir   string        // runtime, not from YAML
	StoreName   string        // runtime, empty = default unnamed store
}

// DefaultConfig returns server config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Port:        42982,
		Bind:        "127.0.0.1",
		IdleTimeout: 30 * time.Minute,
	}
}

// Server is the Gramaton HTTP daemon.
type Server struct {
	engine     *core.Engine
	cfg        Config
	httpServer *http.Server
	log        *slog.Logger
	runner     *curation.Runner

	// api is the canonical operations surface. Methods migrate here
	// from the server layer one cluster at a time (T-02); transports
	// (HTTP / MCP / CLI proxy) will consume api via binding tables.
	// Currently constructed but only used where operations have been
	// migrated.
	api *api.API

	mu                  sync.Mutex
	lastRequest         time.Time
	lastBackup          time.Time
	curationCancel      context.CancelFunc
	accessCancel        context.CancelFunc
	preparedSweepCancel context.CancelFunc

	retrieval    *retrievalTracker
	usageTracker *llm.UsageTracker

	// Session prepare/commit state. In-memory; lost on restart (B2 resolution).
	// Key: session ID, value: timestamp when prepare was called.
	preparedSessions map[string]time.Time

	// Curation envelope cache: every successful HTTP response embeds
	// a CurationStatus. Computing it requires an engine RLock plus a
	// PropIdx lookup ("processing_status" = "captured"), so it cost
	// per-request CPU on busy servers. We cache for curationCacheTTL
	// since the envelope's underlying counts shift on a curation
	// tick (~1 minute), not per-request. (Wave 3 P1-29.)
	curationCacheMu  sync.RWMutex
	curationCache    CurationStatus
	curationCacheAt  time.Time
	curationCacheTTL time.Duration
}

// retrievalTracker records which node IDs were served to agents via
// search, inspect, and explore. Used by the observe pipeline's
// feedback loop detection (Gate 3) to prevent re-extracting knowledge
// that was just retrieved.
type retrievalTracker struct {
	mu      sync.Mutex
	entries map[string]time.Time // nodeID -> when last served
	maxAge  time.Duration
	maxSize int
}

func newRetrievalTracker() *retrievalTracker {
	return &retrievalTracker{
		entries: make(map[string]time.Time),
		maxAge:  4 * time.Hour,
		maxSize: 500,
	}
}

// Track records that one or more node IDs were served to the agent.
func (rt *retrievalTracker) Track(ids ...string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range ids {
		rt.entries[id] = now
	}
	// Enforce size bound.
	if len(rt.entries) > rt.maxSize {
		rt.pruneOldest()
	}
}

// RetrievedIDs returns all currently tracked node IDs (not expired).
func (rt *retrievalTracker) RetrievedIDs() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.pruneExpired()
	ids := make([]string, 0, len(rt.entries))
	for id := range rt.entries {
		ids = append(ids, id)
	}
	return ids
}

// Len returns the number of tracked entries.
func (rt *retrievalTracker) Len() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.entries)
}

func (rt *retrievalTracker) pruneExpired() {
	cutoff := time.Now().UTC().Add(-rt.maxAge)
	for id, t := range rt.entries {
		if t.Before(cutoff) {
			delete(rt.entries, id)
		}
	}
}

func (rt *retrievalTracker) pruneOldest() {
	// Find and remove the oldest entry until under maxSize.
	for len(rt.entries) > rt.maxSize {
		var oldestID string
		var oldestTime time.Time
		for id, t := range rt.entries {
			if oldestID == "" || t.Before(oldestTime) {
				oldestID = id
				oldestTime = t
			}
		}
		if oldestID != "" {
			delete(rt.entries, oldestID)
		}
	}
}

// New creates a new server wrapping the given engine.
// New creates a server. Requires an engine with a configured LLM
// provider -- the server performs classification on every record and
// cannot operate without one. Returns an error if LLM is not configured.
func New(engine *core.Engine, cfg Config, logger *slog.Logger) (*Server, error) {
	if engine.LLM() == nil {
		return nil, fmt.Errorf("LLM provider is required: configure llm.provider and llm.model in config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	engineCfg := engine.Config()
	usageTracker := llm.NewUsageTracker(cfg.ConfigDir, engineCfg.LLM.MaxCallsPerDay, engineCfg.LLM.MaxCallsPerSession)
	s := &Server{
		engine:       engine,
		cfg:          cfg,
		log:          logger,
		lastRequest:  time.Now(),
		retrieval:        newRetrievalTracker(),
		usageTracker:     usageTracker,
		preparedSessions: make(map[string]time.Time),
		curationCacheTTL: 5 * time.Second,
	}
	// Construct the canonical API surface. As operations migrate into
	// the api package (T-02), transports will call s.api.X instead of
	// s.serviceX. Kept on Server for now; lives past migration as the
	// shared reference all three transports (HTTP/MCP/CLI-proxy)
	// consume via binding tables.
	s.api = api.New(api.Dependencies{
		Engine:       engine,
		UsageTracker: usageTracker,
		Log:          logger,
		ConfigDir:    cfg.ConfigDir,
		StoreName:    cfg.StoreName,
	})

	// Seed validation with the active LimitsConfig so user YAML overrides
	// take effect on summary_short cap, keyword count, etc. Both the
	// package-level server limits and the api limits are set during
	// the migration; once all operations move to api, the server
	// duplicates disappear.
	setServerLimits(engineCfg.Limits)
	api.SetLimits(engineCfg.Limits)

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Mount MCP Streamable HTTP handler directly (not through security
	// middleware -- MCP has its own content types and headers).
	// Loopback-only: MCP exposes destructive admin tools (gramaton_branch
	// merge/discard, gramaton_backup, gramaton_curation trigger/batch)
	// whose REST counterparts are loopback-gated. Without this guard the
	// /mcp endpoint becomes a bypass for those gates when the server
	// binds to a non-loopback address. The supported flow (CLI MCP proxy
	// -> HTTP localhost) is unaffected.
	mux.Handle("/mcp", loopbackOnly(s.MCPHandler()))

	// Wrap REST routes with security headers. MCP handler is already
	// mounted before the wrapper, so it won't be affected.
	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port),
		Handler:      s.securityHeaders(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 120 * time.Second, // embedding and bulk ops can be slow
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

// Run starts the server and blocks until shutdown. It handles
// signals (SIGTERM, SIGINT), idle timeout, and writes the server
// info file for CLI discovery.
func (s *Server) Run() error {
	// Write server info for CLI discovery.
	if err := s.writeServerInfo(); err != nil {
		return fmt.Errorf("write server info: %w", err)
	}
	defer s.removeServerInfo()

	// Listen first so we know the port before logging.
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}

	s.log.Info("server started",
		"addr", ln.Addr().String(),
		"store", s.engine.Config().DataDir,
		"nodes", s.engine.NodeCount(),
		"edges", s.engine.EdgeCount())

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Idle timeout checker.
	shutdownCh := make(chan string, 1)
	go s.idleWatcher(shutdownCh)

	// Start access flusher.
	s.startAccessFlusher()

	// Start prepared-sessions sweeper.
	s.api.StartPreparedSweeper()

	// Start curation runner. Wrap LLM with metered provider for usage tracking.
	engineCfg := s.engine.Config()
	if engineCfg.Curation.Enabled {
		curationLLM := s.engine.LLM()
		if curationLLM != nil && s.usageTracker != nil {
			curationLLM = llm.NewMetered(curationLLM, s.usageTracker, s.log)
		}
		s.runner = curation.NewRunner(s.engine, curationLLM, engineCfg, s.log)
		s.api.SetRunner(s.runner)
		curationCtx, curationCancel := context.WithCancel(context.Background())
		defer curationCancel()
		go s.runner.Start(curationCtx)
		if engineCfg.Backup.Enabled {
			s.runner.SetPostCycleHook(func() {
				s.runAutoBackup()
			})
		}
		if s.engine.LLM() != nil {
			s.log.Info("curation started", "mode", "deterministic+autonomous", "llm", s.engine.LLM().ModelID())
		} else {
			s.log.Info("curation started", "mode", "deterministic")
		}
	}

	// Serve in background.
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(ln); err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for shutdown trigger.
	select {
	case sig := <-sigCh:
		s.log.Info("received signal, shutting down", "signal", sig.String())
	case reason := <-shutdownCh:
		s.log.Info("shutting down", "reason", reason)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	}

	// Stop access flusher (triggers final flush) and prepared-sessions sweeper.
	s.mu.Lock()
	if s.accessCancel != nil {
		s.accessCancel()
	}
	{
		s.api.StopPreparedSweeper()
	}
	s.mu.Unlock()

	// Graceful shutdown with 30-second deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.log.Error("shutdown error", "err", err)
	}

	// Persist LLM usage tracking.
	if s.usageTracker != nil {
		s.usageTracker.Persist()
	}

	// Close the engine (flushes mmap vectors, closes bbolt DB).
	if err := s.engine.Close(); err != nil {
		s.log.Warn("engine close error", "err", err)
	}

	s.log.Info("server stopped")
	return nil
}

// StartHTTP starts the HTTP server and curation runner in background
// goroutines without blocking. Use Shutdown to stop. Designed for the
// MCP stdio process which needs the HTTP server running alongside the
// stdio transport.
func (s *Server) StartHTTP() error {
	// Write server info for CLI discovery.
	if err := s.writeServerInfo(); err != nil {
		return fmt.Errorf("write server info: %w", err)
	}

	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}

	s.log.Info("HTTP server started (MCP companion)",
		"addr", ln.Addr().String(),
		"store", s.engine.Config().DataDir)

	// Start access flusher.
	s.startAccessFlusher()

	// Start prepared-sessions sweeper.
	s.api.StartPreparedSweeper()

	// Start curation runner. Wrap LLM with metered provider for usage tracking.
	engineCfg := s.engine.Config()
	if engineCfg.Curation.Enabled {
		curationLLM := s.engine.LLM()
		if curationLLM != nil && s.usageTracker != nil {
			curationLLM = llm.NewMetered(curationLLM, s.usageTracker, s.log)
		}
		s.runner = curation.NewRunner(s.engine, curationLLM, engineCfg, s.log)
		s.api.SetRunner(s.runner)
		curationCtx, curationCancel := context.WithCancel(context.Background())
		go s.runner.Start(curationCtx)

		// Store cancel for shutdown.
		s.mu.Lock()
		s.curationCancel = curationCancel
		s.mu.Unlock()

		if engineCfg.Backup.Enabled {
			s.runner.SetPostCycleHook(func() {
				s.runAutoBackup()
			})
		}
	}

	// Serve HTTP in background.
	go func() {
		if err := s.httpServer.Serve(ln); err != http.ErrServerClosed {
			s.log.Error("HTTP server error", "err", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the HTTP server, access flusher, prepared-
// sessions sweeper, and curation runner.
func (s *Server) Shutdown() {
	s.mu.Lock()
	curationCancel := s.curationCancel
	accessCancel := s.accessCancel
	s.mu.Unlock()

	// Stop access flusher first (triggers final flush).
	if accessCancel != nil {
		accessCancel()
	}

	// Stop the prepared-sessions sweeper owned by the api layer.
	s.api.StopPreparedSweeper()

	if curationCancel != nil {
		curationCancel()
	}

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()
	s.httpServer.Shutdown(ctx)
	s.removeServerInfo()

	// Close the engine (flushes mmap vectors, closes bbolt DB).
	if err := s.engine.Close(); err != nil {
		s.log.Warn("engine close error", "err", err)
	}

	s.log.Info("HTTP server stopped (MCP companion)")
}

// RequestShutdown triggers a graceful shutdown from an API call.
// Caller is responsible for any response flushing -- this function
// only signals SIGTERM. (The 100ms sleep + recover() that used to
// be here was a magic number; handleShutdown now flushes explicitly
// before calling here. Wave 7 P1-64.)
func (s *Server) RequestShutdown() {
	go func() {
		p, err := os.FindProcess(os.Getpid())
		if err == nil && p != nil {
			p.Signal(syscall.SIGTERM)
		}
	}()
}

// Handler returns the HTTP handler for use with httptest.NewServer
// or other test infrastructure.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// recordActivity updates the last request timestamp for idle tracking.
func (s *Server) recordActivity() {
	s.mu.Lock()
	s.lastRequest = time.Now()
	s.mu.Unlock()
}

// accessFlusher periodically persists deferred access metadata
// (access_count, last_accessed, activation_boost). Runs as a
// background goroutine. Exits when ctx is cancelled.
func (s *Server) accessFlusher(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown.
			s.engine.FlushAccess()
			return
		case <-ticker.C:
			s.engine.FlushAccess()
		}
	}
}

// startAccessFlusher starts the background access flusher and
// stores its cancel function for shutdown.
func (s *Server) startAccessFlusher() {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.accessCancel = cancel
	s.mu.Unlock()
	go s.accessFlusher(ctx)
}

// idleWatcher checks for idle timeout and signals shutdown.
func (s *Server) idleWatcher(shutdownCh chan<- string) {
	if s.cfg.IdleTimeout <= 0 {
		return
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		idle := time.Since(s.lastRequest)
		s.mu.Unlock()

		if idle >= s.cfg.IdleTimeout {
			shutdownCh <- fmt.Sprintf("idle for %s", idle.Round(time.Second))
			return
		}
	}
}

// registerRoutes sets up the HTTP routes.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Records cluster: migrated to the api package. Each route is a
	// thin shim (parse -> call s.api.X -> write) in bindings_records.go.
	s.registerRecordsRoutes(mux)

	// Search + ops cluster: migrated to api. Shims in bindings_search.go.
	// Covers /v1/search, /v1/explore, /v1/pending, /v1/stats, /v1/status,
	// /v1/duplicates.
	s.registerSearchRoutes(mux)

	// Admin cluster: branches + backup + restore + export + import
	// migrated to api (PR #3 of admin-cluster migration). Shims in
	// bindings_admin.go. Backup create uses snapshot-consistent
	// phase split -- RLock snapshot HEAD/refs, release, then
	// compress off-lock.
	s.registerAdminRoutes(mux)

	// History cluster: log + diff + per-record history migrated to api
	// (PR #2 of admin-cluster migration). Shims in bindings_history.go.
	s.registerHistoryRoutes(mux)

	// Intake (unified write endpoint)
	mux.HandleFunc("POST /v1/intake", s.handleIntake)

	// Operations (not yet migrated to api)
	mux.HandleFunc("POST /v1/revert", s.handleRevert)
	mux.HandleFunc("POST /v1/ingest", s.handleIngest)

	// System -- /v1/status, /v1/stats, /v1/duplicates moved to api.
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/stats/llm", s.handleLLMStats)
	mux.HandleFunc("POST /v1/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /debug/goroutines", s.handleDebugGoroutines)

	// Maintenance cluster: curation + reembed migrated to api (PR #1
	// of admin-cluster migration). Shims in bindings_maintenance.go.
	s.registerMaintenanceRoutes(mux)

	// Collections cluster: migrated to api package (T-02). Shims in
	// bindings_collections.go.
	s.registerCollectionsRoutes(mux)

	// Sessions cluster: migrated to api package (T-02). Shims in
	// bindings_sessions.go. Covers /v1/sessions,
	// /v1/sessions/{id}, /v1/sessions/{id}/prepare,
	// /v1/sessions/{id}/commit, /v1/sessions/{id}/archive.
	s.registerSessionsRoutes(mux)
}

// Log returns the server's structured logger.
func (s *Server) Log() *slog.Logger { return s.log }

// runAutoBackup creates a backup if enough time has elapsed since the last one.
func (s *Server) runAutoBackup() {
	cfg := s.engine.Config()
	schedule := cfg.Backup.Schedule
	if schedule <= 0 {
		schedule = 24 * time.Hour
	}

	s.mu.Lock()
	elapsed := time.Since(s.lastBackup)
	s.mu.Unlock()

	if elapsed < schedule {
		return
	}

	result, apiErr := s.api.BackupCreate(context.Background())
	if apiErr != nil {
		s.log.Error("auto-backup failed", "err", apiErr.Code, "msg", apiErr.Message)
		return
	}
	archivePath := result.Path
	deleted := result.DeletedOld

	s.mu.Lock()
	s.lastBackup = time.Now()
	s.mu.Unlock()

	s.log.Info("auto-backup created", "path", archivePath, "deleted_old", len(deleted))
}

// requestIDKey is the context key for per-request correlation IDs.
type requestIDKey struct{}

// requestCounter generates monotonically increasing request IDs.
var requestCounter atomic.Uint64

// securityHeaders wraps a handler with security response headers
// and request logging. Skips the /mcp path since MCP has its own
// content types.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.recordActivity()
		start := time.Now()

		// Generate a short correlation ID for this request.
		reqID := fmt.Sprintf("r%d", requestCounter.Add(1))
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, reqID))

		// Don't set JSON content-type for MCP -- it uses SSE and
		// has its own content negotiation.
		if r.URL.Path != "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
		}

		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)

		dur := time.Since(start)
		s.log.Info("request",
			"component", "http",
			"req_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", dur.Milliseconds(),
			"remote", r.RemoteAddr)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// writeJSON writes a JSON response with the standard envelope.
// Callers that already hold a lock should use writeJSONLocked to
// avoid deadlock (RWMutex is not reentrant).
//
// The curation envelope is cached for curationCacheTTL (5s) so this
// hot path no longer takes an engine RLock + PropIdx lookup per
// request. Stale data on a 5s window is fine -- the underlying
// counters shift on the curation tick (~1 minute).
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	curation := s.curationStatus(true)
	s.writeJSONRaw(w, status, data, curation)
}

// writeJSONLocked writes a JSON response when the caller already
// holds the engine lock (read or write). Reads the curation cache
// without refreshing -- refresh would require its own engine RLock,
// which would deadlock.
func (s *Server) writeJSONLocked(w http.ResponseWriter, status int, data any) {
	curation := s.curationStatus(false)
	s.writeJSONRaw(w, status, data, curation)
}

// curationStatus returns the cached curation envelope. If
// allowRefresh is true and the cache is stale (or empty), refreshes
// it under an engine RLock first. Callers that hold the engine
// lock MUST pass allowRefresh=false to avoid deadlock; in that
// case a stale value is returned (or zero-value if never populated,
// which is harmless -- agents see "no curation status this turn"
// rather than incorrect data).
func (s *Server) curationStatus(allowRefresh bool) CurationStatus {
	s.curationCacheMu.RLock()
	cached := s.curationCache
	cachedAt := s.curationCacheAt
	s.curationCacheMu.RUnlock()

	if !allowRefresh {
		return cached
	}
	if !cachedAt.IsZero() && time.Since(cachedAt) < s.curationCacheTTL {
		return cached
	}

	// Refresh under engine RLock + cache write lock.
	s.engine.RLock()
	fresh := computeCuration(s.engine, s.runner, s.usageTracker)
	s.engine.RUnlock()

	s.curationCacheMu.Lock()
	s.curationCache = fresh
	s.curationCacheAt = time.Now()
	s.curationCacheMu.Unlock()

	return fresh
}

func (s *Server) writeJSONRaw(w http.ResponseWriter, status int, data any, curation CurationStatus) {
	envelope := ResponseEnvelope{
		Data:     data,
		Curation: curation,
		Meta: ResponseMeta{
			Version: version.Version,
		},
	}

	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(envelope)
}

// writeError writes a JSON error response. Includes the curation
// envelope so agents see the same backlog signals on a 4xx/5xx as
// they do on a 2xx -- without it, an agent hammering an erroring
// endpoint never learns the store has work pending. (P2-13 nit.)
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(ErrorResponse{
		Error: ErrorDetail{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
		Curation: s.curationStatus(false),
	})
}

// ResponseEnvelope is the standard response wrapper.
type ResponseEnvelope struct {
	Data     any            `json:"data"`
	Curation CurationStatus `json:"curation"`
	Meta     ResponseMeta   `json:"meta"`
}

// ResponseMeta contains response metadata.
type ResponseMeta struct {
	DurationMs int64  `json:"duration_ms,omitempty"`
	Version    string `json:"version"`
}

// CurationStatus reports pending curation state.
type CurationStatus struct {
	PendingCount      int        `json:"pending_count"`
	Overdue           bool       `json:"overdue"`
	ConceptCandidates int        `json:"concept_candidates,omitempty"`
	StaleCount        int        `json:"stale_count,omitempty"`
	OrphanCount       int        `json:"orphan_count,omitempty"`
	LastCurated       *time.Time `json:"last_curated,omitempty"`
	Autonomous        bool       `json:"autonomous,omitempty"`
	LLMCallsToday    int        `json:"llm_calls_today,omitempty"`
	LLMDailyCap      int        `json:"llm_daily_cap,omitempty"`
	LLMCapPct        int        `json:"llm_cap_pct,omitempty"`
	Paused           bool       `json:"paused,omitempty"`
	PauseReason      string     `json:"pause_reason,omitempty"`
}

// ErrorResponse is the standard error wrapper. Includes the curation
// envelope so error responses carry the same backlog signal as
// successful ones.
type ErrorResponse struct {
	Error    ErrorDetail    `json:"error"`
	Curation CurationStatus `json:"curation,omitzero"`
}

// ErrorDetail contains error information.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Error makes ErrorDetail satisfy the error interface so CLI clients can
// return it verbatim (rather than collapsing to a plain fmt.Errorf) and
// downstream code can recover Code/Retryable via errors.As. Transports
// that care only about a human string still get one via "%s: %s".
func (e *ErrorDetail) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// computeCuration checks pending record count. Caller must hold
// at least a read lock on the engine. If a runner is provided,
// enriches with curation state (uses runner's own mutex, not the
// engine lock, so no deadlock risk).
func computeCuration(e *core.Engine, runner *curation.Runner, usage *llm.UsageTracker) CurationStatus {
	captured := e.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))
	status := CurationStatus{
		PendingCount: len(captured),
		Overdue:      len(captured) > 0,
	}

	if runner != nil {
		enhanced := runner.Status()
		status.ConceptCandidates = enhanced.ConceptCandidates
		status.StaleCount = enhanced.StaleCount
		status.OrphanCount = enhanced.OrphanCount
		status.LastCurated = enhanced.LastCurated
		status.Autonomous = enhanced.Autonomous
	}

	if usage != nil {
		status.LLMCallsToday = usage.TodayCalls()
		status.LLMDailyCap = usage.DailyCap()
		status.LLMCapPct = usage.DailyCapPct()
		paused, reason := usage.IsPaused()
		status.Paused = paused
		status.PauseReason = reason
	}

	return status
}
