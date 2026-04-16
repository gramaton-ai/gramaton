package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPRecordTools(mcpServer *mcp.Server) {
	type captureInput struct {
		Content           string   `json:"content" jsonschema:"the knowledge to store (required)"`
		Temporality       string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence        *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
		KnowledgeType     string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus   string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance        *float64 `json:"importance,omitempty" jsonschema:"number between 0.0 and 1.0"`
		Keywords          []string `json:"keywords,omitempty" jsonschema:"array of keyword strings for search"`
		SummaryShort      string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
		SourceRef         string   `json:"source_ref,omitempty" jsonschema:"source URL or path"`
		SourceCredibility *float64 `json:"source_credibility,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ContextAbout      string   `json:"context_about,omitempty" jsonschema:"topic/domain"`
		ContextWho        string   `json:"context_who,omitempty" jsonschema:"entities involved"`
		ContextFindable        string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
		ContextSourceType      string         `json:"context_source_type,omitempty" jsonschema:"what kind of source (e.g. published academic article, personal observation, team discussion)"`
		ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty" jsonschema:"how time-sensitive (e.g. stable reference, changes quarterly, deadline-driven)"`
		ContextReliability     string         `json:"context_reliability,omitempty" jsonschema:"reliability signals (e.g. peer-reviewed, unverified, first-hand experience)"`
		ContextCaptureReason   string         `json:"context_capture_reason,omitempty" jsonschema:"why this is being captured (e.g. recording a decision, building reference corpus)"`
		AssertedAsOf           string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339). Distinct from created_at (when we captured it)."`
		Meta              map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems (e.g. {assignee: Sarah, priority: P1, sprint: 23}). Stored as meta.* properties, indexed for keyword search."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_capture",
		Description: `Store a knowledge record in Memory. Use this ONLY when the user explicitly asks you to remember, save, or capture something. Do not call this tool autonomously -- session extraction (gramaton_session_prepare/commit) handles automatic knowledge capture.

NOT for tasks, action items, checklists, or anything that needs exhaustive tracking. Use gramaton_collection_add for those.

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args captureInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_capture")
		defer done(nil)
		result, svcErr := s.serviceCapture(ctx, &captureRequest{
			Content:           args.Content,
			Temporality:       args.Temporality,
			Confidence:        args.Confidence,
			KnowledgeType:     args.KnowledgeType,
			EpistemicStatus:   args.EpistemicStatus,
			Importance:        args.Importance,
			Keywords:          args.Keywords,
			SummaryShort:      args.SummaryShort,
			SourceRef:         args.SourceRef,
			SourceCredibility: args.SourceCredibility,
			ContextAbout:           args.ContextAbout,
			ContextWho:             args.ContextWho,
			ContextFindable:        args.ContextFindable,
			ContextSourceType:      args.ContextSourceType,
			ContextTimeSensitivity: args.ContextTimeSensitivity,
			ContextReliability:     args.ContextReliability,
			ContextCaptureReason:   args.ContextCaptureReason,
			AssertedAsOf:           args.AssertedAsOf,
			Meta:              args.Meta,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type inspectInput struct {
		ID             string `json:"id" jsonschema:"record ID to inspect"`
		IncludeContent *bool  `json:"include_content,omitempty" jsonschema:"include content_full in response (default true)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: "Get full content, metadata, and related records for a specific record. Set include_content=false for lightweight mode (omits content_full).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args inspectInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_inspect")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		includeContent := args.IncludeContent == nil || *args.IncludeContent
		result, svcErr := s.serviceInspect(args.ID, includeContent)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type updateInput struct {
		ID              string   `json:"id" jsonschema:"record ID to update"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance      *float64 `json:"importance,omitempty" jsonschema:"0.0-1.0"`
		Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
		ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration date (YYYY-MM-DD or RFC3339) -- marks record as historical. Use 'clear' to remove."`
		AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
		Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata (e.g. {assignee: Sarah, status: done})"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_update",
		Description: "Update metadata on a Memory record. For collection item fields, use gramaton_collection_update instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_update")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		result, svcErr := s.serviceUpdate(args.ID, &updateRequest{
			Confidence:      args.Confidence,
			Temporality:     args.Temporality,
			KnowledgeType:   args.KnowledgeType,
			EpistemicStatus: args.EpistemicStatus,
			Importance:      args.Importance,
			Keywords:        args.Keywords,
			SummaryShort:    args.SummaryShort,
			ValidUntil:      args.ValidUntil,
			AssertedAsOf:    args.AssertedAsOf,
			Meta:            args.Meta,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type classifyInput struct {
		ID              string   `json:"id" jsonschema:"record ID to classify"`
		Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
		KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_classify",
		Description: "Classify a pending record with metadata. Sets processing_status to processed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args classifyInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_classify")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		result, svcErr := s.serviceClassify(args.ID, &classifyRequest{
			Temporality:     args.Temporality,
			Confidence:      args.Confidence,
			KnowledgeType:   args.KnowledgeType,
			EpistemicStatus: args.EpistemicStatus,
			Keywords:        args.Keywords,
			SummaryShort:    args.SummaryShort,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type resolveInput struct {
		ID             string `json:"id" jsonschema:"record ID to resolve"`
		Resolution     string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
		ResolutionNote string `json:"resolution_note,omitempty" jsonschema:"brief explanation of why (optional)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_resolve",
		Description: "Mark a knowledge record as resolved. Sets resolution status, resolved_at timestamp, and auto-sets valid_until to deprioritize in search. Use for decisions, questions, or knowledge records with a lifecycle. For task completion in collections, use gramaton_collection_update to change the status field instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args resolveInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_resolve")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		result, svcErr := s.serviceResolve(args.ID, &resolveRequest{
			Resolution:     args.Resolution,
			ResolutionNote: args.ResolutionNote,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type linkInput struct {
		ID         string   `json:"id" jsonschema:"source record ID"`
		TargetID   string   `json:"target_id" jsonschema:"target record ID"`
		EdgeType   string   `json:"edge_type" jsonschema:"relationship type (e.g. related_to, discusses, justifies)"`
		EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0 (default 0.5)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_link",
		Description: "Create an edge between two nodes in the graph. Memory records and Collection items are all graph nodes and can be linked.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args linkInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_link")
		defer done(nil)
		if args.ID == "" {
			return mcpErr("id is required")
		}
		result, svcErr := s.serviceLink(args.ID, &edgeRequest{
			TargetID:   args.TargetID,
			EdgeType:   args.EdgeType,
			EdgeWeight: args.EdgeWeight,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
