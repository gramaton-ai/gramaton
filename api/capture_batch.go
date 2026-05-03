package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/jobs"
	"github.com/oklog/ulid/v2"
)

// CaptureBatchRequest captures up to MaxSyncBatchSize records in a
// single call. Items follow the same shape as CaptureRequest with an
// optional ClientRef for in-batch identity.
//
// Layer 3 honors only the synchronous path; passing Wait=false returns
// ErrInvalid until Layer 5 wires the async runner. ClientToken +
// canonicalized RequestHash provide cross-call idempotency.
type CaptureBatchRequest struct {
	Items            []CaptureBatchItem `json:"items" jsonschema:"items to capture (1 to MaxSyncBatchSize); each follows the gramaton_capture shape with an optional client_ref"`
	Wait             *bool              `json:"wait,omitempty" jsonschema:"true (sync, default) returns the full result inline; false (async) returns a job_id to poll. Layer 5 implements async; Layer 3 rejects wait=false."`
	ClientToken      string             `json:"client_token,omitempty" jsonschema:"UUID. With identical request body returns the prior JobID idempotently; with a different body the same token is rejected."`
	SkipSupersession bool               `json:"skip_supersession,omitempty" jsonschema:"when true, dedup-driven supersession is disabled for the entire batch. For migration imports."`
}

// CaptureBatchItem is one record in a CaptureBatchRequest. Embeds the
// existing single-capture shape to keep field semantics identical and
// adds a per-item ClientRef the caller can use to wire intra-batch
// edges (Layer 4).
type CaptureBatchItem struct {
	CaptureRequest
	ClientRef string `json:"client_ref,omitempty" jsonschema:"caller-supplied label unique within the batch (max 128 chars; letters, digits, dot, hyphen, underscore); echoed back in the response and used by Edges in Layer 4."`
}

// CaptureBatchAdded describes one record that the batch successfully
// committed. ClientRef is echoed back when the request supplied one.
// Warnings collect non-fatal events (embed fallback, internal
// supersession, etc.) per item.
type CaptureBatchAdded struct {
	ID         string             `json:"id"`
	ClientRef  string             `json:"client_ref,omitempty"`
	Warnings   []string           `json:"warnings,omitempty"`
	Superseded []SupersededRecord `json:"superseded,omitempty"`
}

