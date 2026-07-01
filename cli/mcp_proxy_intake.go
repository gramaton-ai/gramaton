package cli

import (
	"context"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerIntakeProxyTools registers the intake MCP tool for the CLI
// proxy. Uses api.IntakeRequest directly so the field set can't drift
// between HTTP / MCP / CLI-proxy.
func registerIntakeProxyTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_intake",
		Description: api.IntakeDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.IntakeRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/intake", args)
	})
}
