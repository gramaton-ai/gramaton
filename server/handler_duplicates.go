package server

import (
	"net/http"
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

	result, svcErr := s.serviceDuplicates(req.Threshold, req.MaxPairs)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}
