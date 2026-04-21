package cli

import (
	"context"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerMaintenanceProxyTools(mcpServer *mcp.Server) {
	registerCurationProxy(mcpServer)
	registerReembedProxy(mcpServer)
}

// --- curation ---

type proxyCurationInput struct {
	Action string `json:"action,omitempty" jsonschema:"status|trigger|dry_run|batch|drain_contradictions (default: status)"`
}

func registerCurationProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_curation",
		Description: "View or drive the curation runner. action=status returns the current state and manifest. action=trigger runs a cycle now. action=dry_run previews what an autonomous cycle would do without applying changes. action=batch classifies every pending record (LLM required). action=drain_contradictions artificially marks every in-window contradiction-candidate pair as no_contradiction without calling the LLM; see design-decisions.md D38.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCurationInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "trigger":
			return proxyPost("/v1/curation/trigger", nil)
		case "dry_run":
			return proxyPost("/v1/curation/trigger", map[string]bool{"dry_run": true})
		case "batch":
			return proxyPostSlow("/v1/curation/batch", nil)
		case "drain_contradictions":
			return proxyPostSlow("/v1/curation/drain", nil)
		case "", "status":
			return proxyGet("/v1/curation")
		default:
			return proxyErr("action must be one of: status, trigger, dry_run, batch, drain_contradictions")
		}
	})
}

// --- reembed ---

type proxyReembedInput struct {
	Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50, max 500)"`
}

func registerReembedProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_reembed",
		Description: api.ReembedDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyReembedInput) (*mcp.CallToolResult, any, error) {
		return proxyPostSlow("/v1/reembed", args)
	})
}
