package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
)

// Hold-semantics pins for the batch and session paths. The
// dedupEmbedder (defined in save_batch_review_test.go) gives
// identical vectors for identical text, so seeding then re-capturing
// the same content reliably triggers the save-guard scan.

// addToCollection links recordID into a collection, exercising the
// hold paths against collection-member candidates.
func addToCollection(t *testing.T, a *API, recordID string) string {
	t.Helper()
	coll, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name: "hold-target-collection",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	a.engine.Lock()
	defer a.engine.Unlock()
	if _, err := a.engine.Graph().AddEdge(recordID, coll.ID, "member_of", 1.0, nil); err != nil {
		t.Fatalf("AddEdge member_of: %v", err)
	}
	if _, err := a.engine.Save("test seed: member_of collection"); err != nil {
		t.Fatalf("engine.Save: %v", err)
	}
	return coll.ID
}

// TestSaveBatchHoldNeverMutatesCandidate: the batch hold replaces
// batch auto-supersession; whatever collection knobs the older
// record carries, a hold must leave it byte-identical (holds never
// mutate the matched record -- the supersession opt-out gate has
// nothing left to protect).
func TestSaveBatchHoldNeverMutatesCandidate(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	const text = "batch hold must leave the older candidate record untouched"
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: mustItems(text)})
	if apiErr != nil {
		t.Fatalf("seed SaveBatch: %v", apiErr)
	}
	seedID := resp.Added[0].ID
	addToCollection(t, a, seedID)

	resp2, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: mustItems(text)})
	if apiErr != nil {
		t.Fatalf("dup SaveBatch: %v", apiErr)
	}
	if len(resp2.Held) != 1 || resp2.Held[0].Held == nil || resp2.Held[0].Held.ID != seedID {
		t.Fatalf("expected the duplicate to be held against %s, got %+v", seedID, resp2.Held)
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seedID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); hist {
		t.Error("held-against record has valid_until set; holds must never mutate")
	}
	if res, _ := old.Properties.GetString("resolution"); res != "" {
		t.Errorf("held-against record has resolution %q; holds must never mutate", res)
	}
}

// TestSessionSaveHoldsPromotion: a segment closely matching an
// existing record has its Memory PROMOTION held -- the segment itself
// still lands in the append-only Sessions tier, the matched record is
// never mutated, and the hold is returned (and persisted on the
// segment for re-presentation at the next prepare).
func TestSessionSaveHoldsPromotion(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	const text = "session promotion hold must protect the older candidate record"
	seed, apiErr := a.Save(ctx, SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed Save: %v", apiErr)
	}

	result, svcErr := a.SessionStart(ctx, "hold-session", "")
	if svcErr != nil {
		t.Fatalf("SessionStart: %v", svcErr)
	}
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("SessionPrepare: %v", err)
	}
	resp, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: text, TopicName: "dup topic"},
	}, false)
	if err != nil {
		t.Fatalf("SessionSave: %v", err)
	}
	if resp.SegmentsAdded != 1 {
		t.Fatalf("segment must land in Sessions regardless of hold, got %d", resp.SegmentsAdded)
	}
	if resp.MemoryRecordsCreated != 0 {
		t.Fatalf("held promotion must not create a Memory record, got %d", resp.MemoryRecordsCreated)
	}
	if len(resp.Held) != 1 {
		t.Fatalf("expected 1 held promotion, got %+v", resp.Held)
	}
	h := resp.Held[0]
	if h.Held == nil || h.Held.ID != seed.ID {
		t.Fatalf("held against %+v, want %s", h.Held, seed.ID)
	}
	if h.SegmentID == "" {
		t.Fatal("held promotion must name its segment")
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seed.ID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); hist {
		t.Error("held-against record has valid_until set; holds must never mutate")
	}
	seg, _ := eng.Graph().GetNode(h.SegmentID)
	if seg == nil {
		t.Fatal("held segment node missing")
	}
	if held, _ := seg.Properties.GetBool("promotion_held"); !held {
		t.Error("segment missing persisted promotion_held state")
	}
	if target, _ := seg.Properties.GetString("promotion_hold_target"); target != seed.ID {
		t.Errorf("promotion_hold_target = %q, want %s", target, seed.ID)
	}
}

