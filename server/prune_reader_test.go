package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/prune"
)

// TestPrunedContentReadsAsPolicyNotCorruption pins the post-prune
// reader contract for content-depth pruning: the timeline marks
// swept versions content-pruned (metadata retained), and history
// search reports them in coverage instead of silently not matching.
func TestPrunedContentReadsAsPolicyNotCorruption(t *testing.T) {
	srv, eng := setupTestServer(t)
	ctx := context.Background()

	id := addRecord(t, eng, "the original phrasing with reactorterm")
	reviseRecord(t, eng, id, "the second phrasing with reactorterm", "")
	reviseRecord(t, eng, id, "the final phrasing entirely different", "")

	plan, err := prune.PlanKeepVersions(eng, 1, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := prune.ApplyKeepVersions(eng, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	hist, apiErr := srv.api.History(ctx, api.HistoryRequest{ID: id})
	if apiErr != nil {
		t.Fatalf("History: %v", apiErr)
	}
	if len(hist.Versions) != 3 {
		t.Fatalf("versions = %d, want all 3 as metadata", len(hist.Versions))
	}
	prunedCount := 0
	for _, v := range hist.Versions {
		if v.ContentPruned {
			prunedCount++
			if len(v.FieldsChanged) != 0 {
				t.Fatalf("content-pruned version carries a field diff: %+v", v)
			}
		}
	}
	if prunedCount != 2 {
		t.Fatalf("content-pruned versions = %d, want 2", prunedCount)
	}

	hs, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "reactorterm", ID: id})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if len(hs.Hits) != 0 {
		t.Fatalf("hits on swept content = %+v", hs.Hits)
	}
	if !strings.Contains(hs.Coverage, "content-pruned by retention") {
		t.Fatalf("coverage = %q, want the content-pruned count", hs.Coverage)
	}
}

// TestAsOfBelowFloorStatesPruned pins the chain-truncation contract:
// a point-in-time read below the floor names the floor and the
// policy, never a dangling internal error.
func TestAsOfBelowFloorStatesPruned(t *testing.T) {
	srv, eng := setupTestServer(t)
	ctx := context.Background()

	id := addRecord(t, eng, "rev 0")
	var preFloor time.Time
	for i := 1; i < 9; i++ {
		time.Sleep(2 * time.Millisecond)
		reviseRecord(t, eng, id, "rev "+string(rune('0'+i)), "")
		if i == 1 {
			preFloor = time.Now().UTC()
		}
	}

	plan, err := prune.PlanOlderThan(eng, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := prune.ApplyOlderThan(eng, plan, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// A date below the floor must state the policy.
	_, apiErr := srv.api.Inspect(ctx, api.InspectRequest{ID: id, AsOf: preFloor.Format(time.RFC3339Nano)})
	if apiErr == nil {
		t.Fatal("as_of below the floor succeeded")
	}
	if !strings.Contains(apiErr.Message, "store pruned") {
		t.Fatalf("as_of error = %q, want the pruned-floor statement", apiErr.Message)
	}

	// The live record is untouched.
	live, apiErr := srv.api.Inspect(ctx, api.InspectRequest{ID: id})
	if apiErr != nil {
		t.Fatalf("live inspect: %v", apiErr)
	}
	if got := live.Properties["content_full"]; got != "rev 8" {
		t.Fatalf("live content = %v", got)
	}
}
