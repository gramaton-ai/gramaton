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
	ImportanceMin     *float64 `json:"importance_min,omitempty"`
	ImportanceMax     *float64 `json:"importance_max,omitempty"`
	Temporality       string   `json:"temporality,omitempty"`
	KnowledgeType     string   `json:"knowledge_type,omitempty"`
	EpistemicStatus   string   `json:"epistemic_status,omitempty"`
	IncludeHistorical bool     `json:"include_historical,omitempty"`
	Since             string   `json:"since,omitempty"`
	Missing           []string `json:"missing,omitempty"`
	Keywords            []string `json:"keywords,omitempty"`
	AccessCountMin      *int64   `json:"access_count_min,omitempty"`
	AccessCountMax      *int64   `json:"access_count_max,omitempty"`
	LastAccessedAfter   string   `json:"last_accessed_after,omitempty"`
	LastAccessedBefore  string   `json:"last_accessed_before,omitempty"`
	ValidAfter          string   `json:"valid_after,omitempty"`
	ValidBefore         string   `json:"valid_before,omitempty"`
	ExpiresAfter        string   `json:"expires_after,omitempty"`
	ExpiresBefore       string   `json:"expires_before,omitempty"`
	Match               string   `json:"match,omitempty"`
	SimilarTo           string   `json:"similar_to,omitempty"`
	MinEdges            *int     `json:"min_edges,omitempty"`
	MaxEdges            *int     `json:"max_edges,omitempty"`
	Random              bool     `json:"random,omitempty"`
	Sort                string   `json:"sort,omitempty"`
	Order             string   `json:"order,omitempty"`
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
	if req.Top > maxSearchTop {
		req.Top = maxSearchTop
	}

	// Validate sort and order.
	if req.Sort != "" && !search.ValidSort(req.Sort) {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "invalid sort field", true)
		return
	}
	if req.Order != "" && req.Order != "asc" && req.Order != "desc" {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "order must be asc or desc", true)
		return
	}

	// Validate array bounds.
	if err := validateKeywords(req.Keywords); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
		return
	}
	if len(req.Missing) > maxMissingFields {
		s.writeError(w, http.StatusBadRequest, "invalid_field",
			fmt.Sprintf("maximum %d missing fields allowed", maxMissingFields), true)
		return
	}

	// Validate match string length.
	if len(req.Match) > maxMatchLength {
		s.writeError(w, http.StatusBadRequest, "invalid_field",
			fmt.Sprintf("match string exceeds maximum length of %d", maxMatchLength), true)
		return
	}

	// Validate float64 ranges.
	for _, v := range []struct {
		name string
		val  *float64
	}{
		{"confidence_min", req.ConfidenceMin},
		{"confidence_max", req.ConfidenceMax},
		{"importance_min", req.ImportanceMin},
		{"importance_max", req.ImportanceMax},
	} {
		if err := validateFloat64Range(v.name, v.val, 0, 1); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
			return
		}
	}

	// Validate integer ranges.
	if req.AccessCountMin != nil && *req.AccessCountMin < 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "access_count_min must be >= 0", true)
		return
	}
	if req.AccessCountMax != nil && *req.AccessCountMax < 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "access_count_max must be >= 0", true)
		return
	}
	if req.MinEdges != nil && *req.MinEdges < 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "min_edges must be >= 0", true)
		return
	}
	if req.MaxEdges != nil && *req.MaxEdges < 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "max_edges must be >= 0", true)
		return
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
		ImportanceMin:     req.ImportanceMin,
		ImportanceMax:     req.ImportanceMax,
		Missing:           req.Missing,
		Keywords:          req.Keywords,
		AccessCountMin:    req.AccessCountMin,
		AccessCountMax:    req.AccessCountMax,
		Match:             req.Match,
		SimilarTo:         req.SimilarTo,
		MinEdges:          req.MinEdges,
		MaxEdges:          req.MaxEdges,
		Random:            req.Random,
		Sort:              req.Sort,
		Order:             req.Order,
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
	if req.LastAccessedAfter != "" {
		t, err := parseDateArg(req.LastAccessedAfter)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field",
				fmt.Sprintf("invalid last_accessed_after date: %s", err), true)
			return
		}
		q.LastAccessedAfter = &t
	}
	if req.LastAccessedBefore != "" {
		t, err := parseDateArg(req.LastAccessedBefore)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field",
				fmt.Sprintf("invalid last_accessed_before date: %s", err), true)
			return
		}
		q.LastAccessedBefore = &t
	}
	for _, pair := range []struct {
		raw  string
		name string
		dest **time.Time
	}{
		{req.ValidAfter, "valid_after", &q.ValidAfter},
		{req.ValidBefore, "valid_before", &q.ValidBefore},
		{req.ExpiresAfter, "expires_after", &q.ExpiresAfter},
		{req.ExpiresBefore, "expires_before", &q.ExpiresBefore},
	} {
		if pair.raw != "" {
			t, err := parseDateArg(pair.raw)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_field",
					fmt.Sprintf("invalid %s date: %s", pair.name, err), true)
				return
			}
			*pair.dest = &t
		}
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

	// Track retrieved IDs for observe feedback loop detection.
	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		s.retrieval.Track(ids...)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"facets":  search.ComputeFacets(results),
	})
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
	if req.Depth > maxExploreDepth {
		req.Depth = maxExploreDepth
	}
	if len(req.EdgeTypes) > maxEdgeTypes {
		s.writeError(w, http.StatusBadRequest, "invalid_field",
			fmt.Sprintf("maximum %d edge types allowed", maxEdgeTypes), true)
		return
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

	// Track explored IDs for observe feedback loop detection.
	ids := make([]string, 0, len(sub.Nodes)+1)
	ids = append(ids, req.NodeID)
	for _, n := range sub.Nodes {
		ids = append(ids, n.ID)
	}
	s.retrieval.Track(ids...)

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
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339")
}
