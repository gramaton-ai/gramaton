package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerMaintenanceRoutes wires curation + reembed HTTP endpoints to
// the api package. Loopback gates stay at the transport layer because
// the api package has no concept of HTTP origin.
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
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"curation trigger is restricted to loopback connections", false)
			return
		}
		// Body is optional; the dry_run flag selects DryRun over Trigger.
		// Real parse failures (malformed JSON, oversized body) surface
		// as 400 -- silently defaulting to a real trigger when the
		// caller asked for dry-run via a malformed body would be a foot
		// gun.
		var body struct {
			DryRun bool `json:"dry_run"`
		}
		if err := parseJSON(r, &body, maxJSONBodySize); err != nil && !errors.Is(err, errEmptyBody) {
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
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"batch curation is restricted to loopback connections", false)
			return
		}
		result, apiErr := s.api.CurationBatch(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/curation/drain", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"drain is restricted to loopback connections", false)
			return
		}
		result, apiErr := s.api.CurationDrainContradictions(r.Context())
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
		if err := parseJSON(r, &req, maxJSONBodySize); err != nil && !errors.Is(err, errEmptyBody) {
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
		Description: "View or drive the curation runner. action=status returns the current state and manifest. action=trigger runs a cycle now. action=dry_run previews what an autonomous cycle would do without applying changes. action=batch classifies every pending record (LLM required). action=drain_contradictions artificially marks every in-window contradiction-candidate pair as no_contradiction without calling the LLM; see design-decisions.md D38.",
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
