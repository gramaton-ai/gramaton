package server

import (
	"context"
	"net/http"
)

func (s *Server) handleCurationStatus(w http.ResponseWriter, _ *http.Request) {
	if s.runner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "curation_disabled",
			"curation is not enabled", false)
		return
	}

	status := s.runner.Status()
	candidates := s.runner.ConceptCandidates()
	manifest := s.runner.Manifest()

	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":              status,
		"concept_candidates":  candidates,
		"manifest":            manifest,
	})
}

func (s *Server) handleCurationTrigger(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"curation trigger is restricted to loopback connections", false)
		return
	}

	if s.runner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "curation_disabled",
			"curation is not enabled", false)
		return
	}

	if !s.runner.Trigger(context.Background()) {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"triggered": false,
			"message":   "curation cycle already in progress",
			"status":    s.runner.Status(),
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"triggered": true,
		"status":    s.runner.Status(),
	})
}
