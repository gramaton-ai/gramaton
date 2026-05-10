package api

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// addStuckRecord adds a node directly to the engine with the property
// shape that marks it as stuck on the named curation task. Bypasses
// the api capture path so tests can construct stuck-record fixtures
// without running curation against a fake LLM.
func addStuckRecord(t *testing.T, eng *core.Engine, task, errMsg string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
		"access_count": graph.Int64Property(0),
	}
	switch task {
	case "classify":
		props["content_full"] = graph.StringProperty("test content for classify-stuck")
		props["processing_status"] = graph.StringProperty("stuck")
		props["classify_attempts"] = graph.Int64Property(3)
		props["last_classify_error"] = graph.StringProperty(errMsg)
	case "synthesis":
		props["node_type"] = graph.StringProperty("concept")
		props["concept_keyword"] = graph.StringProperty("auth")
		props["synthesis_status"] = graph.StringProperty("stuck")
		props["synthesis_attempts"] = graph.Int64Property(3)
		props["last_synthesis_error"] = graph.StringProperty(errMsg)
	default:
		t.Fatalf("addStuckRecord: unknown task %q", task)
	}

	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	if _, err := eng.Save("test: add stuck record", graph.CommitAction{Kind: graph.ActionCapture, RecordID: n.ID}); err != nil {
		t.Fatalf("save: %v", err)
	}
	return n.ID
}

// TestCurationListStuckEmpty: an engine with no stuck records returns
// an empty inventory (Records may be nil; Counts is empty map). Pins
// the no-op shape so callers can rely on the contract.
func TestCurationListStuckEmpty(t *testing.T) {
	a, _ := setupTestAPI(t)
	resp, apiErr := a.CurationListStuck(context.Background())
	if apiErr != nil {
		t.Fatalf("CurationListStuck: %v", apiErr)
	}
	if len(resp.Records) != 0 {
		t.Fatalf("expected zero records, got %d", len(resp.Records))
	}
	if len(resp.Counts) != 0 {
		t.Fatalf("expected zero counts, got %v", resp.Counts)
	}
}

// TestCurationListStuckMixed: with both classify-stuck and synthesis-
// stuck records present, the inventory groups them correctly and the
// counts map reflects the per-task split.
func TestCurationListStuckMixed(t *testing.T) {
	a, eng := setupTestAPI(t)

	classifyID := addStuckRecord(t, eng, "classify", "parse: bad json")
	synth1 := addStuckRecord(t, eng, "synthesis", "parse: response was not a valid JSON array")
	synth2 := addStuckRecord(t, eng, "synthesis", "llm timeout")

	resp, apiErr := a.CurationListStuck(context.Background())
	if apiErr != nil {
		t.Fatalf("CurationListStuck: %v", apiErr)
	}

	if len(resp.Records) != 3 {
		t.Fatalf("expected 3 stuck records, got %d", len(resp.Records))
	}
	if resp.Counts["classify"] != 1 {
		t.Fatalf("classify count: got %d want 1", resp.Counts["classify"])
	}
	if resp.Counts["synthesis"] != 2 {
		t.Fatalf("synthesis count: got %d want 2", resp.Counts["synthesis"])
	}

	// Verify each ID appears with the right task + error.
	gotByID := make(map[string]StuckRecord, len(resp.Records))
	for _, r := range resp.Records {
		gotByID[r.ID] = r
	}
	if rec, ok := gotByID[classifyID]; !ok {
		t.Fatalf("classify record %s missing from inventory", classifyID)
	} else if rec.Task != "classify" || rec.Error != "parse: bad json" {
		t.Fatalf("classify record shape wrong: %+v", rec)
	}
	if rec, ok := gotByID[synth1]; !ok || rec.Task != "synthesis" {
		t.Fatalf("synth1 record missing or wrong task: %+v", rec)
	}
	if rec, ok := gotByID[synth2]; !ok || rec.Task != "synthesis" {
		t.Fatalf("synth2 record missing or wrong task: %+v", rec)
	}
}

// TestCurationResetStuckAll: with no IDs in the request, every stuck
// record is reset. The status flips back to its task's reset value,
// the attempts counter is cleared, and the last-error property is
// removed entirely.
func TestCurationResetStuckAll(t *testing.T) {
	a, eng := setupTestAPI(t)

	classifyID := addStuckRecord(t, eng, "classify", "parse: bad json")
	synthID := addStuckRecord(t, eng, "synthesis", "llm timeout")

	resp, apiErr := a.CurationResetStuck(context.Background(), CurationResetStuckRequest{})
	if apiErr != nil {
		t.Fatalf("CurationResetStuck: %v", apiErr)
	}
	if resp.Reset != 2 {
		t.Fatalf("Reset count: got %d want 2", resp.Reset)
	}
	if resp.Counts["classify"] != 1 || resp.Counts["synthesis"] != 1 {
		t.Fatalf("Counts: %+v", resp.Counts)
	}

	// Verify both records' properties were actually reset.
	eng.RLock()
	defer eng.RUnlock()
	g := eng.Graph()

	classifyNode, ok := g.GetNode(classifyID)
	if !ok {
		t.Fatalf("classify node gone")
	}
	if status, _ := classifyNode.Properties.GetString("processing_status"); status != "captured" {
		t.Fatalf("classify status: got %q want captured", status)
	}
	if attempts, _ := classifyNode.Properties.GetInt64("classify_attempts"); attempts != 0 {
		t.Fatalf("classify attempts: got %d want 0", attempts)
	}
	if _, hasErr := classifyNode.Properties.GetString("last_classify_error"); hasErr {
		t.Fatalf("last_classify_error should be cleared")
	}

	synthNode, ok := g.GetNode(synthID)
	if !ok {
		t.Fatalf("synthesis node gone")
	}
	if status, _ := synthNode.Properties.GetString("synthesis_status"); status != "pending" {
		t.Fatalf("synthesis status: got %q want pending", status)
	}
	if attempts, _ := synthNode.Properties.GetInt64("synthesis_attempts"); attempts != 0 {
		t.Fatalf("synthesis attempts: got %d want 0", attempts)
	}
	if _, hasErr := synthNode.Properties.GetString("last_synthesis_error"); hasErr {
		t.Fatalf("last_synthesis_error should be cleared")
	}
}