// BatchItemFailure describes one item that did NOT commit. Index maps
// back to the original Items slice; ClientRef is echoed when the
// caller supplied one. Code is a stable machine-readable token;
// Message is human-readable. Distinct from api.ItemFailure (which
// uses ItemID for pre-existing record references in
// SessionCommit/CollectionMigrate).
type BatchItemFailure struct {
	Index     int    `json:"index"`
	ClientRef string `json:"client_ref,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// CaptureBatchStats summarizes the outcome counts. Always populated
// (zero values when nothing happened).
type CaptureBatchStats struct {
	TotalItems      int `json:"total_items"`
	AddedCount      int `json:"added_count"`
	FailedCount     int `json:"failed_count"`
	SupersededCount int `json:"superseded_count"`
}

// CaptureBatchResponse is the canonical output of CaptureBatch. Sync
// mode populates Added/Failed/Stats inline; async mode (Layer 5) fills
// JobID + Status only and the caller polls.
type CaptureBatchResponse struct {
	JobID    string              `json:"job_id"`
	Status   string              `json:"status"`
	Added    []CaptureBatchAdded `json:"added,omitempty"`
	Failed   []BatchItemFailure  `json:"failed,omitempty"`
	Stats    CaptureBatchStats   `json:"stats"`
	Warnings []string            `json:"warnings,omitempty"`
}

// CaptureBatchDescription is the MCP tool description shared by every
// transport (MCP server registration and CLI MCP proxy).
const CaptureBatchDescription = `Store up to 500 knowledge records in a single call. Each item follows the gramaton_capture shape; the batch shares one engine write lock and one embed call, so wall-clock latency is far lower than the sum of per-item gramaton_capture calls.

Use this when the caller has already collected a batch (migration, file import, conversation extraction). For a single record use gramaton_capture; for tasks/checklists use gramaton_collection_add_batch.

client_token + an exact-match request body returns the prior job_id idempotently (safe to retry on transport failure). A different body with the same token is rejected with conflict. skip_supersession=true disables auto-dedup for migration imports.

Per-item failures land in the response's failed[] array (the batch keeps going); the only request-level errors are validation (item count, byte budget) and client_token reuse with a different body.`

// CaptureBatch runs the synchronous capture-batch path. Layer 3 scope:
// validation, batch embed off-lock with per-item fallback, single
// chunked commit under one engine write lock, JobStore lifecycle, and
// per-batch rollback of in-memory indexes on Save failure. Layer 4
// adds intra-batch edges; Layer 5 adds async mode and chunked commits.
func (a *API) CaptureBatch(ctx context.Context, req CaptureBatchRequest) (CaptureBatchResponse, *APIError) {
	if req.Wait != nil && !*req.Wait {
		return CaptureBatchResponse{}, ErrInvalid("wait=false (async mode) is not yet implemented")
	}

	if err := validateBatchEnvelope(req); err != nil {
		return CaptureBatchResponse{}, ErrInvalid(err.Error())
	}

	store := a.engine.JobStore()
	if store == nil {
		return CaptureBatchResponse{}, ErrUnavailable("jobstore unavailable")
	}

	canonical, err := canonicalizeRequest(req)
	if err != nil {
		a.log.Warn("capture_batch canonicalize failed", "err", err)
		return CaptureBatchResponse{}, ErrInternal("failed to canonicalize request")
	}
	requestHash := hashCanonical(canonical)

	// ClientToken idempotency. A prior completed/running call with the
	// same body returns the same JobID; mismatching body rejected;
	// failed/cancelled prior gets a fresh job linked via SupersedesJobID.
	var supersedesJobID string
	if req.ClientToken != "" {
		prior, err := store.FindByClientToken(req.ClientToken)
		if err != nil {
			a.log.Warn("capture_batch client_token lookup failed", "err", err)
			return CaptureBatchResponse{}, ErrInternal("failed to look up client_token")
		}
		if prior != nil {
			switch prior.Status {
			case jobs.StatusCompleted, jobs.StatusRunning, jobs.StatusPending:
				if prior.RequestHash != requestHash {
					return CaptureBatchResponse{}, ErrConflict("client_token reused with different request body")
				}
				return idempotentResponse(prior), nil
			case jobs.StatusFailed, jobs.StatusCancelled:
				supersedesJobID = prior.ID
			}
		}
	}

	now := time.Now().UTC()
	jobID := ulid.Make().String()
	job := &jobs.Job{
		ID:              jobID,
		Kind:            jobs.KindCaptureBatch,
		Status:          jobs.StatusRunning,
		CreatedAt:       now,
		StartedAt:       now,
		ClientToken:     req.ClientToken,
		RequestHash:     requestHash,
		SupersedesJobID: supersedesJobID,
		TotalItems:      len(req.Items),
		ClientRefToID:   make(map[string]string),
	}
	if err := store.Create(job); err != nil {
		a.log.Warn("capture_batch job create failed", "err", err)
		return CaptureBatchResponse{}, ErrInternal("failed to create job")
	}

	// Phase 0/1: per-item validation off-lock. Failures stay attached
	// to the original request index so the response order is stable.
	itemValid := make([]bool, len(req.Items))
	failures := make([]BatchItemFailure, 0)
	clientRefSeen := make(map[string]int, len(req.Items))
	for i := range req.Items {
		item := &req.Items[i]
		if item.ClientRef != "" {
			if prev, dup := clientRefSeen[item.ClientRef]; dup {
				failures = append(failures, BatchItemFailure{
					Index:     i,
					ClientRef: item.ClientRef,
					Code:      "duplicate_client_ref",
					Message:   fmt.Sprintf("client_ref %q already used by item %d", item.ClientRef, prev),
				})
				continue
			}
			clientRefSeen[item.ClientRef] = i
		}
		if err := validateBatchItem(item); err != nil {
			failures = append(failures, BatchItemFailure{
				Index:     i,
				ClientRef: item.ClientRef,
				Code:      "input_error",
				Message:   err.Error(),
			})
			continue
		}
		itemValid[i] = true
	}

	// Phase 2: batch embed off-lock with per-item fallback.
	itemVecs := make([][]float32, len(req.Items))
	itemEmbedErrs := make([]error, len(req.Items))
	itemEmbedText := make([]string, len(req.Items))
	for i, item := range req.Items {
		if !itemValid[i] {
			continue
		}
		itemEmbedText[i] = embedTextForBatch(item.CaptureRequest)
	}
	a.batchEmbed(ctx, itemValid, itemEmbedText, itemVecs, itemEmbedErrs)
	embedderModel := ""
	if a.engine.Embedder() != nil {
		embedderModel = a.engine.Embedder().ModelID()
	}

	// Phase 3: lock once; commit valid items together; rollback the
	// in-memory indexes if Save fails so vector and BM25 indexes don't
	// retain entries for nodes that didn't land on disk.
	type rollbackEntry struct {
		nodeID string
		props  graph.Properties
	}
	rollback := make([]rollbackEntry, 0, len(req.Items))
	added := make([]CaptureBatchAdded, len(req.Items))
	addedFlag := make([]bool, len(req.Items))
	supersededTotal := 0

	a.engine.Lock()
	unlocked := false
	unlock := func() {
		if !unlocked {
			a.engine.Unlock()
			unlocked = true
		}
	}
	defer unlock()
	actions := make([]graph.CommitAction, 0, len(req.Items))
	for i, item := range req.Items {
		if !itemValid[i] {
			continue
		}
		props := graph.Properties{
			"content_full": graph.StringProperty(item.Content),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
			"access_count": graph.Int64Property(0),
		}
		hasClassification := item.Temporality != "" || item.Confidence != nil
		if hasClassification {
			props["processing_status"] = graph.StringProperty("processed")
		} else {
			props["processing_status"] = graph.StringProperty("captured")
		}
		setOptionalProps(props, item.CaptureRequest)

		n := a.engine.Graph().AddNode(props)
		rollback = append(rollback, rollbackEntry{nodeID: n.ID, props: n.Properties})

		bm25Text := item.Content
		if metaText := metaBM25Text(item.Meta); metaText != "" {
			bm25Text += " " + metaText
		}
		a.engine.IndexNode(n.ID, bm25Text, nil)

		if len(item.Meta) > 0 {
			a.setMetaProps(n.ID, item.Meta)
		}
		// Orphan-recovery stamp. Search by meta._gramaton.import.job_id
		// finds every record this batch wrote even if the response is
		// lost. Set after user meta so reserved-namespace validation
		// can't be tricked into letting the user set this.
		a.engine.SetProp(n.ID, "meta._gramaton.import.job_id", graph.StringProperty(jobID))

		var itemWarnings []string
		if itemEmbedErrs[i] != nil {
			itemWarnings = append(itemWarnings, fmt.Sprintf("embedding failed: %s", itemEmbedErrs[i]))
		} else if itemVecs[i] != nil {
			pre := &preEmbeddedVectors{
				vectors: map[string][]float32{"embedding_full": itemVecs[i]},
				model:   embedderModel,
			}
			if err := a.applyPreEmbedded(n.ID, pre); err != nil {
				itemWarnings = append(itemWarnings, fmt.Sprintf("embedding failed: %s", err))
			}
		}

		var supList []SupersededRecord
		if !req.SkipSupersession {
			supList = a.batchSupersedeIfDuplicate(n.ID)
			supersededTotal += len(supList)
		}

		added[i] = CaptureBatchAdded{
			ID:         n.ID,
			ClientRef:  item.ClientRef,
			Warnings:   itemWarnings,
			Superseded: supList,
		}
		addedFlag[i] = true
		if item.ClientRef != "" {
			job.ClientRefToID[item.ClientRef] = n.ID
		}
		actions = append(actions, graph.CommitAction{Kind: graph.ActionCapture, RecordID: n.ID})
	}

	// Engine first, JobStore second. The fault injector lets tests
	// simulate a Save failure without disturbing bbolt; in production
	// it returns nil and the actual Save runs.
	saveErr := a.injectFault(FaultPhaseChunkSave)
	if saveErr == nil {
		_, saveErr = a.engine.Save("capture_batch", actions...)
	}
	if saveErr != nil {
		for _, e := range rollback {
			// Re-read current props so RemoveNode purges every entry
			// added after AddNode (meta, embedding, orphan stamp).
			// AddNode's snapshot misses those because it cloned the
			// caller's props before the post-AddNode SetProp/Add calls.
			currentProps := e.props
			if n, ok := a.engine.Graph().GetNode(e.nodeID); ok && n != nil {
				currentProps = n.Properties
			}
			a.engine.PropIdx().RemoveNode(e.nodeID, currentProps)
			a.engine.VecIdx().Remove(e.nodeID)
			a.engine.BM25Full().Remove(e.nodeID)
			a.engine.Graph().DeleteNode(e.nodeID)
		}
		unlock()
		a.log.Error("capture_batch save failed", "job_id", jobID, "err", saveErr)
		job.Status = jobs.StatusFailed
		job.FailureReason = "chunk_1_save_failed"
		job.CompletedAt = time.Now().UTC()
		if uerr := store.Update(job); uerr != nil {
			a.log.Error("capture_batch job update on save failure failed", "job_id", jobID, "err", uerr)
		}
		return CaptureBatchResponse{}, ErrInternal("failed to save batch")
	}
	unlock()

	// Build the response payload before flipping Job state so a
	// JobStore failure here still leaves the records on disk and the
	// Job in a clean terminal state.
	finalAdded := make([]CaptureBatchAdded, 0, len(req.Items))
	for i := range req.Items {
		if addedFlag[i] {
			finalAdded = append(finalAdded, added[i])
		}
	}
	job.Status = jobs.StatusCompleted
	job.CompletedAt = time.Now().UTC()
	job.ProcessedCount = len(finalAdded) + len(failures)
	resp := CaptureBatchResponse{
		JobID:  jobID,
		Status: jobs.StatusCompleted,
		Added:  finalAdded,
		Failed: failures,
		Stats: CaptureBatchStats{
			TotalItems:      len(req.Items),
			AddedCount:      len(finalAdded),
			FailedCount:     len(failures),
			SupersededCount: supersededTotal,
		},
	}
	if data, err := json.Marshal(resp); err == nil {
		job.Result = data
	}
	job.Errors = errorsFromFailures(failures)
	updateErr := a.injectFault(FaultPhaseJobstoreUpdate)
	if updateErr == nil {
		updateErr = store.Update(job)
	}
	if updateErr != nil {
		// Records ARE in the store; only Job bookkeeping failed.
		// Caller can recover via gramaton_search(meta={"_gramaton.import.job_id": ...}).
		a.log.Error("capture_batch jobstore update failed after save",
			"job_id", jobID, "added", len(finalAdded), "err", updateErr)
	}
	return resp, nil
}

// batchEmbed runs the embedder over valid items. On batch failure it
// retries each item individually so a single bad input doesn't fail
// the whole batch. itemValid is parallel to itemEmbedText/itemVecs/
// itemEmbedErrs, indexed by request position.
func (a *API) batchEmbed(ctx context.Context, valid []bool, texts []string, vecs [][]float32, errs []error) {
	emb := a.engine.Embedder()
	if emb == nil {
		return
	}
	originIdx := make([]int, 0, len(texts))
	embedTexts := make([]string, 0, len(texts))
	for i, t := range texts {
		if !valid[i] || t == "" {
			continue
		}
		originIdx = append(originIdx, i)
		embedTexts = append(embedTexts, t)
	}
	if len(embedTexts) == 0 {
		return
	}
	out, err := emb.Embed(ctx, embedTexts)
	if err == nil && len(out) == len(embedTexts) {
		for j, v := range out {
			vecs[originIdx[j]] = v
		}
		return
	}
	if err != nil {
		a.log.Warn("capture_batch batch embed failed; falling back per-item", "err", err)
	}
	// Per-item fallback. ctx propagates so cancellation short-circuits
	// remaining items.
	for j, t := range embedTexts {
		if ctx.Err() != nil {
			errs[originIdx[j]] = ctx.Err()
			continue
		}
		v, err := emb.Embed(ctx, []string{t})
		if err != nil {
			errs[originIdx[j]] = err
			continue
		}
		if len(v) > 0 {
			vecs[originIdx[j]] = v[0]
		}
	}
}

// batchSupersedeIfDuplicate inlines the dedup logic from Capture so
// each batch item runs the same auto-supersession check. Caller holds
// the engine write lock. Returns the list of records superseded by
// this item (zero or one in practice).
func (a *API) batchSupersedeIfDuplicate(newID string) []SupersededRecord {
	cfg := a.engine.Config()
	if cfg.Dedup.Action == "reject" {
		return nil
	}
	dupID, sim := a.engine.CheckDedup(newID)
	if dupID == "" {
		return nil
	}
	now := time.Now().UTC()
	oldNode, _ := a.engine.Graph().GetNode(dupID)
	if oldNode == nil {
		return nil
	}
	if _, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until"); alreadyHistorical {
		return nil
	}
	a.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
	a.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
	a.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
	e, err := a.engine.Graph().AddEdge(newID, dupID, "supersedes", sim, nil)
	if err != nil {
		return nil
	}
	summary, _ := oldNode.Properties.GetString("content_short")
	return []SupersededRecord{{
		ID:         dupID,
		Summary:    summary,
		Similarity: sim,
		EdgeID:     e.ID,
	}}
}