// TestSaveHoldAgainstHistoricalCandidate: a hold against a resolved
// record must say so -- Historical plus the recorded resolution --
// because the right exit differs (reviving a concluded record via
// update is a deliberate act, not a routine revision).
func TestSaveHoldAgainstHistoricalCandidate(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)
	ctx := context.Background()

	const text = "the concluded initiative this near-copy would resurrect"
	seed, apiErr := a.Save(ctx, SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	if _, apiErr := a.Resolve(ctx, ResolveRequest{ID: seed.ID, Resolution: "superseded"}); apiErr != nil {
		t.Fatalf("resolve: %v", apiErr)
	}

	resp, apiErr := a.Save(ctx, SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("duplicate save: %v", apiErr)
	}
	if resp.Held == nil || resp.Held.ID != seed.ID {
		t.Fatalf("expected a hold against %s, got %+v", seed.ID, resp.Held)
	}
	if !resp.Held.Historical {
		t.Error("hold against a resolved record must set historical")
	}
	if resp.Held.Resolution != "superseded" {
		t.Errorf("hold resolution = %q, want superseded", resp.Held.Resolution)
	}
}

// holdSessionFixture seeds a Memory record, then session-saves the
// same content so the promotion holds. Returns the seed record ID,
// the session ID, and the held segment ID.
func holdSessionFixture(t *testing.T, a *API, text string) (seedID, sessionID, segmentID string) {
	t.Helper()
	ctx := context.Background()
	seed, apiErr := a.Save(ctx, SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed Save: %v", apiErr)
	}
	result, svcErr := a.SessionStart(ctx, "resolve-held-session", "")
	if svcErr != nil {
		t.Fatalf("SessionStart: %v", svcErr)
	}
	sessionID = result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("SessionPrepare: %v", err)
	}
	resp, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: text, TopicName: "held topic"},
	}, false)
	if err != nil {
		t.Fatalf("SessionSave: %v", err)
	}
	if len(resp.Held) != 1 || resp.Held[0].SegmentID == "" {
		t.Fatalf("expected one held promotion with a segment id, got %+v", resp.Held)
	}
	return seed.ID, sessionID, resp.Held[0].SegmentID
}

// TestSessionResolveHeldAllowSimilar: resolving a held promotion with
// allow_similar promotes the segment now -- Memory record created and
// provenance-linked, hold state cleared so the next prepare stops
// re-presenting it (a second resolve of the same segment is invalid).
func TestSessionResolveHeldAllowSimilar(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	const text = "resolve-held promotes this genuinely distinct segment"
	_, sessionID, segID := holdSessionFixture(t, a, text)

	resp, apiErr := a.SessionResolveHeld(ctx, sessionID, []HeldResolution{
		{SegmentID: segID, Action: "allow_similar"},
	})
	if apiErr != nil {
		t.Fatalf("SessionResolveHeld: %v", apiErr)
	}
	if len(resp.Resolved) != 1 {
		t.Fatalf("expected 1 resolution, got %+v", resp.Resolved)
	}
	memID := resp.Resolved[0].MemoryRecordID
	if memID == "" {
		t.Fatal("allow_similar must report the promoted Memory record")
	}

	eng.RLock()
	mem, ok := eng.Graph().GetNode(memID)
	if !ok {
		t.Fatal("promoted Memory record missing")
	}
	if c, _ := mem.Properties.GetString("content_full"); c != text {
		t.Fatalf("promoted content_full = %q, want the segment content", c)
	}
	var linked bool
	for _, e := range eng.Graph().EdgesFrom(segID) {
		if e.Type == "extracted_as" && e.TargetID == memID {
			linked = true
		}
	}
	if !linked {
		t.Error("missing extracted_as edge from segment to promoted record")
	}
	seg, _ := eng.Graph().GetNode(segID)
	if captured, _ := seg.Properties.GetString("captured_as"); captured != memID {
		t.Errorf("captured_as = %q, want %s", captured, memID)
	}
	if held, _ := seg.Properties.GetBool("promotion_held"); held {
		t.Error("promotion_held must clear after resolution")
	}
	eng.RUnlock()

	if _, apiErr := a.SessionResolveHeld(ctx, sessionID, []HeldResolution{
		{SegmentID: segID, Action: "allow_similar"},
	}); apiErr == nil {
		t.Fatal("re-resolving a cleared hold must fail")
	}
}

