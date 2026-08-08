package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
	"github.com/gramaton-ai/gramaton/search"
)

// contentAppendSeparator joins existing content and appended text.
const contentAppendSeparator = "\n\n"

// summaryRefreshAppendRatio: when cumulative appended bytes since the
// last summary_short write exceed this fraction of the content, the
// record is flagged summary_refresh_pending (the summary is the
// primary vector anchor, so a summary that no longer represents the
// content makes the appended knowledge invisible to semantic search).
// Deliberately a constant, not config, until real use argues for
// tuning it.
const summaryRefreshAppendRatio = 0.25

// appendSizeNudgeBytes: an append that grows a record past this size
// earns a response note suggesting a consolidating rewrite -- append
// is for additive facts, and unbounded accretion turns a record into
// an unstructured log.
const appendSizeNudgeBytes = 8 * 1024

// UpdateRequest is the input to the update operation. ID is set by
// the transport from the URL path / tool args and is not part of the
// HTTP request body.
type UpdateRequest struct {
	ID              string         `json:"-" jsonschema:"-"` // transport-set
	Content         string         `json:"content,omitempty" jsonschema:"replaces the record's full content. Compose from the record's CURRENT full content (inspect first); the response echoes the prior content. Re-embeds and re-indexes. Mutually exclusive with content_append."`
	ContentAppend   string         `json:"content_append,omitempty" jsonschema:"appends to the record's content. For ADDITIVE facts only -- corrections and reversals must use content (replace): appending a correction creates a record that disagrees with itself. Mutually exclusive with content."`
	ExpectedVersion string         `json:"expected_version,omitempty" jsonschema:"version token from a hold response or inspect. When set, the update applies only if the record's content is unchanged since; on mismatch the response carries version_conflict with the current content, and nothing is applied."`
	Confidence      *float64       `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
	Temporality     string         `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	KnowledgeType   string         `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string         `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Importance      *float64       `json:"importance,omitempty" jsonschema:"0.0-1.0"`
	Keywords        []string       `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
	SummaryShort    string         `json:"summary_short,omitempty" jsonschema:"target ~750 chars, max ~900 (semantic anchor for embedding). Provide alongside content rewrites so the anchor tracks the new content."`
	ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration date (YYYY-MM-DD or RFC3339) -- marks record as historical. Use 'clear' to remove."`
	AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
	Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata (e.g. {assignee: Sarah, status: done})"`
	ChangeNote      string         `json:"change_note,omitempty" jsonschema:"optional free-text WHY for this change (max ~1.8KB), surfaced per-version in the record timeline. The field diff is computed mechanically either way -- write the note only when there is a distillable reason a diff cannot show."`
}

// VersionConflict reports an expected_version mismatch: the record's
// content changed since the caller last read it. Nothing was applied;
// re-judge against the current content and retry with its version.
type VersionConflict struct {
	CurrentVersion string `json:"current_version"`
	CurrentContent string `json:"current_content"`
	Note           string `json:"note"`
}

// UpdateResponse carries the id that was updated, whether any field
// actually changed, the prior content when content changed (immediate
// self-correction safety), the record's post-update version token,
// and advisory notes.
type UpdateResponse struct {
	ID                string           `json:"id"`
	Updated           bool             `json:"updated"`
	Version           string           `json:"version,omitempty"`
	PreviousContent   string           `json:"previous_content,omitempty"`
	VersionConflict   *VersionConflict `json:"version_conflict,omitempty"`
	Notes             []string         `json:"notes,omitempty"`
	CollectionWarning string           `json:"collection_warning,omitempty"`
}

// UpdateDescription is the MCP tool description for gramaton_update.
const UpdateDescription = `Update a Memory record in place -- metadata AND content. This is how knowledge EVOLVES: when new information revises, corrects, or extends something already stored, update the existing record rather than saving a near-duplicate.

content replaces the full content (compose from the record's current full content -- inspect first; the prior content is echoed back). content_append adds text for additive facts only; corrections must replace. Content changes re-embed and re-index the record. Pass expected_version (from a hold response or inspect) so a concurrent revision surfaces as version_conflict instead of a silent overwrite.

For collection item fields, use gramaton_collection_update instead.`

