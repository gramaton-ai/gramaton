package server

import (
	"net"
	"net/http"
	"runtime"

	"github.com/brandonlattin/gramaton/core"
)

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

// handleShutdown triggers graceful server shutdown. Restricted to
// loopback connections only.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"shutdown is restricted to loopback connections", false)
		return
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]string{
		"message": "shutting down",
	})

	s.RequestShutdown()
}

// handleDebugGoroutines dumps all goroutine stacks. Loopback only.
// Does NOT acquire any locks -- safe to call during a deadlock.
func (s *Server) handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	buf := make([]byte, 1<<20) // 1MB
	n := runtime.Stack(buf, true)
	w.Header().Set("Content-Type", "text/plain")
	w.Write(buf[:n])
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
