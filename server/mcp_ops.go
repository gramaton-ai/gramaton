package server

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/curation"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPOpsTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_pending",
		Description: "List records awaiting classification (processing_status=captured).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_pending")
		defer done(nil)
		result, _ := s.servicePending(50)
		return mcpJSONResult(result)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_status",
		Description: "Get store health: node/edge counts, embedding status, curation status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_status")
		defer done(nil)
		s.engine.RLock()
		defer s.engine.RUnlock()

		curation := computeCuration(s.engine, s.runner, s.usageTracker)
		return mcpJSONResult(map[string]any{
			"nodes":     s.engine.Graph().NodeCount(),
			"edges":     s.engine.Graph().EdgeCount(),
			"embedding": s.engine.Embedder() != nil,
			"curation":  curation,
		})
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_stats",
		Description: "Get aggregate statistics: counts by temporality, knowledge_type, epistemic_status, confidence distribution, and LLM usage.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_stats")
		defer done(nil)
		result, _ := s.serviceStats()
		resp := map[string]any{"stats": result}
		if s.usageTracker != nil {
			resp["llm_usage"] = s.usageTracker.Summary()
		}
		return mcpJSONResult(resp)
	})

	type reembedInput struct {
		Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_reembed",
		Description: "Regenerate stale embeddings (model changed or missing).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reembedInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_reembed")
		defer done(nil)
		result, svcErr := s.serviceReembed(ctx, args.Batch)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type observeInput struct {
		Messages []observeMessage `json:"messages,omitempty" jsonschema:"conversation turns [{role, content}]. Server extracts facts (requires LLM)."`
		Facts    []string         `json:"facts,omitempty" jsonschema:"pre-extracted facts. Server runs quality gates only (no LLM needed)."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_observe",
		Description: `DEPRECATED: Use gramaton_session_prepare/commit for knowledge extraction instead. This tool still works but will be removed in a future version.

Send conversation for knowledge extraction. Fire-and-forget: returns immediately, processes async.
Extracted knowledge enters Memory as ephemeral, low-confidence records.`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args observeInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_observe")
		defer done(nil)
		s.log.Warn("gramaton_observe called (deprecated)", "component", "observe")
		result, svcErr := s.serviceObserve(observeRequest{
			Messages: args.Messages,
			Facts:    args.Facts,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		// Inject deprecation warning into response.
		if result != nil {
			result["deprecation_warning"] = "gramaton_observe is deprecated. Use gramaton_session_prepare/commit for session knowledge extraction."
		}
		return mcpJSONResult(result)
	})

	type curationInput struct {
		Action string `json:"action" jsonschema:"status|trigger|dry_run|batch"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_curation",
		Description: "View curation status, trigger a curation cycle, dry-run to preview changes, or batch-classify all pending records.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args curationInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_curation")
		defer done(nil)
		if s.runner == nil {
			return mcpErr("curation is not enabled")
		}

		switch args.Action {
		case "trigger":
			s.runner.Trigger(ctx)
			return mcpJSONResult(map[string]any{
				"triggered": true,
				"status":    s.runner.Status(),
			})
		case "dry_run":
			result := s.runner.TriggerDryRun(ctx)
			return mcpJSONResult(map[string]any{
				"dry_run":         true,
				"planned_changes": result.PlannedChanges,
				"classified":      result.Classified,
				"summaries":       result.SummariesGenerated,
				"llm_calls":       result.LLMCalls,
				"errors":          result.Errors,
			})
		case "batch":
			if s.engine.LLM() == nil {
				return mcpErr("LLM provider is required for batch curation")
			}
			cfg := s.engine.Config()
			result, err := curation.RunBatchClassification(ctx, s.engine, s.engine.LLM(), cfg, s.log)
			if err != nil {
				return mcpErr(fmt.Sprintf("batch failed: %v", err))
			}
			return mcpJSONResult(result)
		default:
			return mcpJSONResult(map[string]any{
				"status":   s.runner.Status(),
				"manifest": s.runner.Manifest(),
			})
		}
	})

}
