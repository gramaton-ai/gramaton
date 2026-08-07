package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
	"github.com/gramaton-ai/gramaton/similarity"
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
	SummaryShort           string         `json:"summary_short,omitempty" jsonschema:"target ~750 chars, max ~900 (semantic anchor for embedding)"`
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
	ClientToken            string         `json:"client_token,omitempty" jsonschema:"UUID; optional idempotency key. Retrying a timed-out save with the same token returns the already-created record instead of duplicating it; the same token with a different body is rejected."`
	AllowSimilar           []string       `json:"allow_similar,omitempty" jsonschema:"record IDs from a prior hold response, acknowledging the new record is genuinely distinct from them. Only set after a hold; never as a standing default."`
}

// SupersededRecord describes a record that a capture automatically
// marked as historical because the new record was a near-duplicate.
//
// Deprecated: auto-supersession is removed from the single-save path;
// the type remains only while the batch/session paths migrate to
// holds. Removed with the supersession removal cleanup.
type SupersededRecord struct {
	ID         string  `json:"id"`
	Summary    string  `json:"summary,omitempty"`
	Similarity float64 `json:"similarity"`
	EdgeID     string  `json:"edge_id"`
}

// HeldSimilar is the material for the judgment call a held save asks
// of its caller: the existing record the new content closely matched,
// with enough context to either revise it (gramaton_update) or re-send
// the save acknowledging the match (allow_similar).
type HeldSimilar struct {
	ID string `json:"id"`
	// ContentFull is the existing record's full content -- the caller
	// needs it both for the same-vs-distinct judgment and to compose a
	// correct revision (never revise from a summary alone).
	ContentFull string  `json:"content_full"`
	Summary     string  `json:"summary,omitempty"`
	Similarity  float64 `json:"similarity"`
	CreatedAt   string  `json:"created_at,omitempty"`
	// Version is the record's current version token; pass it as
	// expected_version when revising via gramaton_update so a
	// concurrent revision surfaces as a conflict instead of a silent
	// overwrite.
	Version string `json:"version"`
	// Historical is set when the matched record has expired or been
	// resolved (past valid_until): the knowledge exists but is not
	// current. Revising it via update revives it; Resolution carries
	// how it ended, when recorded.
	Historical bool   `json:"historical,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	// Note spells out the two exits.
	Note string `json:"note"`
}

// SimilarAdvisory is the non-blocking notice attached to a successful
// save that landed in the advisory band: similar enough to an
// existing record that it may be a revision, not new knowledge.
type SimilarAdvisory struct {
	ID         string  `json:"id"`
	Summary    string  `json:"summary,omitempty"`
	Similarity float64 `json:"similarity"`
	Note       string  `json:"note"`
}

// SaveResponse is the canonical output of the capture operation.
// Exactly one of ID or Held is set: a held save creates nothing and
// returns the similar record for a judgment call instead. Omitted
// fields use json:",omitempty" so the wire format stays tight on the
// happy path.
type SaveResponse struct {
	ID       string           `json:"id,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
	Held     *HeldSimilar     `json:"held,omitempty"`
	Advisory *SimilarAdvisory `json:"advisory,omitempty"`
}

// SaveDescription is the MCP tool description shared by every
// transport that surfaces save (direct MCP registration and the
// CLI MCP proxy). Changes here update both surfaces.
const SaveDescription = `Save a single NEW record to persistent semantic memory. Save is for new knowledge only: if this revises, corrects, or extends something already stored, search for it and use gramaton_update on the existing record instead.

NOT for saving the session/conversation itself, or for extracting knowledge from ongoing conversation -- use gramaton_session_prepare + gramaton_session_save for those. NOT for tasks/checklists -- use gramaton_collection_add.

Similar-record guard: a save closely matching an existing record is HELD -- nothing is created, and the response carries the similar record (id, full content, version token) with two exits: revise it via gramaton_update (pass the version token as expected_version), or re-send this save with allow_similar=[that id] if genuinely distinct. A save in the softer advisory band succeeds but carries an advisory naming the most similar record -- read it and consider whether an update was the right verb. Never set allow_similar as a standing default.

Field roles: content is unbounded and should be self-contained with rationale; summary_short (target ~750 chars, max ~900) is the embedding-ready semantic anchor for vector search; keywords are BM25 terms a future agent would type. These are different outputs serving different parts of retrieval, not nested compressions. For full guidance call gramaton_guide(topic="save").

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).

Retry safety: when retrying after a timeout or transport error, pass the same client_token (a UUID you generate) on both attempts -- the retry returns the originally created record instead of storing a duplicate.`