// Update mutates an existing record. Empty / nil fields leave the
// existing property unchanged. Setting valid_until to "clear" removes
// the valid_until + resolution + resolved_at triple (reopens a
// resolved record). Returns ErrInvalid for any unknown enum value or
// out-of-range numeric. Refuses to update Session segments
// (append-only per D19).
//
// Content changes follow the three-phase pattern: snapshot under
// RLock, embed off-lock, verify + apply under Lock in one commit.
// Embedding failure fails the update -- content and vectors move
// together, always (a present-but-stale embedding would never be
// re-selected by reembed's gate). The record's observation children
// are deleted on content change; curation re-extracts from the
// current content.
func (a *API) Update(ctx context.Context, req UpdateRequest) (UpdateResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("update"); apiErr != nil {
		return UpdateResponse{}, apiErr
	}
	if req.ID == "" {
		return UpdateResponse{}, ErrMissing("id is required")
	}
	if req.Content != "" && req.ContentAppend != "" {
		return UpdateResponse{}, ErrInvalid("content and content_append are mutually exclusive")
	}
	maxContent := a.engine.Config().Limits.MaxContentLength
	if maxContent > 0 && (len(req.Content) > maxContent || len(req.ContentAppend) > maxContent) {
		return UpdateResponse{}, ErrInvalid("content exceeds maximum length")
	}
	if err := validateUpdateRequest(&req); err != nil {
		return UpdateResponse{}, ErrInvalid(err.Error())
	}
	// Everything that can fail on input is checked BEFORE the write
	// phase: phase 3 mutates the in-memory graph without rollback, so
	// an input-triggered error past the version gate would leave
	// uncommitted partial state for the next unrelated commit to
	// persist.
	var validUntil, assertedAsOf time.Time
	if req.ValidUntil != "" && req.ValidUntil != "clear" {
		t, err := parseDateArg(req.ValidUntil)
		if err != nil {
			return UpdateResponse{}, ErrInvalid("invalid valid_until date")
		}
		validUntil = t
	}
	if req.AssertedAsOf != "" {
		t, err := parseDateArg(req.AssertedAsOf)
		if err != nil {
			return UpdateResponse{}, ErrInvalid("invalid asserted_as_of date")
		}
		assertedAsOf = t
	}
	if len(req.Meta) > 0 {
		if err := validateMeta(req.Meta); err != nil {
			return UpdateResponse{}, ErrInvalid(err.Error())
		}
	}
	contentChanging := req.Content != "" || req.ContentAppend != ""

	// Phase 1: snapshot under read lock -- enough to decide what to
	// embed off-lock. The authoritative content read and version check
	// happen again under the write lock; this snapshot only shapes the
	// embed input (appends don't move the summary-anchored vector, so
	// a concurrent content change between phases cannot invalidate it).
	var existingSummary string
	a.engine.RLock()
	n, ok := a.engine.Graph().GetNode(req.ID)
	if ok {
		existingSummary, _ = n.Properties.GetString("content_short")
		if kt, _ := n.Properties.GetString("knowledge_type"); kt == "segment" {
			a.engine.RUnlock()
			return UpdateResponse{}, ErrInvalid("session segments are append-only; use gramaton_session_save to update capture status")
		}
		if graph.IsConcept(n.Properties) {
			a.engine.RUnlock()
			return UpdateResponse{}, ErrInvalid("concept nodes are machine-owned derived summaries regenerated by curation; update the member records instead")
		}
		if graph.IsSectionOrChunk(n.Properties) {
			a.engine.RUnlock()
			return UpdateResponse{}, ErrInvalid("section/chunk children are derived from their parent document and regenerated on re-chunk; update the parent record instead")
		}
	}
	priorSnapshot := ""
	if ok && contentChanging {
		priorSnapshot, _ = n.Properties.GetString("content_full")
	}
	a.engine.RUnlock()
	if !ok {
		return UpdateResponse{}, ErrNotFound("record not found")
	}

	// Decide the embed input. The primary vector is anchored to
	// summary_short: a new summary always re-embeds; content changes
	// re-embed only when no summary exists (the vector is then
	// content-derived). A content change that leaves an existing
	// summary in place keeps the vector and instead flags the summary
	// for refresh below.
	embedText := ""
	switch {
	case req.SummaryShort != "":
		embedText = req.SummaryShort
	case contentChanging && existingSummary == "":
		newFull := req.Content
		if req.ContentAppend != "" {
			newFull = priorSnapshot + contentAppendSeparator + req.ContentAppend
		}
		embedText = newFull
		if cap := MaxSummaryShort(); len(embedText) > cap {
			embedText = embedText[:cap]
		}
	}

	// Phase 2: embed off-lock. Failure fails the update when the
	// change needed a new vector.
	var newVec []float32
	embedModel := ""
	if embedText != "" && a.engine.Embedder() != nil {
		vecs, err := a.engine.Embedder().Embed(ctx, []string{embedText})
		if err != nil {
			a.log.Warn("update embed failed", "component", "update", "id", req.ID, "err", err)
			return UpdateResponse{}, ErrUnavailable("embedding failed; update not applied (content and vectors move together)")
		}
		newVec = vecs[0]
		embedModel = a.engine.Embedder().ModelID()
	}

	// Phase 2b: long-document chunking off-lock, on the prospective
	// content. For appends the prospective text is built on the
	// phase-1 snapshot; a snapshot invalidated by a concurrent write
	// surfaces as a version conflict under the lock (gate below) --
	// children derived from stale text must never be applied.
	var preChunk *core.PreChunkResult
	if contentChanging {
		prospective := req.Content
		if req.ContentAppend != "" {
			prospective = priorSnapshot + contentAppendSeparator + req.ContentAppend
		}
		if a.engine.ShouldChunk(len(prospective)) {
			summaryForParent := req.SummaryShort
			if summaryForParent == "" {
				summaryForParent = existingSummary
			}
			preChunk = a.engine.PreChunk(ctx, prospective, summaryForParent)
			// This path is fail-closed: content and vectors move
			// together. A degraded prechunk (children without vectors,
			// from an embedding outage) would store text that vector
			// search cannot reach -- reject it like any other
			// embedding failure. PreChunk itself fails open because
			// the save path tolerates degradation; the update path
			// does not.
			if preChunk != nil && a.engine.Embedder() != nil && !preChunk.Embedded() {
				a.log.Warn("update prechunk embed failed", "component", "update", "id", req.ID)
				return UpdateResponse{}, ErrUnavailable("embedding failed; update not applied (content and vectors move together)")
			}
		}
	}

	// Phase 3: verify and apply under the write lock, one commit.
	a.engine.Lock()
	defer a.engine.Unlock()

	n, ok = a.engine.Graph().GetNode(req.ID)
	if !ok {
		return UpdateResponse{}, ErrNotFound("record not found")
	}
	currentContent, _ := n.Properties.GetString("content_full")
	currentToken := recordVersionToken(n)
	if req.ExpectedVersion != "" && req.ExpectedVersion != currentToken {
		return UpdateResponse{
			ID: req.ID,
			VersionConflict: &VersionConflict{
				CurrentVersion: currentToken,
				CurrentContent: currentContent,
				Note:           "The record's content changed since you read it. Nothing was applied. Re-judge against current_content and retry with expected_version=current_version.",
			},
		}, nil
	}

	// A no-summary append derives its vector from the phase-1 content
	// snapshot, and a prechunk derives its children from it; the
	// append itself rebases onto the live content. If the content
	// moved between phases, storing the rebase would silently pair
	// the record with a vector -- or section children -- embedded
	// from text it no longer contains. Surface it as the version
	// conflict it is, before any mutation, and let the caller retry
	// against the fresh state.
	if req.ContentAppend != "" && currentContent != priorSnapshot &&
		((req.SummaryShort == "" && existingSummary == "" && newVec != nil) || preChunk != nil) {
		return UpdateResponse{
			ID: req.ID,
			VersionConflict: &VersionConflict{
				CurrentVersion: currentToken,
				CurrentContent: currentContent,
				Note:           "The record's content changed while the append was being prepared. Nothing was applied. Retry the append against current_content.",
			},
		}, nil
	}

	// The rebased append is checked against the content cap before any
	// mutation: the per-field cap in phase 0 bounds the increment, not
	// the sum, and repeated appends must not grow a record past the
	// limit save enforces.
	if maxContent > 0 && req.ContentAppend != "" &&
		len(currentContent)+len(contentAppendSeparator)+len(req.ContentAppend) > maxContent {
		return UpdateResponse{}, ErrInvalid("content_append would grow the record past the maximum content length; consolidate with a content rewrite instead")
	}

	// The embedding applies first for the same no-rollback reason the
	// input checks moved to phase 0: after the first content mutation,
	// nothing may fail except the commit itself.
	if newVec != nil {
		pre := &preEmbeddedVectors{
			vectors: map[string][]float32{"embedding_full": newVec},
			model:   embedModel,
		}
		if err := a.applyPreEmbedded(req.ID, pre); err != nil {
			return UpdateResponse{}, ErrInternal("failed to apply embedding")
		}
	}

	updated := false
	var notes []string
	previousContent := ""
	newFull := ""

	if contentChanging {
		newFull = req.Content
		if req.ContentAppend != "" {
			// Rebase the append onto the live content -- the phase-1
			// snapshot may be stale, and append semantics are "add to
			// whatever is there".
			newFull = currentContent + contentAppendSeparator + req.ContentAppend
		}
		previousContent = currentContent
		a.engine.SetProp(req.ID, "content_full", graph.StringProperty(newFull))
		a.engine.SetProp(req.ID, "updated_at", graph.TimestampProperty(time.Now().UTC()))

		// A content change reopens any recorded conflict: drop the
		// record's contradicts edges (the pair re-enters the
		// contradiction-detection window) and the contested status
		// they justified -- on this record, and on each peer left
		// with no other conflict.
		a.reopenConflicts(req.ID)

		// Observation children assert the OLD content verbatim at full
		// score; delete them so curation re-extracts from the current
		// content. Section/chunk children are deleted for the same
		// reason -- unconditionally, so a shrink below the chunking
		// threshold clears them even when no re-chunk follows; the
		// re-chunk (if any) re-applies below. (DeleteNode's edge
		// cascade is safe here: plain engine lock, not a shared
		// write-batch transaction.)
		for _, childID := range a.observationChildren(req.ID) {
			a.purgeChildNode(childID)
		}
		for _, childID := range a.sectionChunkChildren(req.ID) {
			a.purgeChildNode(childID)
		}

		// Summary-refresh bookkeeping: the summary is the vector
		// anchor, so content that drifts from it makes the new
		// knowledge semantically invisible.
		if req.SummaryShort == "" && existingSummary != "" {
			if req.ContentAppend != "" {
				var appended int64
				if v, ok := n.Properties.GetInt64("appended_since_summary"); ok {
					appended = v
				}
				appended += int64(len(req.ContentAppend))
				a.engine.SetProp(req.ID, "appended_since_summary", graph.Int64Property(appended))
				if float64(appended) > summaryRefreshAppendRatio*float64(len(newFull)) {
					a.engine.SetProp(req.ID, "summary_refresh_pending", graph.BoolProperty(true))
					notes = append(notes, "Appended content now exceeds the summary-refresh threshold; the record is flagged for summary regeneration (appended text is BM25-findable but not vector-findable until then). Consider providing an updated summary_short.")
				}
			} else {
				// Full replace with a stale summary: flag immediately.
				a.engine.SetProp(req.ID, "summary_refresh_pending", graph.BoolProperty(true))
				notes = append(notes, "Content was replaced but summary_short was not; the record is flagged for summary regeneration. Prefer providing an updated summary_short with content rewrites.")
			}
		}
		if req.ContentAppend != "" && len(newFull) > appendSizeNudgeBytes {
			notes = append(notes, fmt.Sprintf("This record has grown to %d bytes through appends; consider a consolidating rewrite (content replace).", len(newFull)))
		}
		updated = true
	}

	if req.SummaryShort != "" {
		a.engine.SetProp(req.ID, "content_short", graph.StringProperty(req.SummaryShort))
		// A fresh summary resets the refresh bookkeeping.
		for _, key := range []string{"appended_since_summary", "summary_refresh_pending"} {
			if old, has := n.Properties[key]; has {
				a.engine.PropIdx().Remove(req.ID, key, old)
				a.engine.Graph().RemoveNodeProperty(req.ID, key)
			}
		}
		updated = true
	}
	if req.Confidence != nil {
		a.engine.SetProp(req.ID, "confidence", graph.Float64Property(*req.Confidence))
		updated = true
	}
	if req.Temporality != "" {
		a.engine.SetProp(req.ID, "temporality", graph.StringProperty(req.Temporality))
		updated = true
	}
	if req.KnowledgeType != "" {
		a.engine.SetProp(req.ID, "knowledge_type", graph.StringProperty(req.KnowledgeType))
		updated = true
	}
	if req.EpistemicStatus != "" {
		a.engine.SetProp(req.ID, "epistemic_status", graph.StringProperty(req.EpistemicStatus))
		updated = true
	}
	if req.Importance != nil {
		a.engine.SetProp(req.ID, "importance", graph.Float64Property(*req.Importance))
		updated = true
	}
	if len(req.Keywords) > 0 {
		a.engine.SetProp(req.ID, "content_keywords", graph.StringListProperty(req.Keywords))
		updated = true
	}
	if req.ValidUntil != "" {
		if req.ValidUntil == "clear" {
			for _, key := range []string{"valid_until", "resolution", "resolved_at"} {
				if old, has := n.Properties[key]; has {
					a.engine.PropIdx().Remove(req.ID, key, old)
					a.engine.Graph().RemoveNodeProperty(req.ID, key)
				}
			}
			updated = true
		} else {
			a.engine.SetProp(req.ID, "valid_until", graph.TimestampProperty(validUntil))
			updated = true
		}
	}
	if req.AssertedAsOf != "" {
		a.engine.SetProp(req.ID, "asserted_as_of", graph.TimestampProperty(assertedAsOf))
		updated = true
	}
	if len(req.Meta) > 0 {
		a.setMetaProps(req.ID, req.Meta)
		updated = true
	}

	// BM25 re-indexes last so the text reflects every applied change:
	// the new content, the stored (possibly just-replaced) keywords,
	// and post-update meta values. Mirrors save's indexing input.
	if contentChanging {
		a.engine.BM25Full().Remove(req.ID)
		bm25Text := newFull
		if fresh, ok := a.engine.Graph().GetNode(req.ID); ok {
			if kws, ok := fresh.Properties.GetStringList("content_keywords"); ok && len(kws) > 0 {
				bm25Text += " " + strings.Join(kws, " ")
			}
			if metaText := metaBM25TextFromNode(fresh); metaText != "" {
				bm25Text += " " + metaText
			}
		}
		a.engine.IndexNode(req.ID, bm25Text, nil)
	}

	// Re-chunk: apply children derived from the new content and swap
	// the parent's embedding for the purpose-sized ParentVec (which
	// wins over any phase-2 vector -- both are summary-anchored;
	// ParentVec is simply sized for the model window). A failed apply
	// must not error out here -- the content mutation above has no
	// rollback -- so it degrades to a note; the children regenerate
	// on the next content update.
	if preChunk != nil {
		if fresh, ok := a.engine.Graph().GetNode(req.ID); ok {
			if _, err := a.engine.ApplyChunks(req.ID, preChunk, fresh.Properties); err != nil {
				a.log.Warn("update re-chunk apply failed", "component", "update", "id", req.ID, "err", err)
				notes = append(notes, "Chunking failed; the update saved but section children were not regenerated (the next content update retries).")
			} else if preChunk.ParentVec != nil {
				a.engine.NoteRecentWrite(req.ID, preChunk.ParentVec)
			}
		}
	}

	if updated {
		if _, err := a.engine.Save("update", graph.CommitAction{
			Kind: graph.ActionUpdate, RecordID: req.ID, Note: req.ChangeNote,
		}); err != nil {
			return UpdateResponse{}, ErrInternal("failed to save")
		}
	}

	version := currentToken
	if fresh, ok := a.engine.Graph().GetNode(req.ID); ok {
		version = recordVersionToken(fresh)
	}
	resp := UpdateResponse{
		ID:              req.ID,
		Updated:         updated,
		Version:         version,
		PreviousContent: previousContent,
		Notes:           notes,
	}
	if colls := a.nodeCollectionNames(req.ID); len(colls) > 0 {
		resp.CollectionWarning = fmt.Sprintf(
			"This record is a member of collection(s): %s. Use gramaton_collection_update to modify collection item fields.",
			joinCollectionNames(colls))
	}
	return resp, nil
}

