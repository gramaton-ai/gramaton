package server

import (
	"net/http"
	"path/filepath"
)

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientSessionID string `json:"client_session_id"`
		Source          string `json:"source,omitempty"`
	}
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceSessionCreate(req.ClientSessionID, req.Source)
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
	// Restricted to loopback: serviceSessionArchive does os.ReadFile
	// on a caller-supplied path. Default bind is 127.0.0.1 so this
	// is normally moot, but a non-loopback bind without this gate
	// would expose arbitrary local-file read. (Wave 2 P1-21.)
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"session archive is restricted to loopback connections", false)
		return
	}

	id := r.PathValue("id")
	var req struct {
		SessionID  string `json:"session_id"`
		SourcePath string `json:"source_path"`
	}
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	// Path must be absolute. Symlinks/traversal still possible but
	// the loopback gate above is the primary defence; this rejects
	// the most common footgun (relative paths interpreted against
	// the server's cwd).
	if req.SourcePath == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "source_path is required", true)
		return
	}
	if !filepath.IsAbs(req.SourcePath) {
		s.writeError(w, http.StatusBadRequest, "input_error",
			"source_path must be absolute", true)
		return
	}
	result, svcErr := s.serviceSessionArchive(id, req.SourcePath)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}
