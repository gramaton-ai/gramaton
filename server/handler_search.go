package server

import (
	"fmt"
	"net/http"
	"time"
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
	Resolution        string   `json:"resolution,omitempty"`
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
	NearNode            string   `json:"near_node,omitempty"`
	MaxHops             int      `json:"max_hops,omitempty"`
	MinEdges            *int     `json:"min_edges,omitempty"`
	MaxEdges            *int     `json:"max_edges,omitempty"`
	Random              bool              `json:"random,omitempty"`
	Sort                string            `json:"sort,omitempty"`
	Order               string            `json:"order,omitempty"`
	Meta                map[string]string `json:"meta,omitempty"`
	Store               string            `json:"store,omitempty"`
}

type exploreRequest struct {
	NodeID    string   `json:"node_id"`
	Depth     int      `json:"depth"`
	EdgeTypes []string `json:"edge_types,omitempty"`
	MinWeight float64  `json:"min_weight,omitempty"`
	MaxNodes  int      `json:"max_nodes,omitempty"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceSearch(r.Context(), &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExplore(w http.ResponseWriter, r *http.Request) {
	var req exploreRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceExplore(&req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
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
