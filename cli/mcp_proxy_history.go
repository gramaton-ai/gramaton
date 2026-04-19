package cli

import (
	"context"
	"fmt"
	"net/url"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerHistoryProxyTools(mcpServer *mcp.Server) {
	registerLogProxy(mcpServer)
	registerDiffProxy(mcpServer)
}

// --- log ---

type proxyLogInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
}

func registerLogProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_log",
		Description: api.LogDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyLogInput) (*mcp.CallToolResult, any, error) {
		path := "/v1/log"
		if args.Limit > 0 {
			path += "?limit=" + fmt.Sprintf("%d", args.Limit)
		}
		return proxyGet(path)
	})
}

// --- diff ---

type proxyDiffInput struct {
	Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD or RFC3339); empty means against chain root"`
	Topic string `json:"topic,omitempty" jsonschema:"filter by topic substring (matches content_keywords + content_short, case-insensitive)"`
	Limit int    `json:"limit,omitempty" jsonschema:"max changes to return (default 50, max 1000)"`
}

func registerDiffProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: api.DiffDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyDiffInput) (*mcp.CallToolResult, any, error) {
		params := url.Values{}
		if args.Since != "" {
			params.Set("since", args.Since)
		}
		if args.Topic != "" {
			params.Set("topic", args.Topic)
		}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		path := "/v1/diff"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}
