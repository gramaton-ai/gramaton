package cli

import (
	"context"
	"fmt"
	"net/url"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerAdminProxyTools(mcpServer *mcp.Server) {
	registerBranchProxy(mcpServer)
	registerBackupProxy(mcpServer)
}

// --- branch ---

type proxyBranchInput struct {
	Action string `json:"action" jsonschema:"list|create|checkout|merge|discard"`
	Name   string `json:"name,omitempty" jsonschema:"branch name (required for create|checkout|merge|discard)"`
}

func registerBranchProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: list, create, checkout, merge, or discard. Use for safe experimentation, bulk imports, or testing curation changes before merging.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyBranchInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "list":
			return proxyGet("/v1/branches")
		case "create":
			return proxyPost("/v1/branches", api.BranchCreateRequest{Name: args.Name})
		case "checkout":
			return proxyPostSlow(fmt.Sprintf("/v1/branches/%s/checkout", url.PathEscape(args.Name)), nil)
		case "merge":
			return proxyPostSlow(fmt.Sprintf("/v1/branches/%s/merge", url.PathEscape(args.Name)), nil)
		case "discard":
			return proxyDelete(fmt.Sprintf("/v1/branches/%s", url.PathEscape(args.Name)))
		default:
			return proxyErr("action must be one of: list, create, checkout, merge, discard")
		}
	})
}

// --- backup ---

type proxyBackupInput struct {
	Action string `json:"action,omitempty" jsonschema:"backup|status (default: status)"`
}

func registerBackupProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_backup",
		Description: "Create a snapshot-consistent backup or list existing backups. action=backup creates a new tar.gz archive; action=status (default) lists existing archives.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyBackupInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "backup":
			return proxyPostSlow("/v1/backup", nil)
		case "", "status":
			return proxyGetSlow("/v1/backup")
		default:
			return proxyErr("action must be one of: backup, status")
		}
	})
}
