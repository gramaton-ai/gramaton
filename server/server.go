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
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/version"
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

	mu             sync.Mutex
	lastRequest    time.Time
	lastBackup     time.Time
	curationCancel context.CancelFunc
	accessCancel   context.CancelFunc

	retrieval  *retrievalTracker
	observeSem chan struct{} // bounded semaphore for observe goroutines
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
func New(engine *core.Engine, cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		engine:      engine,
		cfg:         cfg,
		log:         logger,
		lastRequest: time.Now(),
		retrieval:   newRetrievalTracker(),
		observeSem:  make(chan struct{}, 3), // max 3 concurrent observe goroutines
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Mount MCP Streamable HTTP handler directly (not through security
	// middleware -- MCP has its own content types and headers).
	mux.Handle("/mcp", s.MCPHandler())

	// Wrap REST routes with security headers. MCP handler is already
	// mounted before the wrapper, so it won't be affected.
	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port),
		Handler:      s.securityHeaders(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 120 * time.Second, // embedding and bulk ops can be slow
		IdleTimeout:  120 * time.Second,
	}

	return s
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

	// Start curation runner.
	engineCfg := s.engine.Config()
	if engineCfg.Curation.Enabled {
		s.runner = curation.NewRunner(s.engine, s.engine.LLM(), engineCfg, s.log)
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

	// Stop access flusher (triggers final flush).
	s.mu.Lock()
	if s.accessCancel != nil {
		s.accessCancel()
	}
	s.mu.Unlock()

	// Graceful shutdown with 30-second deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.log.Error("shutdown error", "err", err)
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

	// Start curation runner.
	engineCfg := s.engine.Config()
	if engineCfg.Curation.Enabled {
		s.runner = curation.NewRunner(s.engine, s.engine.LLM(), engineCfg, s.log)
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

// Shutdown gracefully stops the HTTP server, access flusher, and
// curation runner.
func (s *Server) Shutdown() {
	s.mu.Lock()
	curationCancel := s.curationCancel
	accessCancel := s.accessCancel
	s.mu.Unlock()

	// Stop access flusher first (triggers final flush).
	if accessCancel != nil {
		accessCancel()
	}

	if curationCancel != nil {
		curationCancel()
	}

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()
	s.httpServer.Shutdown(ctx)
	s.removeServerInfo()

	s.log.Info("HTTP server stopped (MCP companion)")
}

// RequestShutdown triggers a graceful shutdown from an API call.
func (s *Server) RequestShutdown() {
	go func() {
		// Give the response time to send.
		time.Sleep(100 * time.Millisecond)
		p, _ := os.FindProcess(os.Getpid())
		p.Signal(syscall.SIGTERM)
	}()
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
	// Records
	mux.HandleFunc("POST /v1/records", s.handleCreateRecord)
	mux.HandleFunc("GET /v1/records/{id}", s.handleGetRecord)
	mux.HandleFunc("PATCH /v1/records/{id}", s.handleUpdateRecord)
	mux.HandleFunc("DELETE /v1/records/{id}", s.handleDeleteRecord)
	mux.HandleFunc("POST /v1/records/{id}/edges", s.handleCreateEdge)
	mux.HandleFunc("DELETE /v1/edges/{edge_id}", s.handleDeleteEdge)
	mux.HandleFunc("POST /v1/records/{id}/classify", s.handleClassifyRecord)
	mux.HandleFunc("POST /v1/records/{id}/resolve", s.handleResolveRecord)
	mux.HandleFunc("GET /v1/records/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		// Redirect to log endpoint with record query param.
		id := r.PathValue("id")
		r.URL.RawQuery = "record=" + id
		s.handleLog(w, r)
	})

	// Search and traversal
	mux.HandleFunc("POST /v1/search", s.handleSearch)
	mux.HandleFunc("POST /v1/explore", s.handleExplore)

	// Pending
	mux.HandleFunc("GET /v1/pending", s.handlePending)

	// Branches
	mux.HandleFunc("GET /v1/branches", s.handleListBranches)
	mux.HandleFunc("POST /v1/branches", s.handleCreateBranch)
	mux.HandleFunc("POST /v1/branches/{name}/checkout", s.handleCheckoutBranch)
	mux.HandleFunc("POST /v1/branches/{name}/merge", s.handleMergeBranch)
	mux.HandleFunc("DELETE /v1/branches/{name}", s.handleDiscardBranch)

	// History
	mux.HandleFunc("GET /v1/log", s.handleLog)
	mux.HandleFunc("GET /v1/diff", s.handleDiff)

	// Operations
	mux.HandleFunc("POST /v1/revert", s.handleRevert)
	mux.HandleFunc("POST /v1/reembed", s.handleReembed)
	mux.HandleFunc("POST /v1/ingest", s.handleIngest)
	mux.HandleFunc("POST /v1/duplicates", s.handleDuplicates)

	// System
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/stats", s.handleStats)
	mux.HandleFunc("POST /v1/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /debug/goroutines", s.handleDebugGoroutines)

	// Backup/Export/Import
	mux.HandleFunc("GET /v1/backup", s.handleBackupStatus)
	mux.HandleFunc("POST /v1/backup", s.handleBackup)
	mux.HandleFunc("POST /v1/restore", s.handleRestore)
	mux.HandleFunc("POST /v1/export", s.handleExport)
	mux.HandleFunc("POST /v1/import", s.handleImport)

	// Curation
	mux.HandleFunc("GET /v1/curation", s.handleCurationStatus)
	mux.HandleFunc("POST /v1/curation/trigger", s.handleCurationTrigger)

	// Observe
	mux.HandleFunc("POST /v1/observe", s.handleObserve)

	// Collections
	mux.HandleFunc("POST /v1/collections", s.handleCollectionCreate)
	mux.HandleFunc("GET /v1/collections", s.handleCollectionList)
	mux.HandleFunc("GET /v1/collections/{id}/items", s.handleCollectionItems)
	mux.HandleFunc("POST /v1/collections/{id}/items", s.handleCollectionAdd)
	mux.HandleFunc("PATCH /v1/collections/{id}/items/{item_id}", s.handleCollectionUpdateItem)
	mux.HandleFunc("POST /v1/collections/{id}/items/{item_id}/move", s.handleCollectionMoveItem)
	mux.HandleFunc("DELETE /v1/collections/{id}/items/{item_id}", s.handleCollectionRemoveItem)
	mux.HandleFunc("PATCH /v1/collections/{id}", s.handleCollectionRename)
	mux.HandleFunc("DELETE /v1/collections/{id}", s.handleCollectionDelete)
	mux.HandleFunc("GET /v1/collections/{id}/schema", s.handleCollectionSchemaRead)
	mux.HandleFunc("PUT /v1/collections/{id}/schema", s.handleCollectionSchemaUpdate)
	mux.HandleFunc("POST /v1/collections/{id}/migrate", s.handleCollectionMigrate)
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

	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}

	cfgPath := filepath.Join(s.cfg.ConfigDir, "config.yaml")

	s.engine.RLock()
	archivePath, err := backup.Create(cfg.DataDir, cfgPath, backupDir, s.cfg.StoreName)
	s.engine.RUnlock()

	if err != nil {
		s.log.Error("auto-backup failed", "err", err)
		return
	}

	deleted, _ := backup.ApplyRetention(backupDir, cfg.Backup.Retain)

	s.mu.Lock()
	s.lastBackup = time.Now()
	s.mu.Unlock()

	s.log.Info("auto-backup created", "path", archivePath, "deleted_old", len(deleted))
}

// securityHeaders wraps a handler with security response headers
// and request logging. Skips the /mcp path since MCP has its own
// content types.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.recordActivity()
		start := time.Now()

		// Don't set JSON content-type for MCP -- it uses SSE and
		// has its own content negotiation.
		if r.URL.Path != "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)

		// Request logging at debug level.
		s.log.Debug("request",
			"component", "http",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr)
	})
}

// writeJSON writes a JSON response with the standard envelope.
// Callers that already hold a lock should use writeJSONWithCuration
// to avoid deadlock (RWMutex is not reentrant).
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	s.engine.RLock()
	curation := computeCuration(s.engine, s.runner)
	s.engine.RUnlock()

	s.writeJSONRaw(w, status, data, curation)
}

// writeJSONLocked writes a JSON response when the caller already holds
// a lock. Computes curation without acquiring a separate lock.
func (s *Server) writeJSONLocked(w http.ResponseWriter, status int, data any) {
	curation := computeCuration(s.engine, s.runner)
	s.writeJSONRaw(w, status, data, curation)
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

// writeError writes a JSON error response.
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
}

// ErrorResponse is the standard error wrapper.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error information.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// computeCuration checks pending record count. Caller must hold
// at least a read lock on the engine. If a runner is provided,
// enriches with curation state (uses runner's own mutex, not the
// engine lock, so no deadlock risk).
func computeCuration(e *core.Engine, runner *curation.Runner) CurationStatus {
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

	return status
}