// TestCurationResetStuckSelective: when explicit IDs are passed,
// only those records are reset. Records in the list that aren't
// stuck are silently skipped (caller may pass IDs from a prior list
// snapshot whose state has since changed).
func TestCurationResetStuckSelective(t *testing.T) {
	a, eng := setupTestAPI(t)

	keepID := addStuckRecord(t, eng, "classify", "should stay stuck")
	resetID := addStuckRecord(t, eng, "synthesis", "should be reset")

	resp, apiErr := a.CurationResetStuck(context.Background(), CurationResetStuckRequest{
		IDs: []string{resetID, "nonexistent-id-xyz"},
	})
	if apiErr != nil {
		t.Fatalf("CurationResetStuck: %v", apiErr)
	}
	if resp.Reset != 1 {
		t.Fatalf("Reset count: got %d want 1", resp.Reset)
	}
	if resp.Counts["synthesis"] != 1 {
		t.Fatalf("Counts: %+v", resp.Counts)
	}

	// keepID should still be stuck.
	eng.RLock()
	defer eng.RUnlock()
	g := eng.Graph()
	keepNode, ok := g.GetNode(keepID)
	if !ok {
		t.Fatalf("keep node gone")
	}
	if status, _ := keepNode.Properties.GetString("processing_status"); status != "stuck" {
		t.Fatalf("classify status changed unexpectedly: got %q want stuck", status)
	}

	// resetID should be reset.
	resetNode, ok := g.GetNode(resetID)
	if !ok {
		t.Fatalf("reset node gone")
	}
	if status, _ := resetNode.Properties.GetString("synthesis_status"); status != "pending" {
		t.Fatalf("synthesis status: got %q want pending", status)
	}
}

// TestCurationResetStuckRejectsOversizedIDList: an IDs slice past
// MaxResetStuckIDs returns ErrInvalid before any graph work happens.
// Pins the validation cap so a future refactor can't drop it
// silently.
func TestCurationResetStuckRejectsOversizedIDList(t *testing.T) {
	a, _ := setupTestAPI(t)

	tooMany := make([]string, MaxResetStuckIDs+1)
	for i := range tooMany {
		tooMany[i] = "fake-id"
	}
	_, apiErr := a.CurationResetStuck(context.Background(), CurationResetStuckRequest{IDs: tooMany})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for oversized ID list, got nil")
	}
	if apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %q", apiErr.Code)
	}
}

// TestCurationResetStuckEmptyStore: resetting with no stuck records
// is a no-op that doesn't error and doesn't emit a save (verified by
// returning Reset=0 + empty Counts).
func TestCurationResetStuckEmptyStore(t *testing.T) {
	a, _ := setupTestAPI(t)

	resp, apiErr := a.CurationResetStuck(context.Background(), CurationResetStuckRequest{})
	if apiErr != nil {
		t.Fatalf("CurationResetStuck: %v", apiErr)
	}
	if resp.Reset != 0 {
		t.Fatalf("Reset count: got %d want 0", resp.Reset)
	}
	if len(resp.Counts) != 0 {
		t.Fatalf("Counts should be empty: %+v", resp.Counts)
	}
}

// TestCurationResetStuckEmitsCommitAction: a successful reset must
// emit one ActionCurationStuckReset CommitAction per record so the
// operation surfaces in gramaton_log. Pins the audit-trail contract.
func TestCurationResetStuckEmitsCommitAction(t *testing.T) {
	a, eng := setupTestAPI(t)

	classifyID := addStuckRecord(t, eng, "classify", "err1")
	synthID := addStuckRecord(t, eng, "synthesis", "err2")

	_, apiErr := a.CurationResetStuck(context.Background(), CurationResetStuckRequest{})
	if apiErr != nil {
		t.Fatalf("CurationResetStuck: %v", apiErr)
	}

	commit, err := loadCommitMeta(eng.Store(), eng.HeadHashLocked())
	if err != nil {
		t.Fatalf("loadCommitMeta: %v", err)
	}

	var resetActions []graph.CommitAction
	for _, act := range commit.Actions {
		if act.Kind == graph.ActionCurationStuckReset {
			resetActions = append(resetActions, act)
		}
	}
	if len(resetActions) != 2 {
		t.Fatalf("expected 2 ActionCurationStuckReset actions in last commit, got %d", len(resetActions))
	}

	gotIDs := make([]string, 0, len(resetActions))
	for _, act := range resetActions {
		gotIDs = append(gotIDs, act.RecordID)
	}
	sort.Strings(gotIDs)
	wantIDs := []string{classifyID, synthID}
	sort.Strings(wantIDs)
	if gotIDs[0] != wantIDs[0] || gotIDs[1] != wantIDs[1] {
		t.Fatalf("commit action record IDs: got %v want %v", gotIDs, wantIDs)
	}
}
