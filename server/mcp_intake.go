package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPIntakeTools(mcpServer *mcp.Server) {
	type intakeInput struct {
		Content                string         `json:"content,omitempty" jsonschema:"the knowledge or fact to store"`
		Facts                  []string       `json:"facts,omitempty" jsonschema:"pre-extracted facts for observed mode"`
		Mode                   string         `json:"mode,omitempty" jsonschema:"empty for deliberate capture, 'observed' for ambient extraction with quality gates"`
		ContextSourceType      string         `json:"context_source_type,omitempty" jsonschema:"what kind of source (e.g. published academic article, personal observation, team discussion)"`
		ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty" jsonschema:"how time-sensitive (e.g. stable reference, changes quarterly, deadline-driven)"`
		ContextReliability     string         `json:"context_reliability,omitempty" jsonschema:"reliability signals (e.g. peer-reviewed, unverified, first-hand experience)"`
		ContextCaptureReason   string         `json:"context_capture_reason,omitempty" jsonschema:"why being captured (e.g. recording a decision, building reference corpus)"`
		ContextAbout           string         `json:"context_about,omitempty" jsonschema:"topic/domain"`
		ContextWho             string         `json:"context_who,omitempty" jsonschema:"entities involved"`
		ContextFindable        string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
		Keywords               []string       `json:"keywords,omitempty" jsonschema:"search keywords"`
		SummaryShort           string         `json:"summary_short,omitempty" jsonschema:"~750 char summary (semantic anchor for embedding)"`
		SourceRef              string         `json:"source_ref,omitempty" jsonschema:"source URL or path"`
		AssertedAsOf           string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339)"`
		Meta                   map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_intake",
		Description: `Write endpoint for Memory. Routes to capture (deliberate) or observe (ambient) based on mode.

Deliberate capture: provide content and optional context signals. If the server has an LLM, it classifies automatically. Otherwise the record is stored as "captured" and classified later via gramaton_classify or curation.

Ambient extraction (mode="observed"): SOFT-DEPRECATED. Use gramaton_session_prepare/commit instead -- session extraction has the hot conversation context and produces higher-quality segments with full metadata classification. The observed path remains for batch/non-conversational intake only.

This tool stores records in Memory (ranked semantic search). NOT for tasks, TODOs, or checklists -- use gramaton_collection_add for those. For session-scoped conversation capture, use gramaton_session_prepare/commit.

You can provide classification metadata (temporality, confidence, etc.) OR provide context signals and let the server classify. Both work. Field roles: content unbounded and self-contained; summary_short ~750 chars is the embedding-ready semantic anchor; keywords are BM25 terms a future agent would type.

Example:
  gramaton_intake(content="We decided to use PostgreSQL because of foreign-key support and JSONB", context_capture_reason="recording architecture decision")`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args intakeInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_intake")
		defer done(nil)
		result, svcErr := s.serviceIntake(ctx, &intakeRequest{
			Content:                args.Content,
			Facts:                  args.Facts,
			Mode:                   args.Mode,
			ContextSourceType:      args.ContextSourceType,
			ContextTimeSensitivity: args.ContextTimeSensitivity,
			ContextReliability:     args.ContextReliability,
			ContextCaptureReason:   args.ContextCaptureReason,
			ContextAbout:           args.ContextAbout,
			ContextWho:             args.ContextWho,
			ContextFindable:        args.ContextFindable,
			Keywords:               args.Keywords,
			SummaryShort:           args.SummaryShort,
			SourceRef:              args.SourceRef,
			AssertedAsOf:           args.AssertedAsOf,
			Meta:                   args.Meta,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
