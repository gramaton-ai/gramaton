package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/search"
)

type searchRequest struct {
	Text              string   `json:"text"`
	Top               int      `json:"top"`
	ConfidenceMin     *float64 `json:"confidence_min,omitempty"`
	ConfidenceMax     *float64 `json:"confidence_max,omitempty"`
	Temporality       string   `json:"temporality,omitempty"`
	KnowledgeType     string   `json:"knowledge_type,omitempty"`
	EpistemicStatus   string   `json:"epistemic_status,omitempty"`
	IncludeHistorical bool     `json:"include_historical,omitempty"`
	Since             string   `json:"since,omitempty"`
}

type exploreRequest struct {
	NodeID    string   `json:"node_id"`
	Depth     int      `json:"depth"`
	EdgeTypes []string `json:"edge_types,omitempty"`
	MinWeight float64  `json:"min_weight,omitempty"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.Top <= 0 {
		req.Top = 10
	}

	q := search.Query{
		Text:              req.Text,
		Top:               req.Top,
		Temporality:       req.Temporality,
		KnowledgeType:     req.KnowledgeType,
		EpistemicStatus:   req.EpistemicStatus,
		IncludeHistorical: req.IncludeHistorical,
		ConfidenceMin:     req.ConfidenceMin,
		ConfidenceMax:     req.ConfidenceMax,
	}

	if req.Since != "" {
		t, err := parseDateArg(req.Since)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field",
				fmt.Sprintf("invalid since date: %s", err), true)
			return
		}
		q.Since = &t
	}

	// Pre-embed the query text outside the lock. Embedding can take
	// seconds (Ollama model load), and holding the lock would block
	// the entire server.
	var queryVec []float32
	if q.Text != "" && s.engine.Embedder() != nil {
		vecs, err := s.engine.Embedder().Embed(context.Background(), []string{q.Text})
		if err == nil && len(vecs) > 0 {
			queryVec = vecs[0]
		}
	}

	// Search under read lock with the pre-computed vector.
	s.engine.RLock()
	results, err := s.engine.Searcher().ExecuteWithVector(context.Background(), q, queryVec)
	s.engine.RUnlock()

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "search_error", "search failed", false)
		return
	}

	// Record access under write lock (fast, no embedding involved).
	if len(results) > 0 {
		s.engine.Lock()
		now := time.Now().UTC()
		cfg := s.engine.Config()
		activationCfg := graph.ActivationConfig{
			BaseAmount:        cfg.Activation.BaseAmount,
			AttenuationFactor: cfg.Activation.AttenuationFactor,
		}
		for _, r := range results {
			s.engine.Graph().RecordAccess(r.ID, now, activationCfg)
		}
		s.engine.Save("access")
		s.engine.Unlock()
	}

	if results == nil {
		results = []search.Result{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleExplore(w http.ResponseWriter, r *http.Request) {
	var req exploreRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.NodeID == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "node_id is required", true)
		return
	}
	if req.Depth <= 0 {
		req.Depth = 2
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	if _, ok := s.engine.Graph().GetNode(req.NodeID); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "record not found", false)
		return
	}

	opts := graph.TraverseOptions{
		MaxDepth:      req.Depth,
		EdgeTypes:     req.EdgeTypes,
		MinEdgeWeight: req.MinWeight,
	}

	sub := s.engine.Graph().Traverse(req.NodeID, opts)
	s.writeJSONLocked(w, http.StatusOK, sub)
}

// parseDateArg parses a date string in YYYY-MM-DD or RFC3339 format.
func parseDateArg(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", s)
}
