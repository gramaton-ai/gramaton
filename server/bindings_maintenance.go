package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerMaintenanceRoutes wires curation + reembed HTTP endpoints to
// the api package. These are pathless admin operations: they mutate
// or read store state and spend (capped) LLM budget, but take no
// caller filesystem path, so they are open to authenticated remote
// callers (the global auth middleware gates them). Only the
// path-taking and process-control operations stay loopback-only (see
// adminAllowed / the shutdown+debug handlers).
func (s *Server) registerMaintenanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/curation", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CurationStatus(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/curation/trigger", func(w http.ResponseWriter, r *http.Request) {
		// Body is optional; the dry_run flag selects DryRun over Trigger.
		// Real parse failures (malformed JSON, oversized body) surface
		// as 400 -- silently defaulting to a real trigger when the
		// caller asked for dry-run via a malformed body would be a foot
		// gun.
		var body struct {
			DryRun bool `json:"dry_run"`
		}
		if err := parseJSON(r, &body, getMaxJSONSize()); err != nil && !errors.Is(err, errEmptyBody) {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		if body.DryRun {
			result, apiErr := s.api.CurationDryRun(r.Context())
			if apiErr != nil {
				s.writeAPIError(w, apiErr)
				return
			}
			s.writeJSON(w, http.StatusOK, result)
			return
		}
		result, apiErr := s.api.CurationTrigger(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/curation/batch", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CurationBatch(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/curation/drain", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CurationDrainContradictions(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /v1/curation/stuck-records", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CurationListStuck(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/curation/stuck-records/reset", func(w http.ResponseWriter, r *http.Request) {
		var req api.CurationResetStuckRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil && !errors.Is(err, errEmptyBody) {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CurationResetStuck(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/reembed", func(w http.ResponseWriter, r *http.Request) {
		var req api.ReembedRequest
		// Body is optional -- no required fields. But if a body IS
		// sent, it had better be valid JSON; silently defaulting on
		// a malformed body would hide caller mistakes.
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil && !errors.Is(err, errEmptyBody) {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.Reembed(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})
}

// registerMaintenanceMCPTools wires gramaton_curation + gramaton_reembed.
// gramaton_curation keeps its single `action` argument and dispatches
// across the four api.Curation* methods so the MCP surface is
// backward-compatible.
func (s *Server) registerMaintenanceMCPTools(mcpServer *mcp.Server) {
	type curationArgs struct {
		Action string `json:"action,omitempty" jsonschema:"status|trigger|dry_run|batch|drain_contradictions (default: status)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_curation",
		Description: api.CurationDescription,
		Meta:        MCPAlwaysLoadMeta(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args curationArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_curation")
		defer done(nil)
		switch args.Action {
		case "trigger":
			result, apiErr := s.api.CurationTrigger(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "dry_run":
			result, apiErr := s.api.CurationDryRun(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "batch":
			result, apiErr := s.api.CurationBatch(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "drain_contradictions":
			result, apiErr := s.api.CurationDrainContradictions(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "", "status":
			result, apiErr := s.api.CurationStatus(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		default:
			return mcpAPIErr(api.ErrInvalid("action must be one of: status, trigger, dry_run, batch, drain_contradictions"))
		}
	})

	type reembedArgs struct {
		Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50, max 500)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_reembed",
		Description: api.ReembedDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reembedArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_reembed")
		defer done(nil)
		result, apiErr := s.api.Reembed(ctx, api.ReembedRequest{Batch: args.Batch})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})
}
