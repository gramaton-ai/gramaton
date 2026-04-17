package server

import (
	"net"
	"net/http"
	"runtime"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// handleHealth is a lightweight liveness endpoint used by the CLI to
// verify the server is alive (verifyServer).
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
	})
}

// handleStatus returns server health and store stats.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	storeChunks, _ := s.engine.Store().List()
	embProvider := s.engine.Config().Embedding.Provider
	embModel := s.engine.Config().Embedding.Model

	embedding := map[string]any{
		"provider": embProvider,
		"model":    embModel,
		"healthy":  s.engine.Embedder() != nil,
	}

	storeName := s.cfg.StoreName
	if storeName == "" {
		storeName = "(default)"
	}

	status := map[string]any{
		"store": map[string]any{
			"name":    storeName,
			"nodes":   s.engine.Graph().NodeCount(),
			"edges":   s.engine.Graph().EdgeCount(),
			"commits": len(storeChunks),
		},
		"branch":    core.ActiveBranch(s.engine.Config().DataDir),
		"embedding": embedding,
	}

	s.writeJSONLocked(w, http.StatusOK, status)
}

// handleShutdown triggers graceful server shutdown.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"shutdown is restricted to loopback connections", false)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"message": "shutting down",
	})

	s.RequestShutdown()
}

// handleDebugGoroutines dumps all goroutine stacks. Loopback only.
// Does NOT acquire any locks -- safe to call during a deadlock.
//
// Buffer grows until runtime.Stack returns less than its capacity,
// indicating the full set fit. The fixed 1MB previous version
// silently truncated on processes with many goroutines -- defeating
// the purpose of a debug endpoint when you most need it.
// (Wave 6 P1-66.)
func (s *Server) handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	const maxBuf = 64 << 20 // 64 MB ceiling
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		if len(buf) >= maxBuf {
			// Truncated; better to return what we have than spin.
			break
		}
		buf = make([]byte, len(buf)*2)
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(buf)
}

// handleLLMStats returns LLM usage metrics.
func (s *Server) handleLLMStats(w http.ResponseWriter, _ *http.Request) {
	if s.usageTracker == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"data": s.usageTracker.Summary()})
}

// isLoopback checks if the request originates from a loopback address.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
