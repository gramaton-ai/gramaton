package cli

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSearchProxyTools registers the search + ops cluster MCP
// tools for the CLI proxy. Uses api.XxxRequest directly so field sets
// can't drift between HTTP / MCP / CLI-proxy.
func registerSearchProxyTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_search",
		Description: api.SearchDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SearchRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/search", args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_explore",
		Description: api.ExploreDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.ExploreRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/explore", args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_duplicates",
		Description: api.DuplicatesDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.DuplicatesRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/duplicates", args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_pending",
		Description: api.PendingDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.PendingRequest) (*mcp.CallToolResult, any, error) {
		if args.Limit > 0 {
			return proxyGet(fmt.Sprintf("/v1/pending?limit=%d", args.Limit))
		}
		return proxyGet("/v1/pending")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_stats",
		Description: api.StatsDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return proxyGet("/v1/stats")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_status",
		Description: api.StatusDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return proxyGet("/v1/status")
	})
}
