package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

// TestRevertReloadsEdgesFromTargetCommit is the regression pin for
// the populated-edge-store revert corruption: graph.Load skips edge
// reload whenever the bbolt edge store has data, so an in-place
// revert used to bring back the target commit's NODES while keeping
// the current edge set -- an edge deleted after the target commit
// stayed deleted through the revert. The staged-load + AdoptGraph
// path must restore it.
func TestRevertReloadsEdgesFromTargetCommit(t *testing.T) {
	srv, eng := setupTestServer(t)

	srcID := addRecord(t, eng, "revert edge source")
	dstID := addRecord(t, eng, "revert edge destination")
	// A bystander edge that survives both commits keeps the bbolt
	// edge store non-empty at revert time. Without it the store is
	// empty after the deletion and even the buggy path reloads edges
	// (the populated-store shortcut never fires) -- the pin would
	// pass vacuously.
	otherA := addRecord(t, eng, "bystander edge source")
	otherB := addRecord(t, eng, "bystander edge destination")

	eng.Lock()
	if _, err := eng.Graph().AddEdge(otherA, otherB, "related_to", 0.5, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge bystander: %v", err)
	}
	edge, err := eng.Graph().AddEdge(srcID, dstID, "related_to", 0.9, nil)
	if err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge: %v", err)
	}
	withEdge, err := eng.Save("commit with edge")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save with edge: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	if err := eng.Graph().DeleteEdge(edge.ID); err != nil {
		eng.Unlock()
		t.Fatalf("DeleteEdge: %v", err)
	}
	if _, err := eng.Save("delete the edge"); err != nil {
		eng.Unlock()
		t.Fatalf("Save without edge: %v", err)
	}
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/revert", map[string]any{"hash": withEdge.Hash})
	if w.Code != http.StatusOK {
		t.Fatalf("revert: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	eng.RLock()
	defer eng.RUnlock()
	var restored bool
	for _, e := range eng.Graph().EdgesFrom(srcID) {
		if e.TargetID == dstID && e.Type == "related_to" {
			restored = true
		}
	}
	if !restored {
		t.Fatal("edge deleted after the target commit did not come back on revert; edges were not reloaded from the target commit")
	}
	// The bbolt store must mirror the reverted state too, or the next
	// restart diverges again.
	var inStore bool
	for _, e := range eng.EdgeStore().From(srcID) {
		if e.TargetID == dstID {
			inStore = true
		}
	}
	if !inStore {
		t.Fatal("restored edge missing from the persistent edge store after revert")
	}
}

// TestBranchCheckoutReloadsEdges pins the checkout half of the same
// bug: a branch created before an edge existed must not show that
// edge after checkout, and checking main back out must restore it.
func TestBranchCheckoutReloadsEdges(t *testing.T) {
	srv, eng := setupTestServer(t)
	ctx := context.Background()

	srcID := addRecord(t, eng, "branch edge source")
	dstID := addRecord(t, eng, "branch edge destination")

	if _, e := srv.api.BranchCreate(ctx, api.BranchCreateRequest{Name: "pre-edge"}); e != nil {
		t.Fatalf("BranchCreate: %v", e)
	}

	// Add the edge on main AFTER the branch point.
	eng.Lock()
	if _, err := eng.Graph().AddEdge(srcID, dstID, "related_to", 0.9, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge: %v", err)
	}
	if _, err := eng.Save("main gains an edge"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	if _, e := srv.api.BranchCheckout(ctx, "pre-edge"); e != nil {
		t.Fatalf("BranchCheckout pre-edge: %v", e)
	}
	eng.RLock()
	leaked := len(eng.Graph().EdgesFrom(srcID))
	eng.RUnlock()
	if leaked != 0 {
		t.Fatalf("branch created before the edge shows %d edges after checkout; the old edge set leaked through", leaked)
	}

	if _, e := srv.api.BranchCheckout(ctx, "main"); e != nil {
		t.Fatalf("BranchCheckout main: %v", e)
	}
	eng.RLock()
	restored := len(eng.Graph().EdgesFrom(srcID))
	eng.RUnlock()
	if restored != 1 {
		t.Fatalf("main's edge count after checkout back = %d, want 1", restored)
	}
}
