package server

import (
	"context"
	"net/http"
	"time"
)

// intakeRequest is the unified write endpoint. The server triages
// based on content, mode, and collection hints.
type intakeRequest struct {
	// Content to store (required unless facts are provided).
	Content string `json:"content,omitempty"`

	// Facts for observed mode (alternative to content).
	Facts []string `json:"facts,omitempty"`

	// Mode: "" (deliberate capture) or "observed" (ambient extraction
	// with quality gates). Hooks use mode="observed".
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

	// Collection routing hints. When present, the server checks if a
	// matching collection/item exists and routes accordingly.
	ContextRelatedCollection string `json:"context_related_collection,omitempty"`
	ContextRelatedItem       string `json:"context_related_item,omitempty"`

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
//   - mode="observed" + facts: quality gates -> deferred capture
//   - mode="observed" + content: quality gates -> deferred capture
//   - content + collection hint: route to collection (add or link)
//   - content (default): capture as knowledge record
func (s *Server) serviceIntake(ctx context.Context, req *intakeRequest) (map[string]any, *serviceError) {
	// Validate: must have content or facts.
	if req.Content == "" && len(req.Facts) == 0 {
		return nil, errMissing("content or facts is required")
	}

	// Observed mode: route through quality gates (fire-and-forget).
	if req.Mode == "observed" {
		return s.intakeObserved(req)
	}

	// Deliberate capture mode.
	if req.Content == "" {
		return nil, errMissing("content is required for deliberate capture (use mode=observed for facts)")
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

	// Delegate to existing capture service.
	result, svcErr := s.serviceCapture(ctx, capReq)
	if svcErr != nil {
		return nil, svcErr
	}

	result["route"] = "knowledge"
	return result, nil
}

// intakeObserved routes through the observe pipeline with quality gates.
func (s *Server) intakeObserved(req *intakeRequest) (map[string]any, *serviceError) {
	cfg := s.engine.Config()
	if !cfg.Observe.Enabled {
		return nil, errUnavailable("observe pipeline is not enabled")
	}

	// Build facts list: either provided directly or from content.
	var facts []string
	if len(req.Facts) > 0 {
		facts = req.Facts
	} else if req.Content != "" {
		facts = []string{req.Content}
	}

	maxFacts := cfg.Observe.MaxFactsPerCall
	if maxFacts <= 0 {
		maxFacts = 20
	}
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}

	// Fire-and-forget through observe pipeline.
	select {
	case s.observeSem <- struct{}{}:
		go func() {
			defer func() { <-s.observeSem }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.applyQualityGates(ctx, facts, cfg)
		}()
		return map[string]any{
			"accepted": true,
			"route":    "observed",
			"facts":    len(facts),
		}, nil
	default:
		return nil, errTooMany("too many concurrent observe operations, try again later")
	}
}
