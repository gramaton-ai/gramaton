package server

import (
	"context"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerHistoryRoutes wires log/diff HTTP endpoints to api.
// Per-record change history is exposed via the records cluster at
// /v1/records/{id}/history (see bindings_records.go) -- there is no
// /v1/log?record= alias.
func (s *Server) registerHistoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/log", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result, apiErr := s.api.Log(r.Context(), api.LogRequest{
			Limit: parseIntParam(r, "limit", 0, api.MaxLogLimit),
			Since: query.Get("since"),
			Until: query.Get("until"),
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /v1/diff", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result, apiErr := s.api.Diff(r.Context(), api.DiffRequest{
			Since: query.Get("since"),
			Until: query.Get("until"),
			Topic: query.Get("topic"),
			Limit: parseIntParam(r, "limit", 0, 1000),
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})
}

// registerHistoryMCPTools wires gramaton_log + gramaton_diff.
// gramaton_history stays in bindings_records.go where it already
// lives.
func (s *Server) registerHistoryMCPTools(mcpServer *mcp.Server) {
	type logArgs struct {
		Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
		Since string `json:"since,omitempty" jsonschema:"only include commits on or after this date (YYYY-MM-DD or RFC3339)"`
		Until string `json:"until,omitempty" jsonschema:"only include commits up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_log",
		Description: api.LogDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args logArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_log")
		defer done(nil)
		result, apiErr := s.api.Log(ctx, api.LogRequest{
			Limit: args.Limit,
			Since: args.Since,
			Until: args.Until,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type diffArgs struct {
		Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD or RFC3339); empty means against chain root"`
		Until string `json:"until,omitempty" jsonschema:"show changes up to date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
		Topic string `json:"topic,omitempty" jsonschema:"filter by topic substring (matches content_keywords + content_short, case-insensitive)"`
		Limit int    `json:"limit,omitempty" jsonschema:"max changes to return (default 50, max 1000)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: api.DiffDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args diffArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_diff")
		defer done(nil)
		result, apiErr := s.api.Diff(ctx, api.DiffRequest{
			Since: args.Since,
			Until: args.Until,
			Topic: args.Topic,
			Limit: args.Limit,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})
}
