package api

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestHistoryRC4DeleteRecreateSameHashSurfacesBoth is the RC-4
// regression test. The bug: when a record with ID X was deleted and
// later recreated, the per-record history walker's `prevHash` stayed
// pinned to the pre-deletion hash across the not-found gap. If the
// recreation produced the same content hash as the original creation
// (possible when the record is re-added with identical properties),
// the walker compared the original creation's hash against that
// stale `prevHash` and suppressed it as "no change" -- silently
// dropping the creation event from history.
//
// Fix: when NodeHashInCommit returns found=false for a commit in the
// walk, reset `prevHash` to "" so a later reappearance registers as
// a first-appearance, not a comparison against stale state.
//
// Repro uses graph.AddNodeWithIDForTest to control the ID + props so
// the post-deletion recreation lands at exactly the same content hash
// as the original. No user-facing API reuses IDs today, so this
// scenario is unreachable through normal flows -- the test helper
// exists precisely to exercise this (and future branch-merge) path.
func TestHistoryRC4DeleteRecreateSameHashSurfacesBoth(t *testing.T) {
	a, eng := setupTestAPI(t)
	const id = "01TESTRECORDRC4REGRESSION0"

	// Fixed props so C1 and C3 produce the same content hash.
	newProps := func() graph.Properties {
		return graph.Properties{
			"content_full": graph.StringProperty("rc-4 target"),
			"created_at":   graph.TimestampProperty(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		}
	}

	// C1: create with the chosen ID.
	eng.Lock()
	eng.Graph().AddNodeWithIDForTest(id, newProps())
	if _, err := eng.Save("C1-create"); err != nil {
		eng.Unlock()
		t.Fatalf("C1 save: %v", err)
	}
	eng.Unlock()

	// C2: delete.
	eng.Lock()
	if err := eng.Graph().DeleteNode(id); err != nil {
		eng.Unlock()
		t.Fatalf("delete: %v", err)
	}
	if _, err := eng.Save("C2-delete"); err != nil {
		eng.Unlock()
		t.Fatalf("C2 save: %v", err)
	}
	eng.Unlock()

	// C3: recreate with IDENTICAL properties -> identical content hash.
	eng.Lock()
	eng.Graph().AddNodeWithIDForTest(id, newProps())
	if _, err := eng.Save("C3-recreate"); err != nil {
		eng.Unlock()
		t.Fatalf("C3 save: %v", err)
	}
	eng.Unlock()

	resp, apiErr := a.History(context.Background(), HistoryRequest{ID: id})
	if apiErr != nil {
		t.Fatalf("History: %v", apiErr)
	}

	// Both C1 (original creation) and C3 (recreation) must surface.
	// Before the fix, C1 was silently dropped because its hash
	// compared equal to the stale prevHash carried across the C2 gap.
	if len(resp.Changes) < 2 {
		t.Errorf("expected both C1 and C3 to surface in history, got %d entries:\n  %+v",
			len(resp.Changes), resp.Changes)
	}
	// Sanity: the entries should correspond to the two expected commit
	// messages. Walker is newest-first, so C3 first.
	actions := make([]string, 0, len(resp.Changes))
	for _, e := range resp.Changes {
		actions = append(actions, e.Action)
	}
	hasC1 := false
	hasC3 := false
	for _, a := range actions {
		if a == "C1-create" {
			hasC1 = true
		}
		if a == "C3-recreate" {
			hasC3 = true
		}
	}
	if !hasC1 || !hasC3 {
		t.Errorf("expected actions to include C1-create and C3-recreate, got: %v", actions)
	}
}

// TestHistorySinceUntilNarrowsWalk confirms the Since/Until fields
// (Phase 2) restrict the per-record walker to a date range and
// bypass MaxLogTraversal for date-bounded calls. The test seeds
// changes around a bounded window and asserts out-of-range commits
// are not scanned.
func TestHistorySinceUntilNarrowsWalk(t *testing.T) {
	a, eng := setupTestAPI(t)

	const id = "01TESTRECORDHISTORYRANGE01"

	// Seed a creation in the past, a modification in the window, and
	// a modification after the window. The seeded commits carry real
	// timestamps via engine.Save() (which uses time.Now().UTC());
	// we rely on the natural progression for ordering.
	eng.Lock()
	eng.Graph().AddNodeWithIDForTest(id, graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	_, _ = eng.Save("create")
	eng.Unlock()

	// Capture a timestamp before and after the middle change.
	beforeMid := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)

	eng.Lock()
	eng.Graph().SetNodeProperty(id, "content_full", graph.StringProperty("v2"))
	_, _ = eng.Save("mid-change")
	eng.Unlock()

	time.Sleep(10 * time.Millisecond)
	afterMid := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)

	eng.Lock()
	eng.Graph().SetNodeProperty(id, "content_full", graph.StringProperty("v3"))
	_, _ = eng.Save("late-change")
	eng.Unlock()

	// Narrow to only the middle window [beforeMid, afterMid]. The
	// late-change commit should not appear.
	resp, apiErr := a.History(context.Background(), HistoryRequest{
		ID:    id,
		Since: beforeMid.Format(time.RFC3339Nano),
		Until: afterMid.Format(time.RFC3339Nano),
	})
	if apiErr != nil {
		t.Fatalf("History range: %v", apiErr)
	}

	for _, e := range resp.Changes {
		if e.Action == "late-change" {
			t.Errorf("late-change leaked through the Since/Until window: %+v", resp.Changes)
		}
	}
	// At least the mid-change must show up.
	found := false
	for _, e := range resp.Changes {
		if e.Action == "mid-change" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("mid-change missing from windowed history: %+v", resp.Changes)
	}
}

