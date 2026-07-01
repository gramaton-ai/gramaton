package cli

import (
	"context"
	"net/url"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGuideProxyTools registers the guide MCP tool for the CLI
// proxy. Uses api.GuideRequest directly so the field set can't drift
// between HTTP / MCP / CLI-proxy.
func registerGuideProxyTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_guide",
		Description: api.GuideDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.GuideRequest) (*mcp.CallToolResult, any, error) {
		if args.Topic != "" {
			return proxyGet("/v1/guide?topic=" + url.QueryEscape(args.Topic))
		}
		return proxyGet("/v1/guide")
	})
}
