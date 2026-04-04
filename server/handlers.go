package server

import (
	"net"
	"net/http"
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

	status := map[string]any{
		"store": map[string]any{
			"nodes":   s.engine.Graph().NodeCount(),
			"edges":   s.engine.Graph().EdgeCount(),
			"commits": len(storeChunks),
		},
		"branch":    "main", // TODO: track active branch in engine
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
