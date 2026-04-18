package server

import (
	"context"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSearchRoutes wires the search + ops cluster HTTP endpoints.
func (s *Server) registerSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/search", func(w http.ResponseWriter, r *http.Request) {
		var req api.SearchRequest
		if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		resp, apiErr := s.api.Search(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /v1/explore", func(w http.ResponseWriter, r *http.Request) {
		var req api.ExploreRequest
		if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		resp, apiErr := s.api.Explore(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /v1/pending", func(w http.ResponseWriter, r *http.Request) {
		limit := parseIntParam(r, "limit", 50, 500)
		resp, apiErr := s.api.Pending(r.Context(), api.PendingRequest{Limit: limit})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		resp, apiErr := s.api.Stats(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		// The HTTP status endpoint returns a richer shape than api.Status
		// to preserve existing clients' wire contract (store.* + branch +
		// embedding.{provider,model,healthy}). The core nodes/edges come
		// from api.Status so the source of truth for counts stays there.
		resp, apiErr := s.api.Status(r.Context(), api.StatusRequest{})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.engine.RLock()
		defer s.engine.RUnlock()
		storeChunks, _ := s.engine.Store().List()
		storeName := s.cfg.StoreName
		if storeName == "" {
			storeName = "(default)"
		}
		status := map[string]any{
			"store": map[string]any{
				"name":    storeName,
				"nodes":   resp.Nodes,
				"edges":   resp.Edges,
				"commits": len(storeChunks),
			},
			"branch": core.ActiveBranch(s.engine.Config().DataDir),
			"embedding": map[string]any{
				"provider": s.engine.Config().Embedding.Provider,
				"model":    s.engine.Config().Embedding.Model,
				"healthy":  resp.Embedding,
			},
		}
		s.writeJSONLocked(w, http.StatusOK, status)
	})

	mux.HandleFunc("POST /v1/duplicates", func(w http.ResponseWriter, r *http.Request) {
		var req api.DuplicatesRequest
		if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		resp, apiErr := s.api.Duplicates(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})
}

// registerSearchMCPTools wires the search + ops MCP tools.
func (s *Server) registerSearchMCPTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_search",
		Description: api.SearchDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SearchRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_search")
		defer done(nil)
		resp, apiErr := s.api.Search(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_explore",
		Description: api.ExploreDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.ExploreRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_explore")
		defer done(nil)
		resp, apiErr := s.api.Explore(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_pending",
		Description: api.PendingDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.PendingRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_pending")
		defer done(nil)
		resp, apiErr := s.api.Pending(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_stats",
		Description: api.StatsDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_stats")
		defer done(nil)
		resp, apiErr := s.api.Stats(ctx)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		out := map[string]any{"stats": resp}
		if s.usageTracker != nil {
			out["llm_usage"] = s.usageTracker.Summary()
		}
		return mcpJSONResult(out)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_status",
		Description: api.StatusDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_status")
		defer done(nil)
		resp, apiErr := s.api.Status(ctx, api.StatusRequest{})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		// Preserve the prior MCP shape which wraps store-level fields
		// under "store" and includes curation state alongside.
		storeName := s.cfg.StoreName
		if storeName == "" {
			storeName = "(default)"
		}
		out := map[string]any{
			"store": map[string]any{
				"name":  storeName,
				"nodes": resp.Nodes,
				"edges": resp.Edges,
			},
			"embedding": resp.Embedding,
			"curation":  computeCuration(s.engine, s.runner, s.usageTracker),
		}
		return mcpJSONResult(out)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_duplicates",
		Description: api.DuplicatesDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.DuplicatesRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_duplicates")
		defer done(nil)
		resp, apiErr := s.api.Duplicates(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})
}
