package api

// IntakeRequest is the agent-facing wire contract for the intake
// operation (POST /v1/intake and the gramaton_intake MCP tool),
// shared by the in-process MCP registration and the CLI MCP proxy so
// the field sets can't drift. The operation itself has not been
// migrated to this package yet -- server.serviceIntake remains the
// implementation, and the HTTP handler keeps its own parse struct
// (which additionally accepts classification hints and the retired
// mode tombstone).
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
	SummaryShort           string         `json:"summary_short,omitempty" jsonschema:"~750 char summary (semantic anchor for embedding)"`
	SourceRef              string         `json:"source_ref,omitempty" jsonschema:"source URL or path"`
	AssertedAsOf           string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339)"`
	Meta                   map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems"`
}

// IntakeDescription is the MCP tool description shared by the
// in-process MCP registration and the CLI MCP proxy.
const IntakeDescription = `Deliberate write endpoint for Memory. Provide content and optional context signals. If the server has an LLM, it classifies automatically. Otherwise the record is stored as "captured" and classified later via gramaton_classify or curation.

This tool stores records in Memory (ranked semantic search). NOT for tasks, TODOs, or checklists -- use gramaton_collection_add for those. For session-scoped conversation capture, use gramaton_session_prepare/save.

You can provide classification metadata (temporality, confidence, etc.) OR provide context signals and let the server classify. Both work. Field roles: content unbounded and self-contained; summary_short ~750 chars is the embedding-ready semantic anchor; keywords are BM25 terms a future agent would type.

Example:
  gramaton_intake(content="We decided to use PostgreSQL because of foreign-key support and JSONB", context_capture_reason="recording architecture decision")`