// Save creates a new knowledge record, guarded by the save-guard
// similarity scan. Pre-embeds content_short and runs the candidate
// scan outside the engine write lock (the scan is O(index size)),
// then holds the lock only to insert the node, attach the embedding,
// and commit.
//
// Hold: when the new content closely matches an existing record
// (cosine >= save_guard.similar_hold_threshold, Jaccard-verified),
// nothing is created; the response carries the similar record with
// the two exits (revise it via gramaton_update, or re-send with
// allow_similar acknowledging the match). Advisory: a save in
// [advisory_threshold, hold threshold) succeeds and carries a
// non-blocking notice naming the most similar record.
//
// Concurrency: the off-lock scan cannot see records that commit
// between the scan and this save's write-lock acquisition; the
// engine's delta re-scan (WriteSeq captured before the scan,
// SimilarInDelta under the write lock) closes that window, degrading
// to a logged bounded residual race only if the ring wraps.
//
// Embedder outage: when embedding fails, the record is created with
// a warning and marked similar_check_pending; reembed re-runs the
// scan when the vector arrives and surfaces any match.
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

	// Idempotency prep: hash the canonical request (client_token
	// itself excluded by the canonical form) so a replay can be told
	// apart from token reuse with a different body. Hash computed
	// off-lock; the token lookup happens under the write lock where
	// check-then-insert is atomic.
	var requestHash string
	if req.ClientToken != "" {
		if err := validateClientToken(req.ClientToken); err != nil {
			return SaveResponse{}, ErrInvalid(err.Error())
		}
		canonical, err := json.Marshal(canonicalizeItem(SaveBatchItem{SaveRequest: req}))
		if err != nil {
			a.log.Warn("save canonicalize failed", "err", err)
			return SaveResponse{}, ErrInternal("failed to canonicalize request")
		}
		requestHash = hashCanonical(canonical)
	}

	// Pre-embed outside the lock. Observation extraction (D18/D23)
	// happens asynchronously in the curation cycle, not here.
	embedStart := time.Now()
	preEmbedded := a.preEmbedContent(ctx, req)
	embedDur := time.Since(embedStart)

	// Save-guard scan against a read snapshot, before the write lock.
	// The candidate search is O(index size) and does not belong in
	// the write critical section. WriteSeq is captured under the same
	// read lock so the delta re-scan below covers exactly the records
	// that commit in the scan-to-lock window.
	var scanVec []float32
	var scanSeq uint64
	var outcome similarity.Outcome
	if preEmbedded != nil && preEmbedded.err == nil {
		if vec, ok := preEmbedded.vectors["embedding_full"]; ok {
			scanVec = vec
			a.engine.RLock()
			scanSeq = a.engine.WriteSeq()
			outcome = a.engine.ScanSimilarVec(vec, req.Content)
			a.engine.RUnlock()
		}
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	// Idempotent replay: a prior save with this token already
	// committed. The same body returns the existing record; a
	// different body is token misuse. Checked before the dedup-reject
	// branch so a replay in reject mode is not mistaken for a
	// duplicate of the very record it created.
	if req.ClientToken != "" {
		// Unlike the batch path's tenant-scoped FindByClientToken,
		// this lookup has no tenant predicate -- records carry no
		// tenant today. If record-level tenancy ever wires in
		// (api/tenant.go), scope this lookup in lockstep.
		ids := a.engine.PropIdx().Lookup("client_token", graph.StringProperty(req.ClientToken))
		if len(ids) > 0 {
			if prior, ok := a.engine.Graph().GetNode(ids[0]); ok {
				storedHash, _ := prior.Properties.GetString("client_request_hash")
				if storedHash != requestHash {
					return SaveResponse{}, ErrConflict("client_token reused with different request body")
				}
				return SaveResponse{ID: prior.ID}, nil
			}
		}
	}

	// Delta re-scan: records that committed between the off-lock scan
	// and this lock acquisition. A hold found here beats the scan's
	// hold if more similar.
	if scanVec != nil {
		if m, found, _ := a.engine.SimilarInDelta(scanSeq, scanVec, req.Content); found {
			if outcome.Hold == nil || m.Similarity > outcome.Hold.Similarity {
				held := m
				outcome.Hold = &held
				outcome.Advisory = nil
			}
		}
	}

	// Hold: nothing is created; the caller gets the similar record
	// and the two exits. allow_similar with the matched ID is the
	// acknowledgment that lets the save through. The candidate is
	// re-verified under the lock -- one hard-deleted in the
	// scan-to-lock window must not hold the save.
	if outcome.Hold != nil && !ackContains(req.AllowSimilar, outcome.Hold.NodeID) {
		if held := a.buildHeldSimilar(outcome.Hold); held != nil {
			a.log.Info("save held: similar record",
				"component", "save",
				"similar_to", held.ID,
				"similarity", fmt.Sprintf("%.3f", held.Similarity))
			return SaveResponse{Held: held}, nil
		}
	}

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

	// Idempotency bookkeeping props. Top-level system props like
	// author/processing_status (not meta.*), so they can never collide
	// with caller metadata.
	if req.ClientToken != "" {
		props["client_token"] = graph.StringProperty(req.ClientToken)
		props["client_request_hash"] = graph.StringProperty(requestHash)
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
		// The save-guard scan never ran; mark the record so reembed
		// re-runs it when the deferred embedding arrives.
		a.engine.SetProp(n.ID, "similar_check_pending", graph.BoolProperty(true))
	}

	// Advisory: the save succeeded; attach the non-blocking notice if
	// the best candidate landed in the advisory band (and is not one
	// the caller already acknowledged).
	var advisory *SimilarAdvisory
	if outcome.Advisory != nil && !ackContains(req.AllowSimilar, outcome.Advisory.NodeID) {
		advisory = a.buildSimilarAdvisory(outcome.Advisory)
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
		"advisory", advisory != nil)

	return SaveResponse{
		ID:       n.ID,
		Warnings: warnings,
		Advisory: advisory,
	}, nil
}