// embedTextForBatch derives the embedding text for one batch item,
// preferring SummaryShort and capping Content at MaxSummaryShort to
// match Capture's geometry.
func embedTextForBatch(r CaptureRequest) string {
	if r.SummaryShort != "" {
		return r.SummaryShort
	}
	cap := MaxSummaryShort()
	if len(r.Content) > cap {
		return r.Content[:cap]
	}
	return r.Content
}

// idempotentResponse rebuilds a CaptureBatchResponse from a stored Job
// record. Used when ClientToken matches a prior call.
func idempotentResponse(j *jobs.Job) CaptureBatchResponse {
	resp := CaptureBatchResponse{
		JobID:  j.ID,
		Status: j.Status,
	}
	if len(j.Result) > 0 {
		_ = json.Unmarshal(j.Result, &resp)
		// Live Status from the Job record overrides any stale value
		// in the Result payload.
		resp.Status = j.Status
		resp.JobID = j.ID
	}
	return resp
}

// errorsFromFailures projects per-item BatchItemFailure into the
// jobs.ItemError shape stored on the Job record.
func errorsFromFailures(in []BatchItemFailure) []jobs.ItemError {
	if len(in) == 0 {
		return nil
	}
	out := make([]jobs.ItemError, len(in))
	for i, f := range in {
		out[i] = jobs.ItemError{
			Index:     f.Index,
			ClientRef: f.ClientRef,
			Code:      f.Code,
			Message:   f.Message,
		}
	}
	return out
}

