package server

import (
	"context"
	"net/http"
)

// intakeRequest is the unified write endpoint. The server triages
// based on content and (future) collection hints.
type intakeRequest struct {
	// Content to store (required).
	Content string `json:"content,omitempty"`

	// Mode is retained as a tombstone field so the server can return
	// a clear "removed" error when older callers still send
	// mode="observed".
	Mode string `json:"mode,omitempty"`

	// Context signals for the server LLM classifier. These replace
	// the agent guessing our enum taxonomy.
	ContextSourceType      string `json:"context_source_type,omitempty"`
	ContextTimeSensitivity string `json:"context_time_sensitivity,omitempty"`
	ContextReliability     string `json:"context_reliability,omitempty"`
	ContextCaptureReason   string `json:"context_capture_reason,omitempty"`

	// Existing context fields.
	ContextAbout    string `json:"context_about,omitempty"`
	ContextWho      string `json:"context_who,omitempty"`
	ContextFindable string `json:"context_findable_by,omitempty"`

	// Agent-provided hints (passed to classifier, not stored as final).
	Temporality   string   `json:"temporality,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	KnowledgeType string   `json:"knowledge_type,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	SummaryShort  string   `json:"summary_short,omitempty"`

	// Source provenance.
	SourceRef    string `json:"source_ref,omitempty"`
	AssertedAsOf string `json:"asserted_as_of,omitempty"`

	// Structured metadata.
	Meta map[string]any `json:"meta,omitempty"`
}

func (s *Server) handleIntake(w http.ResponseWriter, r *http.Request) {
	var req intakeRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceIntake(r.Context(), &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusCreated, result)
}

// serviceIntake is the unified write service. It triages:
//   - content + collection hint: route to collection (add or link)
//   - content (default): capture as knowledge record
//
// mode="observed" was retired with the observe pipeline; sessions
// (gramaton_session_prepare/commit) are the supported autonomous
// capture path.
func (s *Server) serviceIntake(ctx context.Context, req *intakeRequest) (map[string]any, *serviceError) {
	if req.Content == "" {
		return nil, errMissing("content is required")
	}
	if req.Mode == "observed" {
		return nil, errInvalid(`mode="observed" was removed; use gramaton_session_prepare/commit for autonomous capture`)
	}

	// Build a captureRequest from the intake fields.
	capReq := &captureRequest{
		Content:                req.Content,
		Temporality:            req.Temporality,
		Confidence:             req.Confidence,
		KnowledgeType:          req.KnowledgeType,
		Keywords:               req.Keywords,
		SummaryShort:           req.SummaryShort,
		SourceRef:              req.SourceRef,
		AssertedAsOf:           req.AssertedAsOf,
		ContextAbout:           req.ContextAbout,
		ContextWho:             req.ContextWho,
		ContextFindable:        req.ContextFindable,
		ContextSourceType:      req.ContextSourceType,
		ContextTimeSensitivity: req.ContextTimeSensitivity,
		ContextReliability:     req.ContextReliability,
		ContextCaptureReason:   req.ContextCaptureReason,
		Meta:                   req.Meta,
	}

	result, svcErr := s.serviceCapture(ctx, capReq)
	if svcErr != nil {
		return nil, svcErr
	}

	result["route"] = "knowledge"
	return result, nil
}
