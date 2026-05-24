package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
)

// Pin that capture-time supersession honors supersession=none
// opt-out on the older candidate. The dedupEmbedder (defined in
// save_batch_review_test.go) gives identical vectors for identical
// text, so seeding then re-capturing the same content reliably
// triggers CheckDedup and the auto-supersession code path.

// addToOptOutCollection links recordID into a collection whose
// effective supersession is "none". After this runs,
// EffectiveCurationFor(g, recordID).Supersession == "none".
func addToOptOutCollection(t *testing.T, a *API, recordID string) string {
	t.Helper()
	coll, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:         "no-supersede",
		Supersession: "none",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	a.engine.Lock()
	defer a.engine.Unlock()
	if _, err := a.engine.Graph().AddEdge(recordID, coll.ID, "member_of", 1.0, nil); err != nil {
		t.Fatalf("AddEdge member_of: %v", err)
	}
	if _, err := a.engine.Save("test seed: member_of opt-out collection"); err != nil {
		t.Fatalf("engine.Save: %v", err)
	}
	return coll.ID
}

// TestSaveHonorsSupersessionOptOut: a near-duplicate capture must
// not supersede a record whose effective supersession is "none".
// Pre-gate, api/save.go would mark the older record historical
// regardless of opt-out.
func TestSaveHonorsSupersessionOptOut(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	const text = "long enough content to bypass the short-jaccard skip and trigger a real supersession check"
	seed, apiErr := a.Save(context.Background(), SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed Save: %v", apiErr)
	}
	addToOptOutCollection(t, a, seed.ID)

	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("dup Save: %v", apiErr)
	}
	if len(resp.Superseded) != 0 {
		t.Errorf("opt-out record was superseded: %+v", resp.Superseded)
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seed.ID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); hist {
		t.Error("opt-out record has valid_until set; should be untouched")
	}
}

// TestSaveBatchHonorsSupersessionOptOut: same guarantee for the
// batch path (batchSupersedeIfDuplicate).
func TestSaveBatchHonorsSupersessionOptOut(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	const text = "batch path also has to honor supersession=none on the older candidate record"
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: mustItems(text)})
	if apiErr != nil {
		t.Fatalf("seed SaveBatch: %v", apiErr)
	}
	seedID := resp.Added[0].ID
	addToOptOutCollection(t, a, seedID)

	resp2, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: mustItems(text)})
	if apiErr != nil {
		t.Fatalf("dup SaveBatch: %v", apiErr)
	}
	if got := resp2.Stats.SupersededCount; got != 0 {
		t.Errorf("SupersededCount = %d, want 0 (opt-out record)", got)
	}
	if sup := resp2.Added[0].Superseded; len(sup) != 0 {
		t.Errorf("Added[0].Superseded = %+v, want none", sup)
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seedID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); hist {
		t.Error("opt-out record has valid_until set; should be untouched")
	}
}

// TestSessionSaveHonorsSupersessionOptOut: the session save path
// runs its own auto-supersession against the Memory record created
// from each segment. It must also honor opt-out on the older
// candidate. Setup mirrors TestSaveHonorsSupersessionOptOut but
// the trigger is a session segment whose content matches the seed.
func TestSessionSaveHonorsSupersessionOptOut(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	const text = "session save path must also honor supersession=none on the older candidate record"
	seed, apiErr := a.Save(ctx, SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed Save: %v", apiErr)
	}
	addToOptOutCollection(t, a, seed.ID)

	result, svcErr := a.SessionStart(ctx, "optout-session", "")
	if svcErr != nil {
		t.Fatalf("SessionStart: %v", svcErr)
	}
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("SessionPrepare: %v", err)
	}
	resp, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: text, TopicName: "dup topic"},
	})
	if err != nil {
		t.Fatalf("SessionSave: %v", err)
	}
	if len(resp.Superseded) != 0 {
		t.Errorf("opt-out record superseded by session segment: %+v", resp.Superseded)
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seed.ID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); hist {
		t.Error("opt-out record has valid_until set after session save")
	}
}

// TestSaveStillSupersedesMemoryOrphan is the positive regression:
// a memory orphan (supersession=store via MemoryOrphan defaults)
// continues to be superseded by a near-duplicate capture. Pins
// that the opt-out gate didn't accidentally block normal
// supersession.
func TestSaveStillSupersedesMemoryOrphan(t *testing.T) {
	emb := &dedupEmbedder{dim: 16}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	const text = "memory orphan capture that should still supersede on a deterministic duplicate"
	seed, apiErr := a.Save(context.Background(), SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("seed Save: %v", apiErr)
	}

	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: text})
	if apiErr != nil {
		t.Fatalf("dup Save: %v", apiErr)
	}
	if len(resp.Superseded) != 1 {
		t.Fatalf("expected 1 superseded, got %d", len(resp.Superseded))
	}
	if resp.Superseded[0].ID != seed.ID {
		t.Errorf("Superseded[0].ID = %s, want %s", resp.Superseded[0].ID, seed.ID)
	}

	eng.RLock()
	defer eng.RUnlock()
	old, _ := eng.Graph().GetNode(seed.ID)
	if _, hist := old.Properties.GetTimestamp("valid_until"); !hist {
		t.Error("memory orphan missing valid_until after supersession")
	}
}
