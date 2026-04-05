// Package server provides the Gramaton HTTP server (daemon).
// It wraps a core.Engine with HTTP handlers, concurrency control
// via the engine's RWMutex, and lifecycle management (idle timeout,
// graceful shutdown).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
)

// Config holds server configuration.
type Config struct {
	Port        int           `yaml:"port"`
	Bind        string        `yaml:"bind"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	ConfigDir   string        // runtime, not from YAML
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
	logger     *log.Logger

	mu          sync.Mutex
	lastRequest time.Time
}

// New creates a new server wrapping the given engine.
func New(engine *core.Engine, cfg Config) *Server {
	s := &Server{
		engine:      engine,
		cfg:         cfg,
		logger:      log.New(os.Stderr, "[gramaton] ", log.LstdFlags),
		lastRequest: time.Now(),
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

	s.logger.Printf("server started on %s (store: %s)", ln.Addr(), s.engine.Config().DataDir)
	s.logger.Printf("nodes: %d, edges: %d", s.engine.NodeCount(), s.engine.EdgeCount())

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Idle timeout checker.
	shutdownCh := make(chan string, 1)
	go s.idleWatcher(shutdownCh)

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
		s.logger.Printf("received signal %s, shutting down", sig)
	case reason := <-shutdownCh:
		s.logger.Printf("shutting down: %s", reason)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	}

	// Graceful shutdown with 30-second deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Printf("shutdown error: %s", err)
	}

	s.logger.Println("server stopped")
	return nil
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
	mux.HandleFunc("POST /v1/records/{id}/classify", s.handleClassifyRecord)
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
}

// securityHeaders wraps a handler with security response headers.
// Skips the /mcp path since MCP has its own content types.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.recordActivity()

		// Don't set JSON content-type for MCP -- it uses SSE and
		// has its own content negotiation.
		if r.URL.Path != "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response with the standard envelope.
// Callers that already hold a lock should use writeJSONWithCuration
// to avoid deadlock (RWMutex is not reentrant).
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	s.engine.RLock()
	curation := computeCuration(s.engine)
	s.engine.RUnlock()

	s.writeJSONRaw(w, status, data, curation)
}

// writeJSONLocked writes a JSON response when the caller already holds
// a lock. Computes curation without acquiring a separate lock.
func (s *Server) writeJSONLocked(w http.ResponseWriter, status int, data any) {
	curation := computeCuration(s.engine)
	s.writeJSONRaw(w, status, data, curation)
}

func (s *Server) writeJSONRaw(w http.ResponseWriter, status int, data any, curation CurationStatus) {
	envelope := ResponseEnvelope{
		Data:     data,
		Curation: curation,
		Meta: ResponseMeta{
			Version: "0.2.0",
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
	PendingCount int  `json:"pending_count"`
	Overdue      bool `json:"overdue"`
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
// at least a read lock on the engine.
func computeCuration(e *core.Engine) CurationStatus {
	captured := e.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))
	return CurationStatus{
		PendingCount: len(captured),
		Overdue:      len(captured) > 0,
	}
}
