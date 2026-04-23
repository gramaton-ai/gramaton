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
	Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
	Since string `json:"since,omitempty" jsonschema:"only include commits on or after this date (YYYY-MM-DD or RFC3339)"`
	Until string `json:"until,omitempty" jsonschema:"only include commits up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
}

func registerLogProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_log",
		Description: api.LogDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyLogInput) (*mcp.CallToolResult, any, error) {
		params := url.Values{}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		if args.Since != "" {
			params.Set("since", args.Since)
		}
		if args.Until != "" {
			params.Set("until", args.Until)
		}
		path := "/v1/log"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}

// --- diff ---

type proxyDiffInput struct {
	Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD or RFC3339); empty means against chain root"`
	Until string `json:"until,omitempty" jsonschema:"show changes up to date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
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
		if args.Until != "" {
			params.Set("until", args.Until)
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
