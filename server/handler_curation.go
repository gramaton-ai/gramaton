package server

import (
	"context"
	"net/http"

	"github.com/gramaton-ai/gramaton/curation"
)

func (s *Server) handleCurationStatus(w http.ResponseWriter, _ *http.Request) {
	if s.runner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "curation_disabled",
			"curation is not enabled", false)
		return
	}

	status := s.runner.Status()
	manifest := s.runner.Manifest()

	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"manifest": manifest,
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

	// Check for dry_run parameter.
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	// Ignore parse errors -- body is optional for trigger.
	_ = parseJSON(r, &body, maxJSONBodySize)

	if body.DryRun {
		result := s.runner.TriggerDryRun(context.Background())
		s.writeJSON(w, http.StatusOK, map[string]any{
			"triggered": true,
			"dry_run":   true,
			"result":    result,
			"status":    s.runner.Status(),
		})
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

func (s *Server) handleCurationBatch(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"batch curation is restricted to loopback connections", false)
		return
	}

	if s.engine.LLM() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "llm_required",
			"LLM provider is required for batch curation", false)
		return
	}

	cfg := s.engine.Config()
	result, err := curation.RunBatchClassification(
		r.Context(), s.engine, s.engine.LLM(), cfg, s.log)
	if err != nil {
		s.log.Error("batch curation failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "batch_error", "batch processing failed", false)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"data": result,
	})
}
