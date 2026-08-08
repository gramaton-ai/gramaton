package api

// IntakeRequest is the wire contract for the intake operation
// (POST /v1/intake, mirrored by the in-process MCP registration in
// server/mcp_intake.go). The operation itself has not been migrated
// to this package yet -- server.serviceIntake remains the
// implementation, and the HTTP handler keeps its own parse struct
// (which additionally accepts classification hints and the retired
// mode tombstone).
//
// What intake is for: it is the write path into Memory for callers
// that don't know the metadata taxonomy. The caller describes the
// source in plain language (context_source_type, context_reliability,
// context_capture_reason, ...) and the server owns the epistemic
// judgment -- an LLM maps the signals onto temporality/confidence/
// epistemic status when configured, otherwise the record lands as
// "captured" and curation classifies it later.
//
// History: intake began (2026-04) as the intended unified front door
// for all writes, replacing the capture/observe/collection_add
// choice. The design moved on to the three storage paths agents are
// taught today (gramaton_save with agent-side classification,
// session prepare/save extraction, collections), which superseded
// intake for agent use. It is retained as the taxonomy-free HTTP
// endpoint for external integrations -- scripts and source systems
// POSTing content without agent-grade classification -- and is
// DELIBERATELY not registered in the agent-facing `gramaton mcp`
// proxy: a second Memory-write tool there would compete with the
// installed three-path guidance (see registerProxyTools in
// cli/mcp_proxy.go and TestProxyIntakeNotExposed).
type IntakeRequest struct {
	Content                string         `json:"content,omitempty" jsonschema:"the knowledge or fact to store"`
	ContextSourceType      string         `json:"context_source_type,omitempty" jsonschema:"what kind of source (e.g. published academic article, personal observation, team discussion)"`
	ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty" jsonschema:"how time-sensitive (e.g. stable reference, changes quarterly, deadline-driven)"`
	ContextReliability     string         `json:"context_reliability,omitempty" jsonschema:"reliability signals (e.g. peer-reviewed, unverified, first-hand experience)"`
	ContextCaptureReason   string         `json:"context_capture_reason,omitempty" jsonschema:"why being captured (e.g. recording a decision, building reference corpus)"`
	ContextAbout           string         `json:"context_about,omitempty" jsonschema:"topic/domain"`
	ContextWho             string         `json:"context_who,omitempty" jsonschema:"entities involved"`
	ContextFindable        string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
	Keywords               []string       `json:"keywords,omitempty" jsonschema:"search keywords"`
	SummaryShort           string         `json:"summary_short,omitempty" jsonschema:"summary, target ~750 chars, max ~900 (semantic anchor for embedding)"`
	SourceRef              string         `json:"source_ref,omitempty" jsonschema:"source URL or path"`
	AssertedAsOf           string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339)"`
	AllowSimilar           []string       `json:"allow_similar,omitempty" jsonschema:"record IDs from a prior hold response, acknowledging the new record is genuinely distinct from them. Only set after a hold; never as a standing default."`
	Meta                   map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems"`
}

// IntakeDescription is the MCP tool description shared by the
// in-process MCP registration and the CLI MCP proxy.
const IntakeDescription = `Deliberate write endpoint for Memory. Provide content and optional context signals. If the server has an LLM, it classifies automatically. Otherwise the record is stored as "captured" and classified later via gramaton_classify or curation.

This tool stores records in Memory (ranked semantic search). NOT for tasks, TODOs, or checklists -- use gramaton_collection_add for those. For session-scoped conversation capture, use gramaton_session_prepare/save.

You can provide classification metadata (temporality, confidence, etc.) OR provide context signals and let the server classify. Both work. Field roles: content unbounded and self-contained; summary_short ~750 chars is the embedding-ready semantic anchor; keywords are BM25 terms a future agent would type.

Example:
  gramaton_intake(content="We decided to use PostgreSQL because of foreign-key support and JSONB", context_capture_reason="recording architecture decision")`