// validateBatchEnvelope checks the request-level invariants that don't
// depend on per-item content. Item-level checks live in
// validateBatchItem.
func validateBatchEnvelope(req CaptureBatchRequest) error {
	if len(req.Items) == 0 {
		return errors.New("items must not be empty")
	}
	if len(req.Items) > MaxSyncBatchSize {
		return fmt.Errorf("items exceeds maximum of %d", MaxSyncBatchSize)
	}
	if req.ClientToken != "" {
		if err := validateClientToken(req.ClientToken); err != nil {
			return err
		}
	}
	var bytes int64
	for i, item := range req.Items {
		bytes += int64(len(item.Content))
		if bytes > MaxBatchBytes {
			return fmt.Errorf("items[%d]: total content bytes exceed %d", i, MaxBatchBytes)
		}
	}
	return nil
}

// validateBatchItem runs the same field-level validation as Capture
// plus the additional batch-only constraints (ClientRef shape,
// reserved meta-key namespace).
func validateBatchItem(item *CaptureBatchItem) error {
	if item.Content == "" {
		return errors.New("content is required")
	}
	if item.ClientRef != "" {
		if err := validateClientRef(item.ClientRef); err != nil {
			return err
		}
	}
	if err := validateReservedMetaNamespace(item.Meta); err != nil {
		return err
	}
	if err := validateMeta(item.Meta); err != nil {
		return err
	}
	if err := validateCaptureRequest(&item.CaptureRequest); err != nil {
		return err
	}
	return nil
}

// validateReservedMetaNamespace rejects any meta key that uses or
// nests inside the `_gramaton.` namespace. Reserved for orphan-
// recovery stamps and future internal bookkeeping; allowing a caller
// to write here would let them shadow the import.job_id stamp.
func validateReservedMetaNamespace(meta map[string]any) error {
	for k := range meta {
		if strings.HasPrefix(k, reservedMetaPrefix) || strings.Contains(k, reservedMetaInfix) {
			return fmt.Errorf("meta key %q uses reserved namespace %q", k, reservedMetaPrefix)
		}
	}
	return nil
}

// hashCanonical returns the hex SHA256 of the canonical bytes.
func hashCanonical(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