// reopenConflicts removes recordID's contradicts edges (both
// directions) and its contested status, plus the contested status of
// any peer left with no remaining conflicts. Called on content
// change: the revision reopens the question, and with the edges gone
// the pair re-enters the contradiction-detection window naturally.
// Caller must hold the engine write lock.
func (a *API) reopenConflicts(recordID string) {
	// Every contradiction VERDICT reopens on content change -- the
	// positive edge, and equally the negative ones (no_contradiction,
	// contradiction_check_skipped): the curation pass hard-skips any
	// pair carrying a prior verdict, so a stale negative would pin a
	// pair out of re-evaluation forever even after a rewrite put the
	// two records in direct contradiction.
	verdictEdge := func(t string) bool {
		return t == "contradicts" || t == "no_contradiction" || t == "contradiction_check_skipped"
	}
	peers := map[string]struct{}{}
	var edgeIDs []string
	for _, e := range a.engine.Graph().EdgesFrom(recordID) {
		if verdictEdge(e.Type) {
			edgeIDs = append(edgeIDs, e.ID)
			if e.Type == "contradicts" {
				peers[e.TargetID] = struct{}{}
			}
		}
	}
	for _, e := range a.engine.Graph().EdgesTo(recordID) {
		if verdictEdge(e.Type) {
			edgeIDs = append(edgeIDs, e.ID)
			if e.Type == "contradicts" {
				peers[e.SourceID] = struct{}{}
			}
		}
	}
	if len(edgeIDs) == 0 {
		return
	}
	for _, id := range edgeIDs {
		if err := a.engine.Graph().DeleteEdge(id); err != nil {
			a.log.Warn("reopen conflicts: delete edge failed",
				"component", "update", "record", recordID, "edge", id, "err", err)
		}
	}
	clearContested := func(id string) {
		if n, ok := a.engine.Graph().GetNode(id); ok {
			if es, _ := n.Properties.GetString("epistemic_status"); es == "contested" {
				if old, has := n.Properties["epistemic_status"]; has {
					a.engine.PropIdx().Remove(id, "epistemic_status", old)
					a.engine.Graph().RemoveNodeProperty(id, "epistemic_status")
				}
			}
		}
	}
	clearContested(recordID)
	for peer := range peers {
		if len(search.ConflictingRecordIDs(a.engine.Graph(), peer)) == 0 {
			clearContested(peer)
		}
	}
}

