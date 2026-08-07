package cli

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSessionProxyTools registers the sessions-cluster MCP tools
// for the CLI proxy. Uses api.SaveSegment directly so the segment
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
		SessionID    string            `json:"session_id" jsonschema:"session ID to commit segments to"`
		Segments     []api.SaveSegment `json:"segments" jsonschema:"array of extracted knowledge segments"`
		AllowSimilar bool              `json:"allow_similar,omitempty" jsonschema:"disable similar-record promotion holds for this whole commit. Bulk-ingestion escape only; never a standing default"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_save",
		Description: api.SessionSaveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionCommitArgs) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/save", args.SessionID), args)
	})

	type sessionResolveHeldArgs struct {
		SessionID   string               `json:"session_id" jsonschema:"session whose held promotions to resolve"`
		Resolutions []api.HeldResolution `json:"resolutions" jsonschema:"resolutions to apply"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_resolve_held",
		Description: api.SessionResolveHeldDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionResolveHeldArgs) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/resolve-held", args.SessionID), args)
	})
}
