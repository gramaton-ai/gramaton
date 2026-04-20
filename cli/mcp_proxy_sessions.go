package cli

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSessionProxyTools registers the sessions-cluster MCP tools
// for the CLI proxy. Uses api.CommitSegment directly so the segment
// schema can't drift between transports.
func registerSessionProxyTools(mcpServer *mcp.Server) {
	type sessionStartArgs struct {
		ClientSessionID string `json:"client_session_id" jsonschema:"unique session identifier from the client"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_start",
		Description: api.SessionStartDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionStartArgs) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/sessions", args)
	})

	type sessionGetArgs struct {
		SessionID string `json:"session_id" jsonschema:"session ID to retrieve"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_get",
		Description: api.SessionGetDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionGetArgs) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyGet(fmt.Sprintf("/v1/sessions/%s", args.SessionID))
	})

	type sessionPrepareArgs struct {
		SessionID string `json:"session_id" jsonschema:"session ID to prepare extraction for"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_prepare",
		Description: api.SessionPrepareDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionPrepareArgs) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/prepare", args.SessionID), nil)
	})

	type sessionCommitArgs struct {
		SessionID string               `json:"session_id" jsonschema:"session ID to commit segments to"`
		Segments  []api.CommitSegment  `json:"segments" jsonschema:"array of extracted knowledge segments"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_commit",
		Description: api.SessionCommitDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionCommitArgs) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/commit", args.SessionID), args)
	})
}