// TestSessionResolveHeldPartialBatchChangesNothing pins the
// atomicity contract: a batch that cannot fully apply leaves the
// graph untouched. A duplicated segment used to pass upfront
// validation and fail phase-3 re-verification only on its second
// occurrence -- after the first had already promoted a record,
// wired provenance, and cleared the hold, all uncommitted.
func TestSessionResolveHeldPartialBatchChangesNothing(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	const text = "atomic resolve-held leaves nothing behind on failure"
	_, sessionID, segID := holdSessionFixture(t, a, text)
	baseline := eng.NodeCount()

	_, apiErr := a.SessionResolveHeld(ctx, sessionID, []HeldResolution{
		{SegmentID: segID, Action: "allow_similar"},
		{SegmentID: segID, Action: "allow_similar"},
	})
	if apiErr == nil {
		t.Fatal("a batch naming the same segment twice must fail")
	}
	if got := eng.NodeCount(); got != baseline {
		t.Fatalf("node count %d after failed batch, want unchanged %d", got, baseline)
	}
	eng.RLock()
	seg, _ := eng.Graph().GetNode(segID)
	if held, _ := seg.Properties.GetBool("promotion_held"); !held {
		t.Error("failed batch must leave the hold in place")
	}
	if captured, _ := seg.Properties.GetString("captured_as"); captured != "" {
		t.Errorf("failed batch stamped captured_as = %q", captured)
	}
	for _, e := range eng.Graph().EdgesFrom(segID) {
		if e.Type == "extracted_as" {
			t.Errorf("failed batch left a provenance edge to %s", e.TargetID)
		}
	}
	eng.RUnlock()

	resp, apiErr := a.SessionResolveHeld(ctx, sessionID, []HeldResolution{
		{SegmentID: segID, Action: "allow_similar"},
	})
	if apiErr != nil {
		t.Fatalf("resolve after failed batch: %v", apiErr)
	}
	if len(resp.Resolved) != 1 || resp.Resolved[0].MemoryRecordID == "" {
		t.Fatalf("expected a clean promotion after the failed batch, got %+v", resp.Resolved)
	}
}

// TestSessionResolveHeldUpdateTarget: update_target wires the
// segment's provenance to the existing record -- no new Memory record
// -- and defaults the target to the record the hold named.
func TestSessionResolveHeldUpdateTarget(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	const text = "resolve-held folds this segment into the existing record"
	seedID, sessionID, segID := holdSessionFixture(t, a, text)
	baseline := eng.NodeCount()

	resp, apiErr := a.SessionResolveHeld(ctx, sessionID, []HeldResolution{
		{SegmentID: segID, Action: "update_target"},
	})
	if apiErr != nil {
		t.Fatalf("SessionResolveHeld: %v", apiErr)
	}
	if len(resp.Resolved) != 1 || resp.Resolved[0].TargetID != seedID {
		t.Fatalf("expected resolution targeting %s, got %+v", seedID, resp.Resolved)
	}
	if resp.Resolved[0].MemoryRecordID != "" {
		t.Fatal("update_target must not create a Memory record")
	}
	if got := eng.NodeCount(); got != baseline {
		t.Fatalf("node count %d after update_target, want unchanged %d", got, baseline)
	}

	eng.RLock()
	defer eng.RUnlock()
	var linked bool
	for _, e := range eng.Graph().EdgesFrom(segID) {
		if e.Type == "extracted_as" && e.TargetID == seedID {
			linked = true
		}
	}
	if !linked {
		t.Error("missing extracted_as edge from segment to target record")
	}
	seg, _ := eng.Graph().GetNode(segID)
	if captured, _ := seg.Properties.GetString("captured_as"); captured != seedID {
		t.Errorf("captured_as = %q, want %s", captured, seedID)
	}
	if held, _ := seg.Properties.GetBool("promotion_held"); held {
		t.Error("promotion_held must clear after resolution")
	}
}
