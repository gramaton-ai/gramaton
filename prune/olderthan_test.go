package prune

import (
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestOlderThanPlanAndApply pins chain truncation end to end:
// commits below the cut leave the CAS, the kept chain and live state
// survive, the derived indexes truncate, the floor is installed, and
// the baseline still grounds out a diff against the oldest kept
// commit.
func TestOlderThanPlanAndApply(t *testing.T) {
	eng, _ := newPruneTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("rev 0"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	for i := 1; i < 9; i++ {
		time.Sleep(2 * time.Millisecond) // distinct commit timestamps
		saveVersion(t, eng, n.ID, "rev "+string(rune('0'+i)))
	}

	// Collect the chain newest-first.
	var hashes []string
	var stamps []time.Time
	for cur := eng.HeadHash(); cur != ""; {
		c, err := graph.LoadCommitMeta(eng.Store(), cur)
		if err != nil {
			t.Fatalf("chain walk: %v", err)
		}
		hashes = append(hashes, cur)
		stamps = append(stamps, c.Timestamp)
		cur = c.Parent
	}
	if len(hashes) < 8 {
		t.Fatalf("fixture chain too short: %d", len(hashes))
	}

	// Horizon at the third-newest commit; the MinKeepCommits floor
	// extends the keep to five.
	horizon := stamps[2]
	plan, err := PlanOlderThan(eng, horizon, nil)
	if err != nil {
		t.Fatalf("PlanOlderThan: %v", err)
	}
	if plan.OldestKept != hashes[MinKeepCommits-1] {
		t.Fatalf("oldest kept = %s, want the floor commit %s", plan.OldestKept, hashes[MinKeepCommits-1])
	}
	wantTruncated := len(hashes) - MinKeepCommits
	if plan.TruncateCount != wantTruncated {
		t.Fatalf("truncate count = %d, want %d", plan.TruncateCount, wantTruncated)
	}

	res, err := ApplyOlderThan(eng, plan, nil)
	if err != nil {
		t.Fatalf("ApplyOlderThan: %v", err)
	}
	if res.SweepErrors != 0 || res.SweptBlobs == 0 {
		t.Fatalf("sweep result = %+v", res)
	}

	// Truncated commit chunks are gone; kept ones remain.
	if eng.Store().Has(hashes[len(hashes)-1]) {
		t.Fatal("root commit survived truncation")
	}
	if !eng.Store().Has(plan.OldestKept) {
		t.Fatal("oldest kept commit swept")
	}
	// Live state intact.
	eng.RLock()
	cur, ok := eng.Graph().GetNode(n.ID)
	eng.RUnlock()
	if !ok {
		t.Fatal("record lost")
	}
	if c, _ := cur.Properties.GetString("content_full"); c != "rev 8" {
		t.Fatalf("live content = %q", c)
	}
	// Floor installed with the truncation fields.
	floor := eng.HistoryFloor()
	if floor == nil || floor.OldestKeptCommit != plan.OldestKept {
		t.Fatalf("floor = %+v", floor)
	}
	if !floor.CoversRecordVersion(n.ID, stamps[len(stamps)-1]) {
		t.Fatal("pre-floor timestamp not covered")
	}
	// Derived indexes truncated: a date below the floor no longer
	// resolves, and the record keeps only post-floor versions.
	if _, found := eng.TSIndex().CommitAt(stamps[len(stamps)-1]); found {
		t.Fatal("timestamp index still resolves below the floor")
	}
	for _, e := range eng.Changelog().Versions(n.ID) {
		if e.Timestamp.Before(floor.FloorDate) {
			t.Fatalf("changelog entry below floor survived: %+v", e)
		}
	}
	// The baseline grounds out a diff against the oldest kept commit.
	if floor.Baseline == "" {
		t.Fatal("no baseline recorded")
	}
	base, err := graph.LoadCommitMeta(eng.Store(), floor.Baseline)
	if err != nil {
		t.Fatalf("baseline unreadable: %v", err)
	}
	kCommit, err := graph.LoadCommitMeta(eng.Store(), plan.OldestKept)
	if err != nil {
		t.Fatalf("oldest kept unreadable: %v", err)
	}
	if _, err := graph.DiffCommits(eng.Store(), base, kCommit); err != nil {
		t.Fatalf("diff against baseline: %v", err)
	}
}

// TestOlderThanRefusesBranchInTruncatedRegion pins the ref guard.
func TestOlderThanRefusesBranchInTruncatedRegion(t *testing.T) {
	eng, _ := newPruneTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v0"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	oldTip := eng.HeadHash()
	for i := 1; i < 9; i++ {
		time.Sleep(2 * time.Millisecond)
		saveVersion(t, eng, n.ID, "v"+string(rune('0'+i)))
	}

	if _, err := PlanOlderThan(eng, time.Now().UTC(), map[string]string{"stale": oldTip}); err == nil {
		t.Fatal("plan succeeded with a ref in the truncated region")
	}
}

// TestOlderThanRePrunable pins the one-shot-prune fix: after a
// truncation, the chain grounds out at the floor instead of dying on
// the swept parent chunk -- a second plan and the changelog backfill
// (the advertised repair tool) both still work.
func TestOlderThanRePrunable(t *testing.T) {
	eng, _ := newPruneTestEngine(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("r0"),
	})
	if _, err := eng.Save("create"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	for i := 1; i < 9; i++ {
		time.Sleep(2 * time.Millisecond)
		saveVersion(t, eng, n.ID, "r"+string(rune('0'+i)))
	}

	plan, err := PlanOlderThan(eng, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	if _, err := ApplyOlderThan(eng, plan, nil); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// A second plan must ground out at the floor, not error on the
	// swept parent. (With so few commits left, "nothing to truncate"
	// is the correct answer; a chunk-not-found error is the bug.)
	if _, err := PlanOlderThan(eng, time.Now().UTC(), nil); err != nil {
		if !strings.Contains(err.Error(), "nothing to truncate") {
			t.Fatalf("second plan died at the floor: %v", err)
		}
	}

	// The backfill grounds at the floor and diffs against the
	// baseline instead of walking into swept history.
	if err := eng.Changelog().SetMarker(""); err != nil {
		t.Fatalf("SetMarker: %v", err)
	}
	if _, err := eng.BackfillChangelog(nil); err != nil {
		t.Fatalf("backfill on pruned store: %v", err)
	}
	if got := len(eng.Changelog().Versions(n.ID)); got == 0 {
		t.Fatal("backfill re-derived nothing on the pruned store")
	}
}
