package server

import (
	"net/http"
)

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientSessionID string `json:"client_session_id"`
	}
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceSessionCreate(req.ClientSessionID)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, svcErr := s.serviceSessionGet(id)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionPrepare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, svcErr := s.serviceSessionPrepare(id)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionCommit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		SessionID string          `json:"session_id"`
		Segments  []commitSegment `json:"segments"`
	}
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceSessionCommit(id, req.Segments)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		SessionID  string `json:"session_id"`
		SourcePath string `json:"source_path"`
	}
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceSessionArchive(id, req.SourcePath)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}
