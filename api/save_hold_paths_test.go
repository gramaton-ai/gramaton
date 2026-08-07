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
