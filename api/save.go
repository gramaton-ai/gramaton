package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
)

// SaveRequest is the canonical input to the capture operation.
// Every transport (HTTP, MCP, CLI proxy) uses this struct directly --
// there is no per-transport copy to drift from.
//
// json tags are the HTTP wire format.
// jsonschema tags surface as MCP tool descriptions (the MCP SDK reads
// them via reflection when the struct is passed as a tool args type).
type SaveRequest struct {
	Content                string         `json:"content" jsonschema:"the knowledge to store (required)"`
	Temporality            string         `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	Confidence             *float64       `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
	KnowledgeType          string         `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus        string         `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Importance             *float64       `json:"importance,omitempty" jsonschema:"number between 0.0 and 1.0"`
	Keywords               []string       `json:"keywords,omitempty" jsonschema:"array of keyword strings for search"`
	SummaryShort           string         `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
	SourceRef              string         `json:"source_ref,omitempty" jsonschema:"source URL or path"`
	SourceCredibility      *float64       `json:"source_credibility,omitempty" jsonschema:"number between 0.0 and 1.0"`
	TestimonyHops          *int64         `json:"testimony_hops,omitempty" jsonschema:"how many people removed from the first-hand source (0=first-hand, 1=heard from someone who was there, etc.)"`
	ContextAbout           string         `json:"context_about,omitempty" jsonschema:"topic/domain"`
	ContextWho             string         `json:"context_who,omitempty" jsonschema:"entities involved"`
	ContextPrompted        string         `json:"context_prompted,omitempty" jsonschema:"what prompted this capture"`
	ContextFindable        string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
	ContextRelated         string         `json:"context_related,omitempty" jsonschema:"related concepts or records"`
	ContextSourceType      string         `json:"context_source_type,omitempty" jsonschema:"what kind of source (e.g. published academic article, personal observation, team discussion)"`
	ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty" jsonschema:"how time-sensitive (e.g. stable reference, changes quarterly, deadline-driven)"`
	ContextReliability     string         `json:"context_reliability,omitempty" jsonschema:"reliability signals (e.g. peer-reviewed, unverified, first-hand experience)"`
	ContextCaptureReason   string         `json:"context_capture_reason,omitempty" jsonschema:"why this is being captured (e.g. recording a decision, building reference corpus)"`
	ValidFrom              string         `json:"valid_from,omitempty" jsonschema:"RFC3339; optional lower lifecycle bound"`
	ValidUntil             string         `json:"valid_until,omitempty" jsonschema:"RFC3339; optional expiration"`
	AssertedAsOf           string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339). Distinct from created_at (when we captured it)."`
	Meta                   map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems (e.g. {assignee: Sarah, priority: P1, sprint: 23}). Stored as meta.* properties, indexed for keyword search."`
}

// SupersededRecord describes a record that Capture automatically marked
// as historical because the new record was near-duplicate.
type SupersededRecord struct {
	ID         string  `json:"id"`
	Summary    string  `json:"summary,omitempty"`
	Similarity float64 `json:"similarity"`
	EdgeID     string  `json:"edge_id"`
}

// SaveResponse is the canonical output of the capture operation.
// Omitted fields use json:",omitempty" so the wire format stays tight
// on the happy path (no warnings, no supersession).
type SaveResponse struct {
	ID         string             `json:"id"`
	Warnings   []string           `json:"warnings,omitempty"`
	Superseded []SupersededRecord `json:"superseded,omitempty"`
}

// SaveDescription is the MCP tool description shared by every
// transport that surfaces save (direct MCP registration and the
// CLI MCP proxy). Changes here update both surfaces.
const SaveDescription = `Save a single record to persistent semantic memory. Use when the user explicitly hands you content to remember/save/store, or when you want to persist a specific fact, decision, or observation. Single record per call.

NOT for saving the session/conversation itself, or for extracting knowledge from ongoing conversation -- use gramaton_session_prepare + gramaton_session_save for those. NOT for tasks/checklists -- use gramaton_collection_add.

Field roles: content is unbounded and should be self-contained with rationale; summary_short (~750 chars) is the embedding-ready semantic anchor for vector search; keywords are BM25 terms a future agent would type. These are different outputs serving different parts of retrieval, not nested compressions. For full guidance on what to save, classification heuristics per question type, and synthesis-not-summarization discipline, call gramaton_guide(topic="save").

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).`