// observationChildren returns the observation nodes extracted from
// recordID (inbound observation_of edges). Caller must hold at least
// a read lock.
func (a *API) observationChildren(recordID string) []string {
	var out []string
	for _, e := range a.engine.Graph().EdgesTo(recordID) {
		if e.Type != "observation_of" {
			continue
		}
		if child, ok := a.engine.Graph().GetNode(e.SourceID); ok {
			if nt, _ := child.Properties.GetString("node_type"); nt == "observation" {
				out = append(out, child.ID)
			}
		}
	}
	return out
}

// sectionChunkChildren returns the section/chunk children of
// recordID (inbound section_of/chunk_of edges), double-gated on the
// child's node_type like observationChildren. Caller must hold at
// least a read lock.
func (a *API) sectionChunkChildren(recordID string) []string {
	var out []string
	for _, e := range a.engine.Graph().EdgesTo(recordID) {
		if e.Type != "section_of" && e.Type != "chunk_of" {
			continue
		}
		if child, ok := a.engine.Graph().GetNode(e.SourceID); ok && graph.IsSectionOrChunk(child.Properties) {
			out = append(out, child.ID)
		}
	}
	return out
}

// purgeChildNode removes a derived child from every index and the
// graph -- the plain-engine-lock deletion idiom (each removal opens
// its own bbolt update; the batched twin is WriteSession.DeleteNode).
// DeleteNode's edge cascade removes the structural edge. Caller must
// hold the engine write lock.
func (a *API) purgeChildNode(childID string) {
	child, ok := a.engine.Graph().GetNode(childID)
	if !ok {
		return
	}
	a.engine.PropIdx().RemoveNode(childID, child.Properties)
	a.engine.VecIdx().Remove(childID)
	a.engine.BM25Full().Remove(childID)
	if sec := a.engine.SecIdx(); sec != nil {
		sec.RemoveNode(childID)
	}
	a.engine.Graph().DeleteNode(childID)
}

