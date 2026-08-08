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

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/jobs"
	"github.com/gramaton-ai/gramaton/similarity"
	"github.com/oklog/ulid/v2"
)

// SaveBatchRequest captures up to MaxSyncBatchSize records in a
// single call. Items follow the same shape as SaveRequest with an
// optional ClientRef for in-batch identity.
//
// Layer 3 honors only the synchronous path; passing Wait=false returns
// ErrInvalid until Layer 5 wires the async runner. ClientToken +
// canonicalized RequestHash provide cross-call idempotency.
type SaveBatchRequest struct {
	Items        []SaveBatchItem `json:"items" jsonschema:"items to capture (1 to MaxSyncBatchSize); each follows the gramaton_save shape with an optional client_ref"`
	Edges        []EdgeSpec      `json:"edges,omitempty" jsonschema:"intra-batch and to-existing-record edges. Capped at 10x item count. Each edge resolves source/target via either an existing record id or an in-batch client_ref."`
	Wait         *bool           `json:"wait,omitempty" jsonschema:"true (sync, default) returns the full result inline; false (async) returns a job_id to poll. Layer 5 implements async; Layer 3 rejects wait=false."`
	ClientToken  string          `json:"client_token,omitempty" jsonschema:"UUID. With identical request body returns the prior JobID idempotently; with a different body the same token is rejected."`
	AllowSimilar bool            `json:"allow_similar,omitempty" jsonschema:"when true, similar-record holds are disabled for the entire batch. For migration imports and bulk ingestion of pre-vetted content; never as a standing default."`
}

// EdgeSpec describes a single edge to create alongside the batch's
// items. Exactly one of (SourceID, SourceClientRef) must be set per
// endpoint; same for target. ID resolves to an existing record OR a
// successful item from this batch. ClientRef resolves only to a
// successful item in this batch.
type EdgeSpec struct {
	SourceID        string   `json:"source_id,omitempty" jsonschema:"existing record id OR id assigned to a successful batch item"`
	SourceClientRef string   `json:"source_client_ref,omitempty" jsonschema:"client_ref of a batch item (mutually exclusive with source_id)"`
	TargetID        string   `json:"target_id,omitempty" jsonschema:"existing record id OR id assigned to a successful batch item"`
	TargetClientRef string   `json:"target_client_ref,omitempty" jsonschema:"client_ref of a batch item (mutually exclusive with target_id)"`
	Type            string   `json:"type" jsonschema:"edge type (e.g. related_to, supports, contradicts)"`
	Weight          *float64 `json:"weight,omitempty" jsonschema:"0.0-1.0; default 0.5"`
}

// EdgeAdded describes one edge that was successfully created. Index
// maps back to the original Edges slice so callers can correlate.
type EdgeAdded struct {
	Index    int     `json:"index"`
	EdgeID   string  `json:"edge_id"`
	SourceID string  `json:"source_id"`
	TargetID string  `json:"target_id"`
	Type     string  `json:"type"`
	Weight   float64 `json:"weight"`
}

// EdgeFailure describes one edge that did NOT commit. Code is one of:
//   - source_item_failed / target_item_failed: the referenced
//     ClientRef points at an item whose Phase 0 validation failed
//   - source_id_not_found / target_id_not_found: the referenced ID
//     (or ClientRef) doesn't resolve to any record
//   - self_loop: source and target resolve to the same node
//   - duplicate_edge: same (source, target, type) tuple appears
//     earlier in the batch
//   - invalid_type / invalid_weight: per-edge shape rejected
//   - missing_endpoint: neither id nor client_ref supplied for an
//     endpoint, or both supplied for the same endpoint
type EdgeFailure struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SaveBatchItem is one record in a SaveBatchRequest. Embeds the
// existing single-capture shape to keep field semantics identical and
// adds a per-item ClientRef the caller can use to wire intra-batch
// edges (Layer 4).
type SaveBatchItem struct {
	SaveRequest
	ClientRef string `json:"client_ref,omitempty" jsonschema:"caller-supplied label unique within the batch (max 128 chars; letters, digits, dot, hyphen, underscore); echoed back in the response and used by Edges in Layer 4."`
}