// Capture creates a new knowledge record. Pre-embeds content_short
// outside the engine write lock, then holds the lock for the minimum
// time needed to insert the node, attach the embedding, check dedup,
// and save. Auto-supersession: if the captured record is a
// near-duplicate (cosine >= dedup.threshold) of an existing record,
// the older one is marked historical and a "supersedes" edge links
// the new record to it. Returns ErrConflict only when dedup.action =
// "reject" AND a duplicate is found.
func (a *API) Save(ctx context.Context, req SaveRequest) (SaveResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("save"); apiErr != nil {
		return SaveResponse{}, apiErr
	}
	saveStart := time.Now()

	if req.Content == "" {
		return SaveResponse{}, ErrMissing("content is required")
	}
	if len(req.Content) > a.engine.Config().Limits.MaxContentLength {
		return SaveResponse{}, ErrInvalid("content exceeds maximum length")
	}
	if err := validateSaveRequest(&req); err != nil {
		return SaveResponse{}, ErrInvalid(err.Error())
	}
	if err := validateMeta(req.Meta); err != nil {
		return SaveResponse{}, ErrInvalid(err.Error())
	}

	// Pre-embed outside the lock. Observation extraction (D18/D23)
	// happens asynchronously in the curation cycle, not here.
	embedStart := time.Now()
	preEmbedded := a.preEmbedContent(ctx, req)
	embedDur := time.Since(embedStart)

	a.engine.Lock()
	defer a.engine.Unlock()

	props := graph.Properties{
		"content_full": graph.StringProperty(req.Content),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
		"access_count": graph.Int64Property(0),
	}
	// Set-once author attribution (bare `author` key), composed from
	// the effective engine config. Stamped before AddNode so it lands
	// in the property index with the rest of the base props. An empty
	// composed identity stamps nothing: the property is absent, not
	// empty-string.
	if author := a.engine.Config().Author.String(); author != "" {
		props["author"] = graph.StringProperty(author)
	}

	hasClassification := req.Temporality != "" || req.Confidence != nil
	if hasClassification {
		props["processing_status"] = graph.StringProperty("processed")
	} else {
		props["processing_status"] = graph.StringProperty("captured")
	}

	setOptionalProps(props, req)

	n := a.engine.Graph().AddNode(props)

	// Index content for BM25. Append meta values so keyword search
	// matches structured metadata fields.
	bm25Text := req.Content
	if metaText := metaBM25Text(req.Meta); metaText != "" {
		bm25Text += " " + metaText
	}
	a.engine.IndexNode(n.ID, bm25Text, nil)

	if len(req.Meta) > 0 {
		a.setMetaProps(n.ID, req.Meta)
	}

	var warnings []string
	if err := a.applyPreEmbedded(n.ID, preEmbedded); err != nil {
		warnings = append(warnings, fmt.Sprintf("embedding failed: %s", err))
	}

	var superseded []SupersededRecord
	if dupID, sim := a.engine.CheckDedup(n.ID); dupID != "" {
		cfg := a.engine.Config()
		if cfg.Dedup.Action == "reject" {
			msg := fmt.Sprintf("potential duplicate of %s (similarity %.3f)", dupID, sim)
			a.engine.PropIdx().RemoveNode(n.ID, n.Properties)
			a.engine.VecIdx().Remove(n.ID)
			a.engine.Graph().DeleteNode(n.ID)
			return SaveResponse{}, ErrConflict(msg)
		}

		// Default action is "supersede": mark the older record historical
		// and link the new record to it via a supersedes edge. Config.Load()
		// validates Action to one of "supersede" or "reject" (see
		// design-decisions.md D37), so reaching this branch implies
		// "supersede" semantics.
		if curation.IsSupersessionOptOut(a.engine.Graph(), dupID) {
			a.log.Debug("auto-supersession skipped: opt-out",
				"component", "save", "new_id", n.ID, "dup_id", dupID,
				"similarity", fmt.Sprintf("%.3f", sim))
		} else {
			now := time.Now().UTC()
			oldNode, _ := a.engine.Graph().GetNode(dupID)
			if oldNode != nil {
				_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
				if !alreadyHistorical {
					a.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
					a.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
					a.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
					if e, err := a.engine.Graph().AddEdge(n.ID, dupID, "supersedes", sim, nil); err == nil {
						summary := ""
						if v, ok := oldNode.Properties.GetString("content_short"); ok {
							summary = v
						}
						superseded = append(superseded, SupersededRecord{
							ID:         dupID,
							Summary:    summary,
							Similarity: sim,
							EdgeID:     e.ID,
						})
					}
				}
			}
		}
	}

	if _, err := a.engine.Save("save", graph.CommitAction{
		Kind: graph.ActionSave, RecordID: n.ID,
	}); err != nil {
		return SaveResponse{}, ErrInternal("failed to save")
	}

	a.log.Info("capture complete",
		"component", "save",
		"node", n.ID,
		"content_len", len(req.Content),
		"embed_ms", embedDur.Milliseconds(),
		"total_ms", time.Since(saveStart).Milliseconds(),
		"superseded", len(superseded) > 0)

	return SaveResponse{
		ID:         n.ID,
		Warnings:   warnings,
		Superseded: superseded,
	}, nil
}

