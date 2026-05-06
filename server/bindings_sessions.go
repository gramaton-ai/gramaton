package server

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSessionsRoutes wires session HTTP endpoints to the api.
// Session state moved to the api package; see api/sessions.go.
func (s *Server) registerSessionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ClientSessionID string `json:"client_session_id"`
			Source          string `json:"source,omitempty"`
		}
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.SessionStart(r.Context(), req.ClientSessionID, req.Source)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, result)
	})

	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		result, apiErr := s.api.SessionGet(r.Context(), id)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/sessions/{id}/prepare", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		result, apiErr := s.api.SessionPrepare(r.Context(), id)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/sessions/{id}/commit", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			SessionID string               `json:"session_id"`
			Segments  []api.CommitSegment  `json:"segments"`
		}
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.SessionCommit(r.Context(), id, req.Segments)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/sessions/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		// Loopback gate: SessionArchive reads a caller-supplied path.
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
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		if req.SourcePath == "" {
			s.writeError(w, http.StatusBadRequest, "missing_field", "source_path is required", true)
			return
		}
		if !filepath.IsAbs(req.SourcePath) {
			s.writeError(w, http.StatusBadRequest, "input_error",
				"source_path must be absolute", true)
			return
		}
		result, apiErr := s.api.SessionArchive(r.Context(), id, req.SourcePath)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})
}

// registerSessionsMCPTools wires the session MCP tools.
func (s *Server) registerSessionsMCPTools(mcpServer *mcp.Server) {
	type sessionStartArgs struct {
		ClientSessionID string `json:"client_session_id" jsonschema:"unique session identifier from the client (e.g. Claude Code session ID)"`
		Source          string `json:"source,omitempty" jsonschema:"startup|resume -- controls session chaining. Omit for idempotent lookup."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_start",
		Description: api.SessionStartDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionStartArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_start")
		defer done(nil)
		result, apiErr := s.api.SessionStart(ctx, args.ClientSessionID, args.Source)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type sessionGetArgs struct {
		SessionID string `json:"session_id" jsonschema:"session ID to retrieve"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_get",
		Description: api.SessionGetDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionGetArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_get")
		defer done(nil)
		result, apiErr := s.api.SessionGet(ctx, args.SessionID)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type sessionPrepareArgs struct {
		SessionID string `json:"session_id" jsonschema:"session ID to prepare extraction for"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_prepare",
		Description: api.SessionPrepareDescription,
		Meta:        mcpAlwaysLoadMeta(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionPrepareArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_prepare")
		defer done(nil)
		result, apiErr := s.api.SessionPrepare(ctx, args.SessionID)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type sessionCommitArgs struct {
		SessionID string               `json:"session_id" jsonschema:"session ID to commit segments to"`
		Segments  []api.CommitSegment  `json:"segments" jsonschema:"array of extracted knowledge segments"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_commit",
		Description: api.SessionCommitDescription,
		Meta:        mcpAlwaysLoadMeta(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionCommitArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_commit")
		defer done(nil)
		result, apiErr := s.api.SessionCommit(ctx, args.SessionID, args.Segments)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})
}
