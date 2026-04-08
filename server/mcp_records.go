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
		SummaryAbstract   string   `json:"summary_abstract,omitempty" jsonschema:"longer summary"`
		SourceRef         string   `json:"source_ref,omitempty" jsonschema:"source URL or path"`
		SourceCredibility *float64 `json:"source_credibility,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ContextAbout      string   `json:"context_about,omitempty" jsonschema:"topic/domain"`
		ContextWho        string   `json:"context_who,omitempty" jsonschema:"entities involved"`
		ContextFindable   string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
		AssertedAsOf      string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339). Distinct from created_at (when we captured it)."`
		Meta              map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems (e.g. {assignee: Sarah, priority: P1, sprint: 23}). Stored as meta.* properties, indexed for keyword search."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_capture",
		Description: `Store a knowledge record in the graph. Use for decisions, context, research findings, preferences -- things where ranked semantic search is the right retrieval mode.

NOT for tasks, action items, checklists, or anything that needs exhaustive tracking. Use gramaton_collection_add for those.

Example: gramaton_capture(content="User prefers dark mode", temporality="durable", confidence=0.95, knowledge_type="semantic", keywords=["preference", "ui"], summary_short="User prefers dark mode")

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args captureInput) (*mcp.CallToolResult, any, error) {
		result, svcErr := s.serviceCapture(ctx, &captureRequest{
			Content:           args.Content,
			Temporality:       args.Temporality,
			Confidence:        args.Confidence,
			KnowledgeType:     args.KnowledgeType,
			EpistemicStatus:   args.EpistemicStatus,
			Importance:        args.Importance,
			Keywords:          args.Keywords,
			SummaryShort:      args.SummaryShort,
			SummaryAbstract:   args.SummaryAbstract,
			SourceRef:         args.SourceRef,
			SourceCredibility: args.SourceCredibility,
			ContextAbout:      args.ContextAbout,
			ContextWho:        args.ContextWho,
			ContextFindable:   args.ContextFindable,
			AssertedAsOf:      args.AssertedAsOf,
			Meta:              args.Meta,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type inspectInput struct {
		ID string `json:"id" jsonschema:"record ID to inspect"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: "Get full content, metadata, and related records for a specific record.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args inspectInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return mcpErr("id is required")
		}
		result, svcErr := s.serviceInspect(args.ID)
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
		Description: "Update metadata on a knowledge graph record. For collection item fields, use gramaton_collection_update instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateInput) (*mcp.CallToolResult, any, error) {
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
		Description: "Create an edge between two records in the knowledge graph. Collection items are also graph nodes and can be linked.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args linkInput) (*mcp.CallToolResult, any, error) {
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