// validateSaveRequest checks per-field invariants: numeric ranges,
// enum values, string lengths. Returns the first problem found.
func validateSaveRequest(r *SaveRequest) error {
	if err := validateFloat64Range("confidence", r.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", r.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("source_credibility", r.SourceCredibility, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", r.Temporality, ValidTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", r.KnowledgeType, ValidKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", r.EpistemicStatus, ValidEpistemicStatuses); err != nil {
		return err
	}
	if err := validateKeywords(r.Keywords); err != nil {
		return err
	}
	// Sanitize LLM-generated short fields for tool-use-format
	// leakage (`</summary_short>`, `<parameter name=`, etc.)
	// observed 2026-04-24. Mutates in place so downstream storage
	// uses the cleaned values. Rejects pure-contamination inputs
	// via sanitize.Validate.
	origSummary := r.SummaryShort
	r.SummaryShort = sanitize.Field(r.SummaryShort)
	if err := sanitize.Validate(origSummary, r.SummaryShort, "summary_short", MaxSummaryShort()); err != nil {
		return err
	}
	if len(r.SourceRef) > MaxSourceRefLen {
		return fmt.Errorf("source_ref exceeds maximum length of %d", MaxSourceRefLen)
	}
	contextFields := []struct {
		name string
		val  *string
	}{
		{"context_about", &r.ContextAbout},
		{"context_who", &r.ContextWho},
		{"context_prompted", &r.ContextPrompted},
		{"context_findable_by", &r.ContextFindable},
		{"context_related", &r.ContextRelated},
		{"context_source_type", &r.ContextSourceType},
		{"context_time_sensitivity", &r.ContextTimeSensitivity},
		{"context_reliability", &r.ContextReliability},
		{"context_capture_reason", &r.ContextCaptureReason},
	}
	for _, pair := range contextFields {
		orig := *pair.val
		*pair.val = sanitize.Field(*pair.val)
		if err := sanitize.Validate(orig, *pair.val, pair.name, MaxContextFieldLen); err != nil {
			return err
		}
	}
	for _, pair := range []struct{ name, val string }{
		{"valid_from", r.ValidFrom},
		{"valid_until", r.ValidUntil},
		{"asserted_as_of", r.AssertedAsOf},
	} {
		if pair.val == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, pair.val); err != nil {
			return fmt.Errorf("%s is not valid RFC3339", pair.name)
		}
	}
	return nil
}
