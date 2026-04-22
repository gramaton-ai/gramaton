package server

import (
	"context"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerRecordsRoutes wires the records-cluster HTTP endpoints to
// the api methods. Each closure: parse body/path params -> call api
// method -> write response. No business logic in this file.
func (s *Server) registerRecordsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/records", func(w http.ResponseWriter, r *http.Request) {
		var req api.CaptureRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		resp, apiErr := s.api.Capture(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, resp)
	})

	mux.HandleFunc("GET /v1/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		includeContent := r.URL.Query().Get("include_content") != "false"
		req := api.InspectRequest{
			ID:             r.PathValue("id"),
			IncludeContent: &includeContent,
		}
		resp, apiErr := s.api.Inspect(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("PATCH /v1/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req api.UpdateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		req.ID = r.PathValue("id")
		resp, apiErr := s.api.Update(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /v1/records/{id}/classify", func(w http.ResponseWriter, r *http.Request) {
		var req api.ClassifyRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		req.ID = r.PathValue("id")
		resp, apiErr := s.api.Classify(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /v1/records/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var req api.ResolveRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		req.ID = r.PathValue("id")
		resp, apiErr := s.api.Resolve(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /v1/records/{id}/edges", func(w http.ResponseWriter, r *http.Request) {
		var req api.LinkRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		req.SourceID = r.PathValue("id")
		resp, apiErr := s.api.Link(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, resp)
	})

	mux.HandleFunc("DELETE /v1/edges/{edge_id}", func(w http.ResponseWriter, r *http.Request) {
		resp, apiErr := s.api.Unlink(r.Context(), api.UnlinkRequest{EdgeID: r.PathValue("edge_id")})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("DELETE /v1/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		reason := r.URL.Query().Get("reason")
		resp, apiErr := s.api.DeleteRecord(r.Context(), api.DeleteRecordRequest{
			ID:     r.PathValue("id"),
			Reason: reason,
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /v1/records/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		limit := parseIntParam(r, "limit", 20, maxLogLimit)
		resp, apiErr := s.api.History(r.Context(), api.HistoryRequest{
			ID:    r.PathValue("id"),
			Limit: limit,
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})
}

// registerRecordsMCPTools wires the records-cluster MCP tools to the
// api methods. The args types are api.XxxRequest directly -- no
// per-transport struct redefinition. The jsonschema tags on the
// canonical request structs produce the tool schema.
func (s *Server) registerRecordsMCPTools(mcpServer *mcp.Server) {
	type captureArgs = api.CaptureRequest
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_capture",
		Description: api.CaptureDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args captureArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_capture")
		defer done(nil)
		resp, apiErr := s.api.Capture(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	// Inspect uses a dedicated arg struct because MCP needs the ID
	// as a tool-level field (not path-set). Mirror api.InspectRequest
	// but with the ID tagged for MCP schema.
	type inspectArgs struct {
		ID             string `json:"id" jsonschema:"record ID to inspect"`
		IncludeContent *bool  `json:"include_content,omitempty" jsonschema:"include content_full in response (default true)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: api.InspectDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args inspectArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_inspect")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.Inspect(ctx, api.InspectRequest{ID: args.ID, IncludeContent: args.IncludeContent})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	type updateArgs struct {
		ID              string         `json:"id" jsonschema:"record ID to update"`
		Confidence      *float64       `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		Temporality     string         `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		KnowledgeType   string         `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string         `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance      *float64       `json:"importance,omitempty" jsonschema:"0.0-1.0"`
		Keywords        []string       `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string         `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
		ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration (YYYY-MM-DD or RFC3339); 'clear' removes."`
		AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
		Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_update",
		Description: api.UpdateDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_update")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.Update(ctx, api.UpdateRequest{
			ID: args.ID, Confidence: args.Confidence, Temporality: args.Temporality,
			KnowledgeType: args.KnowledgeType, EpistemicStatus: args.EpistemicStatus,
			Importance: args.Importance, Keywords: args.Keywords, SummaryShort: args.SummaryShort,
			ValidUntil: args.ValidUntil, AssertedAsOf: args.AssertedAsOf, Meta: args.Meta,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	type classifyArgs struct {
		ID              string   `json:"id" jsonschema:"record ID to classify"`
		Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance      *float64 `json:"importance,omitempty" jsonschema:"0.0-1.0"`
		Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_classify",
		Description: api.ClassifyDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args classifyArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_classify")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.Classify(ctx, api.ClassifyRequest{
			ID: args.ID, Temporality: args.Temporality, Confidence: args.Confidence,
			KnowledgeType: args.KnowledgeType, EpistemicStatus: args.EpistemicStatus,
			Importance: args.Importance, Keywords: args.Keywords, SummaryShort: args.SummaryShort,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	type resolveArgs struct {
		ID             string `json:"id" jsonschema:"record ID to resolve"`
		Resolution     string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
		ResolutionNote string `json:"resolution_note,omitempty" jsonschema:"optional free-form note"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_resolve",
		Description: api.ResolveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args resolveArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_resolve")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.Resolve(ctx, api.ResolveRequest{
			ID: args.ID, Resolution: args.Resolution, ResolutionNote: args.ResolutionNote,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	// gramaton_link: source record ID is 'id' (not 'source_id') to
	// preserve the prior MCP wire contract.
	type linkArgs struct {
		ID         string   `json:"id" jsonschema:"source record ID"`
		TargetID   string   `json:"target_id" jsonschema:"destination record ID"`
		EdgeType   string   `json:"edge_type" jsonschema:"relationship name (e.g. related_to, supports, contradicts)"`
		EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0, default 0.5"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_link",
		Description: api.LinkDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args linkArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_link")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.Link(ctx, api.LinkRequest{
			SourceID: args.ID, TargetID: args.TargetID,
			EdgeType: args.EdgeType, EdgeWeight: args.EdgeWeight,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	type unlinkArgs struct {
		EdgeID string `json:"edge_id" jsonschema:"edge ID to delete"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_unlink",
		Description: api.UnlinkDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args unlinkArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_unlink")
		defer done(nil)
		if args.EdgeID == "" {
			return mcpErr("edge_id is required")
		}
		resp, apiErr := s.api.Unlink(ctx, api.UnlinkRequest{EdgeID: args.EdgeID})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	type historyArgs struct {
		ID    string `json:"id" jsonschema:"record ID"`
		Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_history",
		Description: api.HistoryDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args historyArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_history")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		resp, apiErr := s.api.History(ctx, api.HistoryRequest{ID: args.ID, Limit: args.Limit})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})
}
