package cli

import (
	"encoding/json"
	"errors"

	"github.com/gramaton-ai/gramaton/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerProxyTools registers all MCP tools as HTTP proxy handlers.
// Each tool forwards its request to the gramaton HTTP server and returns
// the response. The MCP process is stateless -- all state lives in the
// server process.
func registerProxyTools(mcpServer *mcp.Server) {
	// Records cluster: api-typed bindings in mcp_proxy_records.go.
	registerRecordsProxyTools(mcpServer)
	// Search + ops cluster: api-typed bindings in mcp_proxy_search.go.
	// Covers search, explore, duplicates, pending, stats, status.
	registerSearchProxyTools(mcpServer)
	// Maintenance cluster: curation + reembed in mcp_proxy_maintenance.go.
	registerMaintenanceProxyTools(mcpServer)
	// History cluster: log + diff in mcp_proxy_history.go.
	// gramaton_history (per-record) lives in mcp_proxy_records.go.
	registerHistoryProxyTools(mcpServer)
	// Admin cluster: branches + backup in mcp_proxy_admin.go.
	registerAdminProxyTools(mcpServer)
	// registerDeleteProxy intentionally excluded -- destructive operations
	// should not be available to agents via MCP. Use the CLI or HTTP API.
	registerCollectionProxyTools(mcpServer)
	registerSessionProxyTools(mcpServer)
	// gramaton_intake intentionally excluded -- it is the HTTP-only
	// write path (POST /v1/intake) for external integrations that
	// don't know the metadata taxonomy. Agents write through the
	// three storage paths the installed guidance teaches (save /
	// sessions / collections); exposing a second Memory-write tool
	// here would compete with that guidance. See api/intake.go.
	// Guide: api-typed binding in mcp_proxy_guide.go.
	registerGuideProxyTools(mcpServer)
}

// --- helpers ---

func proxyErr(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

// proxyServerErr translates an error from the server HTTP client into an
// MCP tool result. When the underlying error is a typed server.ErrorDetail
// (returned by parseResponse for well-formed 4xx/5xx responses), the
// Code/Retryable fields ride along via StructuredContent so an MCP client
// can branch on them without string-parsing the message.
func proxyServerErr(err error) (*mcp.CallToolResult, any, error) {
	var detail *server.ErrorDetail
	if errors.As(err, &detail) {
		text := detail.Message
		if detail.Code != "" {
			text = detail.Code + ": " + detail.Message
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
			StructuredContent: map[string]any{
				"code":      detail.Code,
				"message":   detail.Message,
				"retryable": detail.Retryable,
			},
			IsError: true,
		}, nil, nil
	}
	return proxyErr(err.Error())
}

func proxyResult(data any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return proxyErr("failed to marshal result")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

func proxyPost(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPost(path, args)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}

func proxyGet(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverGet(path)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}

// Slow variants for I/O-heavy operations (backup, reembed).
func proxyPostSlow(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPostSlow(path, args)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}

func proxyGetSlow(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverGetSlow(path)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}

func proxyPatch(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPatch(path, args)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}

func proxyDelete(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverDelete(path)
	if err != nil {
		return proxyServerErr(err)
	}
	return proxyResult(env.Data)
}