// metaBM25TextFromNode rebuilds the "key:value" BM25 text from a
// node's stored meta.* properties, mirroring metaBM25Text's output
// for the original save across every stored meta type (string,
// number, bool, string list).
func metaBM25TextFromNode(n *graph.Node) string {
	var parts []string
	for key := range n.Properties {
		if !strings.HasPrefix(key, "meta.") {
			continue
		}
		name := strings.TrimPrefix(key, "meta.")
		if s, ok := n.Properties.GetString(key); ok {
			parts = append(parts, name+":"+s)
			continue
		}
		if f, ok := n.Properties.GetFloat64(key); ok {
			parts = append(parts, fmt.Sprintf("%s:%g", name, f))
			continue
		}
		if b, ok := n.Properties.GetBool(key); ok {
			parts = append(parts, fmt.Sprintf("%s:%t", name, b))
			continue
		}
		if ss, ok := n.Properties.GetStringList(key); ok {
			for _, s := range ss {
				parts = append(parts, name+":"+s)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func validateUpdateRequest(r *UpdateRequest) error {
	if len(r.ID) > MaxIDArgLen {
		return fmt.Errorf("id exceeds maximum length of %d", MaxIDArgLen)
	}
	if len(r.ExpectedVersion) > MaxIDArgLen {
		return fmt.Errorf("expected_version exceeds maximum length of %d", MaxIDArgLen)
	}
	if len(r.ChangeNote) > MaxChangeNote {
		return fmt.Errorf("change_note exceeds maximum length of %d", MaxChangeNote)
	}
	if err := validateFloat64Range("confidence", r.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", r.Importance, 0.0, 1.0); err != nil {
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
	origSummary := r.SummaryShort
	r.SummaryShort = sanitize.Field(r.SummaryShort)
	if err := sanitize.Validate(origSummary, r.SummaryShort, "summary_short", MaxSummaryShort()); err != nil {
		return err
	}
	return nil
}