// TestHistoryInvalidSince / TestHistoryInvalidUntil / TestHistorySinceAfterUntil
// exercise the validator path added in Phase 2.
func TestHistoryInvalidSince(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.History(context.Background(), HistoryRequest{ID: "x", Since: "nope"})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %+v", apiErr)
	}
}

func TestHistoryInvalidUntil(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.History(context.Background(), HistoryRequest{ID: "x", Until: "nope"})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %+v", apiErr)
	}
}

// TestD3ResolveEmitsAction confirms that api.Resolve writes a D3
// CommitAction on the resulting commit. This is the smoke-test-
// critical path: Phase 8's gramaton_log(actions=["resolve"]) filter
// only works if resolve-commits carry the structured action.
func TestD3ResolveEmitsAction(t *testing.T) {
	a, eng := setupTestAPI(t)
	id := addRecord(t, eng, "resolve target")

	resp, apiErr := a.Resolve(context.Background(), ResolveRequest{ID: id, Resolution: "completed"})
	if apiErr != nil {
		t.Fatalf("Resolve: %v", apiErr)
	}
	if !resp.Resolved {
		t.Fatal("Resolve should return Resolved=true")
	}

	// Load HEAD commit and inspect Actions.
	store := eng.Store()
	headHash := eng.HeadHash()
	commit, err := graph.LoadCommitMeta(store, headHash)
	if err != nil {
		t.Fatalf("LoadCommitMeta: %v", err)
	}
	if len(commit.Actions) != 1 {
		t.Fatalf("HEAD commit Actions len = %d, want 1 (resolve)", len(commit.Actions))
	}
	if commit.Actions[0].Kind != "resolve" {
		t.Errorf("Actions[0].Kind = %q, want resolve", commit.Actions[0].Kind)
	}
	if commit.Actions[0].RecordID != id {
		t.Errorf("Actions[0].RecordID = %q, want %q", commit.Actions[0].RecordID, id)
	}
}

func TestHistorySinceAfterUntilRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.History(context.Background(), HistoryRequest{
		ID:    "x",
		Since: "2026-04-25",
		Until: "2026-04-20",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for since > until, got %+v", apiErr)
	}
}

// TestHistoryVersionTimeline pins the timeline surface: logical
// versions newest-first with author, change_note, and the masked
// field diff; a deletion entry closes the history.
func TestHistoryVersionTimeline(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)
	ctx := context.Background()

	saved, apiErr := a.Save(ctx, SaveRequest{Content: "timeline subject v1"})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}
	conf := 0.9
	if _, apiErr := a.Update(ctx, UpdateRequest{
		ID:         saved.ID,
		Content:    "timeline subject v2 with a substantive revision",
		Confidence: &conf,
		ChangeNote: "vendor confirmed the revised numbers",
	}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	resp, apiErr := a.History(ctx, HistoryRequest{ID: saved.ID})
	if apiErr != nil {
		t.Fatalf("history: %v", apiErr)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("versions = %d (%+v), want 2", len(resp.Versions), resp.Versions)
	}
	latest := resp.Versions[0]
	if latest.ChangeNote != "vendor confirmed the revised numbers" {
		t.Fatalf("latest change_note = %q", latest.ChangeNote)
	}
	var hasContent, hasConfidence bool
	for _, f := range latest.FieldsChanged {
		if f == "content_full" {
			hasContent = true
		}
		if f == "confidence" {
			hasConfidence = true
		}
		if f == "embedding_model" || f == "last_accessed" {
			t.Fatalf("bookkeeping field %q leaked into the version diff", f)
		}
	}
	if !hasContent || !hasConfidence {
		t.Fatalf("fields_changed = %v, want content_full and confidence", latest.FieldsChanged)
	}
	if resp.VersionCoverage != "" {
		t.Fatalf("unexpected coverage caveat: %q", resp.VersionCoverage)
	}
}

