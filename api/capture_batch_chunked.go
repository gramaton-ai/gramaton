package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/jobs"
)

// runCaptureBatchAsyncChunked is the multi-chunk async pipeline. It
// replaces L5's "spawn → call runCaptureBatchCore once" pattern with:
//
//  1. Phase 0/1: validate every item once, off-lock.
//  2. Phase 2: embed every item once, off-lock (single batch call).
//  3. Phase 3: chunked commits. For each chunk of MaxSyncBatchSize:
//     re-read Job.Status (cancel check), acquire engine lock, commit
//     just that chunk's items in one Save, release lock, persist
//     progress (ProcessedCount + ClientRefToID).
//  4. Edge fixup: after all node chunks land, acquire lock, resolve
//     and commit ALL edges in one Save, release.
//  5. Mark Job completed, populate Result.
//
// Per-chunk save failure: scoped rollback removes only the failing
// chunk's nodes (prior chunks already on disk); Job.Status =
// failed/chunk_N_save_failed; Result.Added reflects every chunk
// before N.
//
// Edge fixup failure: every node chunk stayed on disk; Job.Status =
// failed/edge_fixup_failed; Result.EdgesFailed lists every edge with
// reason "fixup_failed" so the caller can replay via gramaton_link.
//
// Cancel between chunks: status check at chunk boundaries observes
// the flip, finalizes Job with whatever has committed so far.
func (a *API) runCaptureBatchAsyncChunked(ctx context.Context, jobID string, req CaptureBatchRequest, job *jobs.Job) {
	store := a.engine.JobStore()

	// Test-only panic-injection seam (same shape as runCaptureBatchCore).
	if err := a.injectFault(FaultPhasePanic); err != nil {
		panic(err.Error())
	}

	// Phase 0/1: per-item validation.
	itemValid, failures := a.validateAllBatchItems(req)
	failedRefs := map[string]struct{}{}
	for _, f := range failures {
		if f.ClientRef != "" {
			failedRefs[f.ClientRef] = struct{}{}
		}
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
	if e := a.engine.Embedder(); e != nil {
		embedderModel = e.ModelID()
	}

	// Aggregate state across chunks.
	summary := &chunkSummary{
		Added:         make([]CaptureBatchAdded, 0, len(req.Items)),
		Failures:      failures,
		ClientRefToID: map[string]string{},
		FailedRefs:    failedRefs,
	}

	// Chunker.
	chunks := chunkRanges(len(req.Items), a.chunkSize())

	for chunkIdx, rng := range chunks {
		// Cancel check between chunks. ctx.Err() observes a Cancel
		// endpoint signal; the persisted Status read picks up cancel
		// flips that arrived before the runner registered its ctx.
		if a.shouldStopChunked(ctx, jobID) {
			a.finalizeCancelledWithProgress(jobID, summary)
			return
		}

		chunkData, saveErr := a.commitItemsChunk(jobID, chunkIdx+1, len(chunks),
			req.Items[rng.start:rng.end], itemValid[rng.start:rng.end],
			itemVecs[rng.start:rng.end], itemEmbedErrs[rng.start:rng.end],
			embedderModel, req.SkipSupersession)
		if saveErr != nil {
			a.finalizeChunkSaveFailure(jobID, chunkIdx+1, summary, saveErr)
			return
		}
		summary.Added = append(summary.Added, chunkData.Added...)
		for k, v := range chunkData.NewRefs {
			summary.ClientRefToID[k] = v
		}
		summary.SupersededCount += chunkData.SupersededCount
		summary.ProcessedItems += chunkData.Processed

		// Persist per-chunk progress so a Status reader sees real
		// motion. AdvanceStatus(running, mutator) is idempotent for
		// same-state and enforces the transition whitelist if a
		// concurrent cancel raced us — that's a clean exit signal.
		if err := store.AdvanceStatus(jobID, jobs.StatusRunning, func(j *jobs.Job) {
			j.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
			j.ClientRefToID = copyRefMap(summary.ClientRefToID)
		}); err != nil {
			a.log.Warn("chunked: per-chunk progress update failed",
				"job_id", jobID, "chunk", chunkIdx+1, "err", err)
			// If the cancel won this race, the next loop iteration's
			// shouldStopChunked observes it.
		}
	}

	// Edge fixup phase.
	if a.shouldStopChunked(ctx, jobID) {
		a.finalizeCancelledWithProgress(jobID, summary)
		return
	}

	edgeData, fixupErr := a.commitEdgeFixup(jobID, req.Edges, summary.ClientRefToID, summary.FailedRefs)
	if fixupErr != nil {
		a.finalizeEdgeFixupFailure(jobID, summary, edgeData, fixupErr)
		return
	}

	a.finalizeChunkedCompleted(jobID, req.Items, summary, edgeData)
}

// chunkSummary aggregates per-chunk state for the final response.
type chunkSummary struct {
	Added           []CaptureBatchAdded
	Failures        []BatchItemFailure
	ClientRefToID   map[string]string
	FailedRefs      map[string]struct{}
	SupersededCount int
	ProcessedItems  int // valid items actually committed
}

// chunkRange describes one item-slice [start, end) in the full Items
// list.
type chunkRange struct{ start, end int }

func chunkRanges(total, size int) []chunkRange {
	if total <= 0 {
		return nil
	}
	if size <= 0 {
		size = total
	}
	out := make([]chunkRange, 0, (total+size-1)/size)
	for s := 0; s < total; s += size {
		e := s + size
		if e > total {
			e = total
		}
		out = append(out, chunkRange{s, e})
	}
	return out
}

// shouldStopChunked returns true if the runner should exit cleanly
// without committing further work (ctx cancelled OR Job.Status flipped
// to cancelled out-of-band).
func (a *API) shouldStopChunked(ctx context.Context, jobID string) bool {
	if ctx.Err() != nil {
		return true
	}
	store := a.engine.JobStore()
	if store == nil {
		return false
	}
	j, err := store.Get(jobID)
	if err != nil {
		return false
	}
	return j.Status == jobs.StatusCancelled
}

// validateAllBatchItems runs Phase 0/1 over every item in req,
// returning a parallel itemValid bool slice and a list of per-item
// failures keyed by original index.
func (a *API) validateAllBatchItems(req CaptureBatchRequest) ([]bool, []BatchItemFailure) {
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
	return itemValid, failures
}

// chunkData is the per-chunk output: items committed, new refs
// learned, etc.
type chunkData struct {
	Added           []CaptureBatchAdded
	NewRefs         map[string]string
	SupersededCount int
	Processed       int
}

// commitItemsChunk runs Phase 3 over one chunk's items. Acquires the
// engine write lock, walks valid items, runs Save with N
// ActionCapture entries, releases the lock. On Save failure performs
// scoped rollback (only this chunk's nodes) and returns the error.
//
// itemValid / itemVecs / itemEmbedErrs are parallel to items
// (already chunk-local — caller slices before passing). embedderModel
// labels embeddings for the applyPreEmbedded call.
func (a *API) commitItemsChunk(jobID string, chunkNum, totalChunks int,
	items []CaptureBatchItem, itemValid []bool, itemVecs [][]float32,
	itemEmbedErrs []error, embedderModel string, skipSupersession bool,
) (*chunkData, error) {
	type rollbackEntry struct {
		nodeID string
		props  graph.Properties
	}
	rollback := make([]rollbackEntry, 0, len(items))
	added := make([]CaptureBatchAdded, 0, len(items))
	newRefs := map[string]string{}
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

	actions := make([]graph.CommitAction, 0, len(items))
	for i, item := range items {
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
		if !skipSupersession {
			supList = a.batchSupersedeIfDuplicate(n.ID)
			supersededTotal += len(supList)
		}

		added = append(added, CaptureBatchAdded{
			ID:         n.ID,
			ClientRef:  item.ClientRef,
			Warnings:   itemWarnings,
			Superseded: supList,
		})
		if item.ClientRef != "" {
			newRefs[item.ClientRef] = n.ID
		}
		actions = append(actions, graph.CommitAction{Kind: graph.ActionCapture, RecordID: n.ID})
	}

	saveErr := a.injectFault(FaultPhaseChunkSave)
	if saveErr == nil {
		_, saveErr = a.engine.Save(fmt.Sprintf("capture_batch chunk %d/%d", chunkNum, totalChunks), actions...)
	}
	if saveErr != nil {
		for _, e := range rollback {
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
		return nil, saveErr
	}
	unlock()
	return &chunkData{
		Added:           added,
		NewRefs:         newRefs,
		SupersededCount: supersededTotal,
		Processed:       len(added),
	}, nil
}

// edgeFixupData is the post-fixup output.
type edgeFixupData struct {
	Added  []EdgeAdded
	Failed []EdgeFailure
}

// commitEdgeFixup runs the post-chunks edge fixup phase. Acquires the
// engine lock, resolves every edge, calls Save with M ActionLink
// entries, releases. On Save failure rolls back the edges so the
// in-memory edge store doesn't leak entries that aren't on disk.
func (a *API) commitEdgeFixup(jobID string, edges []EdgeSpec,
	clientRefToID map[string]string, failedRefs map[string]struct{},
) (*edgeFixupData, error) {
	if len(edges) == 0 {
		return &edgeFixupData{}, nil
	}

	type edgeKey struct{ source, target, etype string }
	seenEdges := make(map[edgeKey]int, len(edges))
	added := make([]EdgeAdded, 0, len(edges))
	failed := make([]EdgeFailure, 0)

	a.engine.Lock()
	unlocked := false
	unlock := func() {
		if !unlocked {
			a.engine.Unlock()
			unlocked = true
		}
	}
	defer unlock()

	actions := make([]graph.CommitAction, 0, len(edges))
	for i, espec := range edges {
		srcID, code, msg := a.resolveEdgeEndpoint(
			"source", espec.SourceID, espec.SourceClientRef,
			clientRefToID, failedRefs,
		)
		if code != "" {
			failed = append(failed, EdgeFailure{Index: i, Code: code, Message: msg})
			continue
		}
		tgtID, code, msg := a.resolveEdgeEndpoint(
			"target", espec.TargetID, espec.TargetClientRef,
			clientRefToID, failedRefs,
		)
		if code != "" {
			failed = append(failed, EdgeFailure{Index: i, Code: code, Message: msg})
			continue
		}
		if espec.Type == "" {
			failed = append(failed, EdgeFailure{Index: i, Code: "invalid_type", Message: "type is required"})
			continue
		}
		if len(espec.Type) > MaxEdgeTypeLen {
			failed = append(failed, EdgeFailure{Index: i, Code: "invalid_type", Message: fmt.Sprintf("type exceeds %d characters", MaxEdgeTypeLen)})
			continue
		}
		if err := validateFloat64Range("weight", espec.Weight, 0.0, 1.0); err != nil {
			failed = append(failed, EdgeFailure{Index: i, Code: "invalid_weight", Message: err.Error()})
			continue
		}
		if srcID == tgtID {
			failed = append(failed, EdgeFailure{Index: i, Code: "self_loop", Message: "source and target are the same record"})
			continue
		}
		key := edgeKey{source: srcID, target: tgtID, etype: espec.Type}
		if prev, dup := seenEdges[key]; dup {
			failed = append(failed, EdgeFailure{Index: i, Code: "duplicate_edge", Message: fmt.Sprintf("duplicate of edges[%d]", prev)})
			continue
		}
		seenEdges[key] = i
		weight := 0.5
		if espec.Weight != nil {
			weight = *espec.Weight
		}
		edge, err := a.engine.Graph().AddEdge(srcID, tgtID, espec.Type, weight, nil)
		if err != nil {
			a.log.Warn("capture_batch fixup unexpected AddEdge error",
				"job_id", jobID, "edge_index", i, "err", err)
			failed = append(failed, EdgeFailure{Index: i, Code: "internal_error", Message: "failed to create edge"})
			continue
		}
		added = append(added, EdgeAdded{
			Index: i, EdgeID: edge.ID, SourceID: srcID, TargetID: tgtID, Type: espec.Type, Weight: weight,
		})
		actions = append(actions, graph.CommitAction{Kind: graph.ActionLink, RecordID: srcID})
	}

	saveErr := a.injectFault(FaultPhaseEdgeFixup)
	if saveErr == nil && len(actions) > 0 {
		_, saveErr = a.engine.Save("capture_batch edge fixup", actions...)
	}
	if saveErr != nil {
		// Roll back successful edges so the in-memory store doesn't
		// hold edges that aren't on disk.
		for _, ea := range added {
			if err := a.engine.Graph().DeleteEdge(ea.EdgeID); err != nil {
				a.log.Warn("capture_batch fixup edge rollback failed",
					"job_id", jobID, "edge_id", ea.EdgeID, "err", err)
			}
		}
		unlock()
		// Convert successfully-added edges into "fixup_failed" entries
		// so the caller's recovery can replay them via gramaton_link.
		for _, ea := range added {
			failed = append(failed, EdgeFailure{Index: ea.Index, Code: "fixup_failed", Message: "edge fixup commit failed"})
		}
		return &edgeFixupData{Failed: failed}, saveErr
	}
	unlock()
	return &edgeFixupData{Added: added, Failed: failed}, nil
}

// --- Finalize helpers ---

// finalizeChunkedCompleted writes the terminal Job state for a fully-
// successful chunked run. Builds the response payload, persists it
// in Job.Result, and flips Status to completed.
func (a *API) finalizeChunkedCompleted(jobID string, items []CaptureBatchItem,
	summary *chunkSummary, edge *edgeFixupData,
) {
	resp := buildChunkedResponse(jobID, jobs.StatusCompleted, items, summary, edge)
	job := loadJobOrLog(a, jobID, "finalize-completed")
	if job == nil {
		return
	}
	job.Status = jobs.StatusCompleted
	job.CompletedAt = time.Now().UTC()
	job.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
	job.ClientRefToID = copyRefMap(summary.ClientRefToID)
	if data, err := json.Marshal(resp); err == nil {
		job.Result = data
	}
	job.Errors = errorsFromFailures(summary.Failures)
	updateErr := a.injectFault(FaultPhaseJobstoreUpdate)
	if updateErr == nil {
		updateErr = a.engine.JobStore().Update(job)
	}
	if updateErr != nil {
		a.log.Error("capture_batch chunked: final job update failed",
			"job_id", jobID, "err", updateErr)
	}
}

// finalizeChunkedRunning writes intermediate progress without flipping
// status. Used between chunks. (Currently inlined via AdvanceStatus
// in the main loop; kept as a helper for symmetry with the other
// finalizers in case future code needs it.)

// finalizeChunkSaveFailure handles a chunk-level Save error. Marks
// Job failed/chunk_N_save_failed; Result.Added reflects every chunk
// before N (the failing chunk's nodes already rolled back in-memory).
func (a *API) finalizeChunkSaveFailure(jobID string, chunkNum int,
	summary *chunkSummary, saveErr error,
) {
	a.log.Error("capture_batch chunked: chunk save failed",
		"job_id", jobID, "chunk", chunkNum, "err", saveErr)
	job := loadJobOrLog(a, jobID, "finalize-chunk-save-failure")
	if job == nil {
		return
	}
	resp := buildChunkedResponse(jobID, jobs.StatusFailed, nil, summary, &edgeFixupData{})
	if data, err := json.Marshal(resp); err == nil {
		job.Result = data
	}
	job.Status = jobs.StatusFailed
	job.FailureReason = fmt.Sprintf("chunk_%d_save_failed", chunkNum)
	job.CompletedAt = time.Now().UTC()
	job.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
	job.ClientRefToID = copyRefMap(summary.ClientRefToID)
	job.Errors = errorsFromFailures(summary.Failures)
	if err := a.engine.JobStore().Update(job); err != nil {
		a.log.Error("capture_batch chunked: chunk-fail job update failed",
			"job_id", jobID, "err", err)
	}
}

// finalizeEdgeFixupFailure marks Job failed/edge_fixup_failed.
// Result.Added has every node chunk; Result.EdgesFailed lists every
// edge with the specific failure code.
func (a *API) finalizeEdgeFixupFailure(jobID string, summary *chunkSummary,
	edge *edgeFixupData, fixupErr error,
) {
	a.log.Error("capture_batch chunked: edge fixup failed",
		"job_id", jobID, "err", fixupErr)
	job := loadJobOrLog(a, jobID, "finalize-edge-fixup-failure")
	if job == nil {
		return
	}
	resp := buildChunkedResponse(jobID, jobs.StatusFailed, nil, summary, edge)
	if data, err := json.Marshal(resp); err == nil {
		job.Result = data
	}
	job.Status = jobs.StatusFailed
	job.FailureReason = "edge_fixup_failed"
	job.CompletedAt = time.Now().UTC()
	job.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
	job.ClientRefToID = copyRefMap(summary.ClientRefToID)
	job.Errors = errorsFromFailures(summary.Failures)
	if err := a.engine.JobStore().Update(job); err != nil {
		a.log.Error("capture_batch chunked: edge-fixup-fail job update failed",
			"job_id", jobID, "err", err)
	}
}

// finalizeCancelledWithProgress is invoked when the runner observes a
// cancel between chunks. Items committed in completed chunks stay in
// the store; the Job is flipped to cancelled with a Result that
// reflects the partial state.
func (a *API) finalizeCancelledWithProgress(jobID string, summary *chunkSummary) {
	job := loadJobOrLog(a, jobID, "finalize-cancelled")
	if job == nil {
		return
	}
	if job.Status == jobs.StatusCancelled {
		// CaptureBatchCancel already wrote the terminal state; just
		// merge in the progress info.
		job.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
		job.ClientRefToID = copyRefMap(summary.ClientRefToID)
		resp := buildChunkedResponse(jobID, jobs.StatusCancelled, nil, summary, &edgeFixupData{})
		if data, err := json.Marshal(resp); err == nil {
			job.Result = data
		}
		job.Errors = errorsFromFailures(summary.Failures)
		if err := a.engine.JobStore().Update(job); err != nil {
			a.log.Warn("capture_batch chunked: cancel-merge update failed",
				"job_id", jobID, "err", err)
		}
		return
	}
	// Pending/Running -> Cancelled. AdvanceStatus enforces the
	// transition whitelist.
	if err := a.engine.JobStore().AdvanceStatus(jobID, jobs.StatusCancelled, func(j *jobs.Job) {
		j.CompletedAt = time.Now().UTC()
		j.ProcessedCount = summary.ProcessedItems + len(summary.Failures)
		j.ClientRefToID = copyRefMap(summary.ClientRefToID)
		resp := buildChunkedResponse(jobID, jobs.StatusCancelled, nil, summary, &edgeFixupData{})
		if data, err := json.Marshal(resp); err == nil {
			j.Result = data
		}
		j.Errors = errorsFromFailures(summary.Failures)
	}); err != nil {
		a.log.Warn("capture_batch chunked: cancel finalize failed",
			"job_id", jobID, "err", err)
	}
}

// buildChunkedResponse assembles a CaptureBatchResponse from the
// across-chunks summary + edge fixup data. The items argument is
// ignored on failure paths (only used to populate Stats.TotalItems
// on the success path; failure paths compute it from summary).
func buildChunkedResponse(jobID, status string, items []CaptureBatchItem,
	summary *chunkSummary, edge *edgeFixupData,
) CaptureBatchResponse {
	total := summary.ProcessedItems + len(summary.Failures)
	if items != nil {
		total = len(items)
	}
	stats := CaptureBatchStats{
		TotalItems:      total,
		AddedCount:      len(summary.Added),
		FailedCount:     len(summary.Failures),
		SupersededCount: summary.SupersededCount,
		EdgesAdded:      len(edge.Added),
		EdgesFailed:     len(edge.Failed),
	}
	return CaptureBatchResponse{
		JobID:       jobID,
		Status:      status,
		Added:       summary.Added,
		Failed:      summary.Failures,
		Edges:       edge.Added,
		EdgesFailed: edge.Failed,
		Stats:       stats,
	}
}

// loadJobOrLog reads a Job for finalization. Returns nil and logs on
// failure; finalizers are best-effort and never panic the runner.
func loadJobOrLog(a *API, jobID, opName string) *jobs.Job {
	store := a.engine.JobStore()
	if store == nil {
		a.log.Warn("capture_batch chunked: jobstore unavailable",
			"job_id", jobID, "op", opName)
		return nil
	}
	j, err := store.Get(jobID)
	if err != nil {
		a.log.Warn("capture_batch chunked: get job failed",
			"job_id", jobID, "op", opName, "err", err)
		return nil
	}
	return j
}