// ackContains reports whether the caller's allow_similar
// acknowledgment list names the candidate record.
func ackContains(acks []string, id string) bool {
	for _, a := range acks {
		if a == id {
			return true
		}
	}
	return false
}

// buildHeldSimilar assembles the hold response material for a
// candidate match. Returns nil when the candidate no longer exists
// (deleted in the scan-to-lock window) -- the save proceeds instead.
// Caller must hold the engine lock.
func (a *API) buildHeldSimilar(m *similarity.Match) *HeldSimilar {
	n, ok := a.engine.Graph().GetNode(m.NodeID)
	if !ok {
		return nil
	}
	content, _ := n.Properties.GetString("content_full")
	summary, _ := n.Properties.GetString("content_short")
	created := ""
	if ts, ok := n.Properties.GetTimestamp("created_at"); ok {
		created = ts.UTC().Format(time.RFC3339)
	}
	historical := false
	if vu, ok := n.Properties.GetTimestamp("valid_until"); ok && vu.Before(time.Now().UTC()) {
		historical = true
	}
	resolution, _ := n.Properties.GetString("resolution")
	return &HeldSimilar{
		ID:          n.ID,
		ContentFull: content,
		Summary:     summary,
		Similarity:  m.Similarity,
		CreatedAt:   created,
		Version:     recordVersionToken(n),
		Historical:  historical,
		Resolution:  resolution,
		Note: "Save held; nothing was created. This closely matches the record above. " +
			"If this REVISES it: gramaton_update(id, content=..., expected_version=version) composed from its full content. " +
			"If genuinely distinct: re-send the save with allow_similar=[\"" + n.ID + "\"].",
	}
}

// buildSimilarAdvisory assembles the advisory notice for a candidate
// in the advisory band. Returns nil when the candidate no longer
// exists. Caller must hold the engine lock.
func (a *API) buildSimilarAdvisory(m *similarity.Match) *SimilarAdvisory {
	n, ok := a.engine.Graph().GetNode(m.NodeID)
	if !ok {
		return nil
	}
	summary, _ := n.Properties.GetString("content_short")
	return &SimilarAdvisory{
		ID:         n.ID,
		Summary:    summary,
		Similarity: m.Similarity,
		Note:       "Saved, but this resembles the record above. If it is a revision of that knowledge, prefer gramaton_update on it (inspect first) and consider resolving this record.",
	}
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