// CaptureBatchAdded describes one record that the batch successfully
// committed. ClientRef is echoed back when the request supplied one.
// Warnings collect non-fatal events (embed fallback etc.) per item.
type CaptureBatchAdded struct {
	ID        string   `json:"id"`
	ClientRef string   `json:"client_ref,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// CaptureBatchHeld describes one item the save guard held: nothing
// was created for it. Index maps back to the original Items slice.
// Held carries the similar record and the two exits (see the
// gramaton_save hold contract); for a same-batch sibling match the
// similar record is the earlier batch item, already created.
type CaptureBatchHeld struct {
	Index     int          `json:"index"`
	ClientRef string       `json:"client_ref,omitempty"`
	Held      *HeldSimilar `json:"held"`
}

// BatchItemFailure describes one item that did NOT commit. Index maps
// back to the original Items slice; ClientRef is echoed when the
// caller supplied one. Code is a stable machine-readable token;
// Message is human-readable. Distinct from api.ItemFailure (which
// uses ItemID for pre-existing record references in
// SessionSave/CollectionMigrate).
type BatchItemFailure struct {
	Index     int    `json:"index"`
	ClientRef string `json:"client_ref,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// CaptureBatchStats summarizes the outcome counts. Always populated
// (zero values when nothing happened).
type CaptureBatchStats struct {
	TotalItems  int `json:"total_items"`
	AddedCount  int `json:"added_count"`
	FailedCount int `json:"failed_count"`
	HeldCount   int `json:"held_count"`
	EdgesAdded  int `json:"edges_added,omitempty"`
	EdgesFailed int `json:"edges_failed,omitempty"`
}

// SaveBatchResponse is the canonical output of SaveBatch. Sync
// mode populates Added/Failed/Stats inline; async mode (Layer 5) fills
// JobID + Status only and the caller polls.
type SaveBatchResponse struct {
	JobID       string              `json:"job_id"`
	Status      string              `json:"status"`
	Added       []CaptureBatchAdded `json:"added,omitempty"`
	Held        []CaptureBatchHeld  `json:"held,omitempty"`
	Failed      []BatchItemFailure  `json:"failed,omitempty"`
	Edges       []EdgeAdded         `json:"edges,omitempty"`
	EdgesFailed []EdgeFailure       `json:"edges_failed,omitempty"`
	Stats       CaptureBatchStats   `json:"stats"`
	Warnings    []string            `json:"warnings,omitempty"`
}

// SaveBatchDescription is the MCP tool description shared by every
// transport (MCP server registration and CLI MCP proxy).
const SaveBatchDescription = `Store up to 500 knowledge records in a single call. Each item follows the gramaton_save shape; the batch shares one engine write lock and one embed call, so wall-clock latency is far lower than the sum of per-item gramaton_save calls.

Use this when the caller has already collected a batch (migration, file import, conversation extraction). For a single record use gramaton_save; for tasks/checklists use gramaton_collection_add_batch.

client_token + an exact-match request body returns the prior job_id idempotently (safe to retry on transport failure). A different body with the same token is rejected with conflict.

Items that closely match an existing record (or an earlier item in the same batch) are HELD rather than created and land in the response's held[] array with the similar record's details -- update that record instead, or re-send with allow_similar naming the acknowledged record IDs. Per-item failures land in failed[] (the batch keeps going); the only request-level errors are validation (item count, byte budget) and client_token reuse with a different body.`

// SaveBatch dispatches to either the synchronous core or the
// async runner based on req.Wait. Both paths share envelope
// validation, ClientToken idempotency, and Job.Create up front; the
// per-item commit work runs inline (sync) or in a goroutine (async).
//
// L5 single-chunk runner: the entire batch commits in one Save call
// in the runner goroutine (same shape as sync, just off the request
// path). L6 introduces multi-chunk + cross-chunk edge fixup.
func (a *API) SaveBatch(ctx context.Context, req SaveBatchRequest) (SaveBatchResponse, *APIError) {
	// Guarding the shared entry covers both the sync path and the
	// async runner spawn (L5 single-chunk and L6 chunked): no job is
	// created and no runner goroutine starts on a frozen store.
	if apiErr := a.rejectIfReadOnly("save_batch"); apiErr != nil {
		return SaveBatchResponse{}, apiErr
	}
	asyncMode := req.Wait != nil && !*req.Wait
	cfg := a.engine.Config()
	itemCap := cfg.Jobs.MaxSyncBatchSize
	if itemCap <= 0 {
		itemCap = MaxSyncBatchSize
	}
	if asyncMode {
		itemCap = cfg.Jobs.MaxAsyncBatchSize
		if itemCap <= 0 {
			itemCap = MaxAsyncBatchSize
		}
	}
	byteCap := cfg.Jobs.MaxBatchBytes
	if byteCap <= 0 {
		byteCap = MaxBatchBytes
	}
	if err := validateBatchEnvelope(req, itemCap, byteCap); err != nil {
		return SaveBatchResponse{}, ErrInvalid(err.Error())
	}

	store := a.engine.JobStore()
	if store == nil {
		return SaveBatchResponse{}, ErrUnavailable("jobstore unavailable")
	}

	canonical, err := canonicalizeRequest(req)
	if err != nil {
		a.log.Warn("capture_batch canonicalize failed", "err", err)
		return SaveBatchResponse{}, ErrInternal("failed to canonicalize request")
	}
	requestHash := hashCanonical(canonical)

	// ClientToken idempotency. A prior completed/running call with the
	// same body returns the same JobID; mismatching body rejected;
	// failed/cancelled prior gets a fresh job linked via SupersedesJobID.
	// Lookup is scoped to the caller's tenant so the same token used
	// by a different tenant never collides.
	tenant := tenantFromContext(ctx)
	var supersedesJobID string
	if req.ClientToken != "" {
		prior, err := store.FindByClientToken(req.ClientToken, tenant)
		if err != nil {
			a.log.Warn("capture_batch client_token lookup failed", "err", err)
			return SaveBatchResponse{}, ErrInternal("failed to look up client_token")
		}
		if prior != nil {
			switch prior.Status {
			case jobs.StatusCompleted, jobs.StatusRunning, jobs.StatusPending:
				if prior.RequestHash != requestHash {
					return SaveBatchResponse{}, ErrConflict("client_token reused with different request body")
				}
				return idempotentResponse(prior), nil
			case jobs.StatusFailed, jobs.StatusCancelled:
				supersedesJobID = prior.ID
			}
		}
	}

	now := time.Now().UTC()
	jobID := ulid.Make().String()
	initialStatus := jobs.StatusRunning
	if asyncMode {
		initialStatus = jobs.StatusPending
	}
	job := &jobs.Job{
		ID:              jobID,
		Kind:            jobs.KindCaptureBatch,
		Status:          initialStatus,
		CreatedAt:       now,
		ClientToken:     req.ClientToken,
		TenantID:        tenant,
		RequestHash:     requestHash,
		SupersedesJobID: supersedesJobID,
		TotalItems:      len(req.Items),
		ClientRefToID:   make(map[string]string),
	}
	if !asyncMode {
		job.StartedAt = now
	}
	if err := store.Create(job); err != nil {
		a.log.Warn("capture_batch job create failed", "err", err)
		return SaveBatchResponse{}, ErrInternal("failed to create job")
	}

	if asyncMode {
		runnerCtx, cancel := context.WithCancel(context.Background())
		if !a.registerAsyncRunner(jobID, cancel) {
			// API is shutting down. Mark job failed and return so the
			// caller doesn't believe a phantom runner is in flight.
			cancel()
			job.Status = jobs.StatusFailed
			job.FailureReason = "shutdown"
			job.CompletedAt = time.Now().UTC()
			if uerr := store.Update(job); uerr != nil {
				a.log.Warn("capture_batch shutdown job update failed", "job_id", jobID, "err", uerr)
			}
			return SaveBatchResponse{}, ErrUnavailable("server shutting down")
		}
		go a.runCaptureBatchAsync(runnerCtx, jobID, req)
		return SaveBatchResponse{
			JobID:  jobID,
			Status: jobs.StatusPending,
		}, nil
	}

	return a.runCaptureBatchCore(ctx, jobID, req, job)
}

// runCaptureBatchCore is the per-batch commit pipeline shared by the
// sync path and the async runner. It assumes the Job already exists in
// the JobStore with a usable Status (Pending or Running). Caller is
// responsible for advancing Pending → Running before invoking this in
// the async path.
func (a *API) runCaptureBatchCore(ctx context.Context, jobID string, req SaveBatchRequest, job *jobs.Job) (SaveBatchResponse, *APIError) {
	store := a.engine.JobStore()
	// Test-only panic-injection seam. The async runner's
	// recoverAsyncPanic deferred handler observes the panic and marks
	// the Job failed/panicked. Production never sets the injector so
	// this branch is a no-op.
	if err := a.injectFault(FaultPhasePanic); err != nil {
		panic(err.Error())
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
		// Per-item content cap, matching the single-save path
		// (api/save.go). Previously only the aggregate MaxBatchBytes
		// bounded batch items, so one item could be the whole 256MB.
		if maxContent := a.engine.Config().Limits.MaxContentLength; maxContent > 0 && len(item.Content) > maxContent {
			failures = append(failures, BatchItemFailure{
				Index:     i,
				ClientRef: item.ClientRef,
				Code:      "input_error",
				Message:   fmt.Sprintf("content exceeds maximum length of %d bytes", maxContent),
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
		itemEmbedText[i] = embedTextForBatch(item.SaveRequest)
	}
	a.batchEmbed(ctx, itemValid, itemEmbedText, itemVecs, itemEmbedErrs)
	embedderModel := ""
	if a.engine.Embedder() != nil {
		embedderModel = a.engine.Embedder().ModelID()
	}

	// Phase 2.5: save-guard scans off-lock (skipped wholesale under
	// the batch-level allow_similar migration escape). Store scan per
	// item, then the same-call sibling pass -- items in one batch are
	// invisible to the index scan (none exists yet), so later items
	// are compared pairwise against earlier ones and hold naming the
	// earlier. The write-seq snapshot anchors the under-lock delta
	// re-scan.
	itemStoreHold := make([]*similarity.Match, len(req.Items))
	itemSiblingOf := make([]int, len(req.Items))
	for i := range itemSiblingOf {
		itemSiblingOf[i] = -1
	}
	var scanSeq uint64
	if !req.AllowSimilar {
		guardCfg := a.engine.Config().SaveGuard
		a.engine.RLock()
		scanSeq = a.engine.WriteSeq()
		for i := range req.Items {
			if !itemValid[i] || itemVecs[i] == nil {
				continue
			}
			out := a.engine.ScanSimilarVec(itemVecs[i], req.Items[i].Content)
			if out.Hold != nil && !ackContains(req.Items[i].AllowSimilar, out.Hold.NodeID) {
				itemStoreHold[i] = out.Hold
			}
		}
		a.engine.RUnlock()
		for i := range req.Items {
			if !itemValid[i] || itemVecs[i] == nil || itemStoreHold[i] != nil {
				continue
			}
			for j := 0; j < i; j++ {
				if !itemValid[j] || itemVecs[j] == nil || itemStoreHold[j] != nil {
					continue
				}
				if _, holds := similarity.SiblingMatch(guardCfg,
					itemVecs[i], req.Items[i].Content,
					itemVecs[j], req.Items[j].Content); holds {
					itemSiblingOf[i] = j
					break
				}
			}
		}
	}

	// Phase 2.7: long-document chunking off-lock, per item. Items the
	// guard already holds skip (the hold response discards the work);
	// a delta hold under the lock still wastes the prechunk -- the
	// same speculative tradeoff as the single-save path.
	itemPreChunk := make([]*core.PreChunkResult, len(req.Items))
	for i, item := range req.Items {
		if !itemValid[i] || itemStoreHold[i] != nil || itemSiblingOf[i] >= 0 {
			continue
		}
		if !a.engine.ShouldChunk(len(item.Content)) {
			continue
		}
		itemPreChunk[i] = a.engine.PreChunk(ctx, item.Content, item.SummaryShort)
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
	heldItems := make([]CaptureBatchHeld, 0)

	// Author attribution: composed once for the whole batch (see
	// api/save.go for the stamping contract).
	author := a.engine.Config().Author.String()

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

		// Save-guard hold decisions, under the lock. Store holds and
		// sibling holds were computed off-lock; the delta re-scan
		// covers records other writers committed in the gap. A held
		// item is not created; the response carries the material for
		// the judgment call. Sibling holds resolve against the
		// earlier item's just-created node (skipped if that item was
		// itself held).
		if !req.AllowSimilar {
			hold := itemStoreHold[i]
			if hold == nil && itemVecs[i] != nil {
				if m, found, _ := a.engine.SimilarInDelta(scanSeq, itemVecs[i], item.Content); found &&
					!ackContains(item.AllowSimilar, m.NodeID) {
					held := m
					hold = &held
				}
			}
			if hold == nil && itemSiblingOf[i] >= 0 && addedFlag[itemSiblingOf[i]] {
				j := itemSiblingOf[i]
				// The pair passed the off-lock gate; recompute the
				// cosine for the response.
				sim, _ := similarity.SiblingMatch(a.engine.Config().SaveGuard,
					itemVecs[i], item.Content, itemVecs[j], req.Items[j].Content)
				hold = &similarity.Match{NodeID: added[j].ID, Similarity: sim}
			}
			if hold != nil {
				if hs := a.buildHeldSimilar(hold); hs != nil {
					heldItems = append(heldItems, CaptureBatchHeld{
						Index:     i,
						ClientRef: item.ClientRef,
						Held:      hs,
					})
					continue
				}
			}
		}
		props := graph.Properties{
			"content_full": graph.StringProperty(item.Content),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
			"access_count": graph.Int64Property(0),
		}
		if author != "" {
			props["author"] = graph.StringProperty(author)
		}
		hasClassification := item.Temporality != "" || item.Confidence != nil
		if hasClassification {
			props["processing_status"] = graph.StringProperty("processed")
		} else {
			props["processing_status"] = graph.StringProperty("captured")
		}
		setOptionalProps(props, item.SaveRequest)

		n := a.engine.Graph().AddNode(props)
		rollback = append(rollback, rollbackEntry{nodeID: n.ID, props: n.Properties})

		bm25Text := item.Content
		if len(item.Keywords) > 0 {
			bm25Text += " " + strings.Join(item.Keywords, " ")
		}
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
			// The save-guard scan never ran for this item; mark it so
			// reembed re-runs the check when the vector arrives.
			a.engine.SetProp(n.ID, "similar_check_pending", graph.BoolProperty(true))
		} else if itemVecs[i] != nil {
			pre := &preEmbeddedVectors{
				vectors: map[string][]float32{"embedding_full": itemVecs[i]},
				model:   embedderModel,
			}
			if err := a.applyPreEmbedded(n.ID, pre); err != nil {
				itemWarnings = append(itemWarnings, fmt.Sprintf("embedding failed: %s", err))
				a.engine.SetProp(n.ID, "similar_check_pending", graph.BoolProperty(true))
			}
		}

		// Apply pre-chunked children (see api/save.go for the fresh-
		// props rationale). Children ride the same commit; a failed
		// Save purges them in the rollback loop below.
		if pc := itemPreChunk[i]; pc != nil {
			if parent, ok := a.engine.Graph().GetNode(n.ID); ok {
				if _, err := a.engine.ApplyChunks(n.ID, pc, parent.Properties); err != nil {
					itemWarnings = append(itemWarnings, fmt.Sprintf("chunking failed: %s", err))
				} else if pc.ParentVec != nil {
					a.engine.NoteRecentWrite(n.ID, pc.ParentVec)
				}
			}
		}

		added[i] = CaptureBatchAdded{
			ID:        n.ID,
			ClientRef: item.ClientRef,
			Warnings:  itemWarnings,
		}
		addedFlag[i] = true
		if item.ClientRef != "" {
			job.ClientRefToID[item.ClientRef] = n.ID
		}
		actions = append(actions, graph.CommitAction{Kind: graph.ActionSave, RecordID: n.ID})
	}

	// Edge resolution + commit. Each edge resolves source/target via
	// the in-batch ClientRef map, an existing record id, or both.
	// Failures land in edgesFailed[]; successes append an ActionLink
	// CommitAction for the same Save call as the items.
	failedItemRefs := make(map[string]struct{}, len(failures)+len(heldItems))
	for _, f := range failures {
		if f.ClientRef != "" {
			failedItemRefs[f.ClientRef] = struct{}{}
		}
	}
	// Held items were never created; edges referencing them fail the
	// same way as edges referencing failed items.
	for _, h := range heldItems {
		if h.ClientRef != "" {
			failedItemRefs[h.ClientRef] = struct{}{}
		}
	}
	type edgeKey struct{ source, target, etype string }
	seenEdges := make(map[edgeKey]int, len(req.Edges))
	edgesAdded := make([]EdgeAdded, 0, len(req.Edges))
	edgesFailed := make([]EdgeFailure, 0)
	for i, espec := range req.Edges {
		srcID, code, msg := a.resolveEdgeEndpoint(
			"source", espec.SourceID, espec.SourceClientRef,
			job.ClientRefToID, failedItemRefs,
		)
		if code != "" {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: code, Message: msg})
			continue
		}
		tgtID, code, msg := a.resolveEdgeEndpoint(
			"target", espec.TargetID, espec.TargetClientRef,
			job.ClientRefToID, failedItemRefs,
		)
		if code != "" {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: code, Message: msg})
			continue
		}
		if espec.Type == "" {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "invalid_type", Message: "type is required"})
			continue
		}
		if len(espec.Type) > MaxEdgeTypeLen {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "invalid_type", Message: fmt.Sprintf("type exceeds %d characters", MaxEdgeTypeLen)})
			continue
		}
		if err := validateFloat64Range("weight", espec.Weight, 0.0, 1.0); err != nil {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "invalid_weight", Message: err.Error()})
			continue
		}
		if srcID == tgtID {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "self_loop", Message: "source and target are the same record"})
			continue
		}
		key := edgeKey{source: srcID, target: tgtID, etype: espec.Type}
		if prev, dup := seenEdges[key]; dup {
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "duplicate_edge", Message: fmt.Sprintf("duplicate of edges[%d]", prev)})
			continue
		}
		seenEdges[key] = i
		weight := 0.5
		if espec.Weight != nil {
			weight = *espec.Weight
		}
		edge, err := a.engine.Graph().AddEdge(srcID, tgtID, espec.Type, weight, nil)
		if err != nil {
			// Should be unreachable: we already verified both endpoints
			// resolved to existing records. Treat as a transient
			// internal error to keep the batch moving.
			a.log.Warn("capture_batch unexpected AddEdge error",
				"job_id", jobID, "edge_index", i, "err", err)
			edgesFailed = append(edgesFailed, EdgeFailure{Index: i, Code: "internal_error", Message: "failed to create edge"})
			continue
		}
		edgesAdded = append(edgesAdded, EdgeAdded{
			Index: i, EdgeID: edge.ID, SourceID: srcID, TargetID: tgtID, Type: espec.Type, Weight: weight,
		})
		actions = append(actions, graph.CommitAction{Kind: graph.ActionLink, RecordID: srcID})
	}

	// Engine first, JobStore second. The fault injector lets tests
	// simulate a Save failure without disturbing bbolt; in production
	// it returns nil and the actual Save runs.
	saveErr := a.injectFault(FaultPhaseChunkSave)
	if saveErr == nil {
		_, saveErr = a.engine.Save("capture_batch", actions...)
	}
	if saveErr != nil {
		// Roll back edges first so DeleteNode below doesn't leave
		// dangling edge references in the in-memory edge store.
		for _, ea := range edgesAdded {
			if err := a.engine.Graph().DeleteEdge(ea.EdgeID); err != nil {
				a.log.Warn("capture_batch edge rollback failed",
					"job_id", jobID, "edge_id", ea.EdgeID, "err", err)
			}
		}
		for _, e := range rollback {
			// Section/chunk children created by ApplyChunks are not in
			// rollback[] (it holds parent IDs only); purge them first
			// or they survive as orphaned index entries for nodes that
			// never reached disk.
			for _, ce := range a.engine.Graph().EdgesTo(e.nodeID) {
				if ce.Type != "section_of" && ce.Type != "chunk_of" {
					continue
				}
				if child, ok := a.engine.Graph().GetNode(ce.SourceID); ok {
					a.engine.PropIdx().RemoveNode(child.ID, child.Properties)
					a.engine.VecIdx().Remove(child.ID)
					a.engine.BM25Full().Remove(child.ID)
					if sec := a.engine.SecIdx(); sec != nil {
						sec.RemoveNode(child.ID)
					}
					a.engine.Graph().DeleteNode(child.ID)
				}
			}
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
			if sec := a.engine.SecIdx(); sec != nil {
				sec.RemoveNode(e.nodeID)
			}
			a.engine.Graph().DeleteNode(e.nodeID)
		}
		unlock()
		a.log.Error("capture_batch save failed", "job_id", jobID, "err", saveErr)
		job.Status = jobs.StatusFailed
		// Sync path commits the entire batch in one Save (no chunking),
		// so a generic "save_failed" reason matches reality. The chunked
		// runner's chunk_N_save_failed taxonomy lives in capture_batch_chunked.go.
		job.FailureReason = "save_failed"
		job.CompletedAt = time.Now().UTC()
		if uerr := store.Update(job); uerr != nil {
			a.log.Error("capture_batch job update on save failure failed", "job_id", jobID, "err", uerr)
		}
		return SaveBatchResponse{}, ErrInternal("failed to save batch")
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
	// Held items were fully examined and disposed -- they count as
	// processed so a completed job's bookkeeping covers TotalItems.
	job.ProcessedCount = len(finalAdded) + len(heldItems) + len(failures)
	resp := SaveBatchResponse{
		JobID:       jobID,
		Status:      jobs.StatusCompleted,
		Added:       finalAdded,
		Held:        heldItems,
		Failed:      failures,
		Edges:       edgesAdded,
		EdgesFailed: edgesFailed,
		Stats: CaptureBatchStats{
			TotalItems:  len(req.Items),
			AddedCount:  len(finalAdded),
			FailedCount: len(failures),
			HeldCount:   len(heldItems),
			EdgesAdded:  len(edgesAdded),
			EdgesFailed: len(edgesFailed),
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

// resolveEdgeEndpoint returns the resolved record ID for one edge
// endpoint, or a (code, message) failure tuple. Caller already holds
// the engine write lock.
//
// Resolution rules:
//   - Both id and ref empty                → missing_endpoint
//   - Both id and ref set                  → missing_endpoint (caller must pick one)
//   - ref points at a failed item          → source_item_failed / target_item_failed
//   - ref doesn't appear in batch          → source_id_not_found / target_id_not_found
//   - id doesn't resolve to existing node  → source_id_not_found / target_id_not_found
//
// The role parameter is "source" or "target" so failure codes carry
// the right prefix.
func (a *API) resolveEdgeEndpoint(
	role, id, ref string,
	clientRefToID map[string]string,
	failedItemRefs map[string]struct{},
) (resolved, code, msg string) {
	hasID := id != ""
	hasRef := ref != ""
	if !hasID && !hasRef {
		return "", "missing_endpoint", role + ": neither id nor client_ref supplied"
	}
	if hasID && hasRef {
		return "", "missing_endpoint", role + ": id and client_ref are mutually exclusive"
	}
	if hasRef {
		if _, failed := failedItemRefs[ref]; failed {
			return "", role + "_item_failed", fmt.Sprintf("%s client_ref %q references an item that failed validation", role, ref)
		}
		assigned, ok := clientRefToID[ref]
		if !ok {
			return "", role + "_id_not_found", fmt.Sprintf("%s client_ref %q does not match any batch item", role, ref)
		}
		return assigned, "", ""
	}
	// id path: check whether the id is in the batch's freshly-assigned
	// IDs (rare but valid) or an existing record.
	if _, ok := a.engine.Graph().GetNode(id); !ok {
		return "", role + "_id_not_found", fmt.Sprintf("%s id %q not found", role, id)
	}
	return id, "", ""
}

// embedTextForBatch derives the embedding text for one batch item,
// preferring SummaryShort and capping Content at MaxSummaryShort to
// match Capture's geometry.
func embedTextForBatch(r SaveRequest) string {
	if r.SummaryShort != "" {
		return r.SummaryShort
	}
	cap := MaxSummaryShort()
	if len(r.Content) > cap {
		return r.Content[:cap]
	}
	return r.Content
}

// idempotentResponse rebuilds a SaveBatchResponse from a stored Job
// record. Used when ClientToken matches a prior call.
func idempotentResponse(j *jobs.Job) SaveBatchResponse {
	resp := SaveBatchResponse{
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
// depend on per-item content. itemCap is the mode-specific item count
// limit (MaxSyncBatchSize for sync, cfg.Jobs.MaxAsyncBatchSize or
// MaxAsyncBatchSize for async). byteCap is the cumulative content
// byte ceiling (cfg.Jobs.MaxBatchBytes, falling back to the
// MaxBatchBytes constant). Per-item checks live in validateBatchItem;
// per-edge checks live in resolveEdge.
func validateBatchEnvelope(req SaveBatchRequest, itemCap int, byteCap int64) error {
	if len(req.Items) == 0 {
		return errors.New("items must not be empty")
	}
	if len(req.Items) > itemCap {
		return fmt.Errorf("items exceeds maximum of %d", itemCap)
	}
	if cap := len(req.Items) * MaxBatchEdgeMultiplier; len(req.Edges) > cap {
		return fmt.Errorf("edges exceeds maximum of %d (%d items × %d)", cap, len(req.Items), MaxBatchEdgeMultiplier)
	}
	if req.ClientToken != "" {
		if err := validateClientToken(req.ClientToken); err != nil {
			return err
		}
	}
	var bytes int64
	for i, item := range req.Items {
		bytes += int64(len(item.Content))
		if bytes > byteCap {
			return fmt.Errorf("items[%d]: total content bytes exceed %d", i, byteCap)
		}
	}
	return nil
}

// validateBatchItem runs the same field-level validation as Capture
// plus the additional batch-only constraints (ClientRef shape,
// reserved meta-key namespace).
func validateBatchItem(item *SaveBatchItem) error {
	if item.Content == "" {
		return errors.New("content is required")
	}
	// client_token is the request-level idempotency key. The embedded
	// SaveRequest would otherwise accept it per-item and silently
	// ignore it (the canonical hash excludes it), so reject loudly.
	if item.ClientToken != "" {
		return errors.New("client_token is set on the batch request, not on items")
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
	if err := validateSaveRequest(&item.SaveRequest); err != nil {
		return err
	}
	return nil
}

// validateReservedMetaNamespace rejects any meta key that uses or
// nests inside the `_gramaton.` namespace. Reserved for orphan-
// recovery stamps and future internal bookkeeping; allowing a caller
// to write here would let them shadow the import.job_id stamp.
//
// The check normalizes by trimming whitespace and lowercasing so
// `" _GRAMATON.foo"` and `"\t_Gramaton.bar"` are also rejected --
// otherwise an attacker could plant a quasi-reserved-namespace key
// that fools casual auditors scanning for the prefix in lowercase.
func validateReservedMetaNamespace(meta map[string]any) error {
	for k := range meta {
		norm := strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(norm, reservedMetaPrefix) || strings.Contains(norm, reservedMetaInfix) {
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
