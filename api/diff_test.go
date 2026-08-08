package api

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestDiffNotesPrunedFloorOnExactBoundary pins the inclusive floor
// check: a since date exactly equal to a pruned store's history floor
// must still surface the boundary note, not a silently empty diff.
// Before the fix the comparison was a strict Before, so since ==
// floor skipped the note -- the one case where the caller's window
// starts exactly where history was cut, not before it.
func TestDiffNotesPrunedFloorOnExactBoundary(t *testing.T) {
	a, eng := setupTestAPI(t)

	floor := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	eng.SetHistoryFloor(&graph.Tombstone{FloorDate: floor})

	resp, apiErr := a.Diff(context.Background(), DiffRequest{
		Since: floor.Format(time.RFC3339),
	})
	if apiErr != nil {
		t.Fatalf("diff: %v", apiErr)
	}
	if resp.Note == "" {
		t.Fatal("expected a prune-floor note when since equals the floor date exactly")
	}
}

// TestDiffNotesPrunedFloorWhenBeforeIt pins the pre-existing case the
// fix must not regress: a since date strictly before the floor also
// carries the note.
func TestDiffNotesPrunedFloorWhenBeforeIt(t *testing.T) {
	a, eng := setupTestAPI(t)

	floor := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	eng.SetHistoryFloor(&graph.Tombstone{FloorDate: floor})

	resp, apiErr := a.Diff(context.Background(), DiffRequest{
		Since: floor.Add(-time.Hour).Format(time.RFC3339),
	})
	if apiErr != nil {
		t.Fatalf("diff: %v", apiErr)
	}
	if resp.Note == "" {
		t.Fatal("expected a prune-floor note when since is before the floor date")
	}
}
