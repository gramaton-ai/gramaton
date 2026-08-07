package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

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
		// Actions filter: repeated &action=resolve&action=collection_update
		var actions []string
		if vs := query["action"]; len(vs) > 0 {
			actions = vs
		} else if v := query.Get("actions"); v != "" {
			// Comma-separated fallback for simple callers.
			for _, part := range strings.Split(v, ",") {
				if p := strings.TrimSpace(part); p != "" {
					actions = append(actions, p)
				}
			}
		}
		result, apiErr := s.api.Log(r.Context(), api.LogRequest{
			Limit:                  parseIntParam(r, "limit", 0, api.MaxLogLimit),
			Since:                  query.Get("since"),
			Until:                  query.Get("until"),
			Actions:                actions,
			ExcludeCuration:        query.Get("exclude_curation") == "true",
			IncludeRecordMutations: query.Get("include_record_mutations") == "true",
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/history/search", func(w http.ResponseWriter, r *http.Request) {
		var req api.HistorySearchRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil && !errors.Is(err, errEmptyBody) {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.HistorySearch(r.Context(), req)
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
		Limit                  int      `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
		Since                  string   `json:"since,omitempty" jsonschema:"only include commits on or after this date (YYYY-MM-DD or RFC3339)"`
		Until                  string   `json:"until,omitempty" jsonschema:"only include commits up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
		Actions                []string `json:"actions,omitempty" jsonschema:"filter by CommitAction.Kind (e.g. [resolve, collection_update]). Commit matches if ANY of its actions has a Kind in this list."`
		ExcludeCuration        bool     `json:"exclude_curation,omitempty" jsonschema:"skip commits whose message starts with 'curation:' (server-side curation noise)"`
		IncludeRecordMutations bool     `json:"include_record_mutations,omitempty" jsonschema:"enrich each commit with per-record {record_id, kind, field, title, summary_short} from its CommitAction list (capped at 20 per commit)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_log",
		Description: api.LogDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args logArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_log")
		defer done(nil)
		result, apiErr := s.api.Log(ctx, api.LogRequest{
			Limit:                  args.Limit,
			Since:                  args.Since,
			Until:                  args.Until,
			Actions:                args.Actions,
			ExcludeCuration:        args.ExcludeCuration,
			IncludeRecordMutations: args.IncludeRecordMutations,
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
	type historySearchArgs struct {
		Text   string `json:"text" jsonschema:"lexical query matched against version content and change_notes (case-insensitive substring)"`
		ID     string `json:"id,omitempty" jsonschema:"scan only this record's versions (fastest scope)"`
		Scope  string `json:"scope,omitempty" jsonschema:"'candidates' (default: retrieval nominates records, then their histories are scanned) or 'store' (budgeted scan of every logical version; slow on large stores but finds knowledge revised away entirely)"`
		Budget int    `json:"budget,omitempty" jsonschema:"max version blobs to scan in store scope (default 20000, max 200000)"`
		Since  string `json:"since,omitempty" jsonschema:"only match versions on or after this date (YYYY-MM-DD or RFC3339)"`
		Until  string `json:"until,omitempty" jsonschema:"only match versions up to this date (YYYY-MM-DD or RFC3339)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_history_search",
		Description: api.HistorySearchDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args historySearchArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_history_search")
		defer done(nil)
		result, apiErr := s.api.HistorySearch(ctx, api.HistorySearchRequest{
			Text:   args.Text,
			ID:     args.ID,
			Scope:  args.Scope,
			Budget: args.Budget,
			Since:  args.Since,
			Until:  args.Until,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

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
