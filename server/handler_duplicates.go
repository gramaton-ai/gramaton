package server

import (
	"net/http"

	"github.com/brandonlattin/gramaton/search"
)

type duplicatesRequest struct {
	Threshold float64 `json:"threshold,omitempty"`
	MaxPairs  int     `json:"max_pairs,omitempty"`
}

func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	var req duplicatesRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.Threshold <= 0 || req.Threshold > 1.0 {
		req.Threshold = 0.92
	}
	if req.MaxPairs <= 0 {
		req.MaxPairs = 50
	}
	if req.MaxPairs > maxDuplicatePairs {
		req.MaxPairs = maxDuplicatePairs
	}

	s.engine.RLock()
	pairs := search.FindDuplicates(s.engine.Graph(), s.engine.VecIdx(), req.Threshold, req.MaxPairs)
	s.engine.RUnlock()

	if pairs == nil {
		pairs = []search.DuplicatePair{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"pairs":     pairs,
		"threshold": req.Threshold,
		"count":     len(pairs),
	})
}