// TestInspectAsOf pins the point-in-time read: the frozen reality at
// an earlier commit comes back with its semantics named, a commit
// off the current branch is refused, and a record that did not exist
// at T is a clean not-found.
func TestInspectAsOf(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	saved, apiErr := a.Save(ctx, SaveRequest{Content: "the original claim"})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}
	eng.RLock()
	v1commit := eng.HeadHash()
	eng.RUnlock()

	if _, apiErr := a.Update(ctx, UpdateRequest{ID: saved.ID, Content: "the corrected claim"}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	resp, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID, AsOf: v1commit})
	if apiErr != nil {
		t.Fatalf("inspect as_of: %v", apiErr)
	}
	if resp.Semantics != "point_in_time" || resp.AsOf != v1commit {
		t.Fatalf("semantics/as_of = %q/%q", resp.Semantics, resp.AsOf)
	}
	if got := resp.Properties["content_full"]; got != "the original claim" {
		t.Fatalf("historical content = %v, want the original", got)
	}

	// Live read still returns the correction.
	live, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID})
	if apiErr != nil {
		t.Fatalf("live inspect: %v", apiErr)
	}
	if got := live.Properties["content_full"]; got != "the corrected claim" {
		t.Fatalf("live content = %v", got)
	}

	// A commit hash that is not on the current branch is refused.
	if _, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID, AsOf: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}); apiErr == nil {
		t.Fatal("unknown/off-branch commit must be refused")
	}

	// A record that did not exist at T is a clean not-found.
	second, apiErr := a.Save(ctx, SaveRequest{Content: "a record born after the checkpoint"})
	if apiErr != nil {
		t.Fatalf("second save: %v", apiErr)
	}
	if _, apiErr := a.Inspect(ctx, InspectRequest{ID: second.ID, AsOf: v1commit}); apiErr == nil {
		t.Fatal("record absent at T must be not-found")
	}

	// Date-form as_of resolves through the timestamp index to the
	// same frozen reality.
	eng.RLock()
	v1meta, err := loadCommitMeta(eng.Store(), v1commit)
	eng.RUnlock()
	if err != nil {
		t.Fatalf("load v1 commit meta: %v", err)
	}
	byDate, apiErr := a.Inspect(ctx, InspectRequest{
		ID:   saved.ID,
		AsOf: v1meta.Timestamp.UTC().Format(time.RFC3339Nano),
	})
	if apiErr != nil {
		t.Fatalf("inspect as_of date: %v", apiErr)
	}
	if byDate.AsOf != v1commit {
		t.Fatalf("date resolved to %q, want the v1 commit %q", byDate.AsOf, v1commit)
	}
	if got := byDate.Properties["content_full"]; got != "the original claim" {
		t.Fatalf("date-form historical content = %v, want the original", got)
	}

	// A commit that EXISTS in the store but sits on an abandoned
	// lineage is refused by the ancestry gate (the unknown-hash case
	// above never reaches it). Build one: branch at the current head,
	// advance the main line, check the branch out -- the advance is
	// now off-branch.
	if _, apiErr := a.BranchCreate(ctx, BranchCreateRequest{Name: "asof-side"}); apiErr != nil {
		t.Fatalf("branch create: %v", apiErr)
	}
	if _, apiErr := a.Save(ctx, SaveRequest{Content: "the main line advances past the fork"}); apiErr != nil {
		t.Fatalf("post-fork save: %v", apiErr)
	}
	eng.RLock()
	offBranch := eng.HeadHash()
	eng.RUnlock()
	if _, apiErr := a.BranchCheckout(ctx, "asof-side"); apiErr != nil {
		t.Fatalf("checkout: %v", apiErr)
	}
	if _, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID, AsOf: offBranch}); apiErr == nil {
		t.Fatal("existing off-branch commit must be refused by the ancestry gate")
	}
	// Pre-fork history stays readable from the branch.
	preFork, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID, AsOf: v1commit})
	if apiErr != nil {
		t.Fatalf("pre-fork as_of on branch: %v", apiErr)
	}
	if got := preFork.Properties["content_full"]; got != "the original claim" {
		t.Fatalf("pre-fork content on branch = %v, want the original", got)
	}
}
