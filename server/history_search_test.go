package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// reviseRecord mints a new logical version of a record's content,
// optionally with a change_note-carrying action.
func reviseRecord(t *testing.T, eng *core.Engine, id, content, note string) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	eng.SetContentProp(id, "content_full", content)
	action := graph.CommitAction{Kind: "update", RecordID: id}
	if note != "" {
		action.Note = note
	}
	if _, err := eng.Save("revise", action); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestHistorySearchIDScope pins the single-record rung of the cost
// ladder: revised-away content is found in the record's history,
// labeled a past version, with the live summary for contrast and an
// as_of-ready commit.
func TestHistorySearchIDScope(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "the storage layer uses bbolt for indexes")
	reviseRecord(t, eng, id, "the storage layer uses a custom mmap format for indexes", "")

	resp, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "bbolt", ID: id,
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if resp.Scope != "id" || resp.Semantics != "past_versions" {
		t.Fatalf("scope/semantics = %q/%q", resp.Scope, resp.Semantics)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %+v, want exactly the past version", resp.Hits)
	}
	hit := resp.Hits[0]
	if !strings.HasPrefix(hit.Version, "PAST VERSION from ") {
		t.Fatalf("version label = %q, want a loud past-version prefix", hit.Version)
	}
	if !strings.Contains(hit.Snippet, "bbolt") {
		t.Fatalf("snippet = %q, want the matched content", hit.Snippet)
	}
	if hit.Commit == "" || len(hit.Commit) != 64 {
		t.Fatalf("commit = %q, want a full as_of-ready hash", hit.Commit)
	}
	if hit.RecordSinceDeleted {
		t.Fatal("live record marked deleted")
	}

	// The commit is as_of-ready: inspecting at it returns the old
	// content.
	asOf, apiErr := srv.api.Inspect(context.Background(), api.InspectRequest{ID: id, AsOf: hit.Commit})
	if apiErr != nil {
		t.Fatalf("inspect as_of on hit commit: %v", apiErr)
	}
	if got := asOf.Properties["content_full"]; got != "the storage layer uses bbolt for indexes" {
		t.Fatalf("as_of content = %v", got)
	}

	// The current content matches as CURRENT VERSION, not a past one.
	cur, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "mmap", ID: id,
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch current: %v", apiErr)
	}
	if len(cur.Hits) != 1 || cur.Hits[0].Version != "CURRENT VERSION" {
		t.Fatalf("current-content hit = %+v, want CURRENT VERSION label", cur.Hits)
	}
}

// TestHistorySearchMatchesChangeNotes pins the change_note channel:
// a note explaining a revision is findable even though it never
// appears in any version's content.
func TestHistorySearchMatchesChangeNotes(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "retry limit is 3")
	reviseRecord(t, eng, id, "retry limit is 5", "raised after the outage postmortem")

	resp, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "outage postmortem", ID: id,
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %+v, want the note-matched version", resp.Hits)
	}
	if resp.Hits[0].ChangeNote != "raised after the outage postmortem" {
		t.Fatalf("change_note = %q", resp.Hits[0].ChangeNote)
	}
}

// TestHistorySearchStoreScopeFindsDeletedRecords pins the store
// scope's headline property: knowledge revised away entirely -- here,
// a deleted record -- is still findable, loudly marked as gone.
func TestHistorySearchStoreScopeFindsDeletedRecords(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "the abandoned parser design used recursive descent")
	err := eng.WithWriteBatch("delete", func(ws *core.WriteSession) (bool, error) {
		return true, ws.DeleteNode(id)
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	resp, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "recursive descent", Scope: "store",
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %+v, want the deleted record's version", resp.Hits)
	}
	hit := resp.Hits[0]
	if hit.RecordID != id || !hit.RecordSinceDeleted {
		t.Fatalf("hit = %+v, want record_since_deleted on the deleted record", hit)
	}
	if hit.CurrentSummary != "" {
		t.Fatalf("deleted record carries a current summary: %q", hit.CurrentSummary)
	}
	if !strings.HasPrefix(hit.Version, "PAST VERSION from ") {
		t.Fatalf("version label = %q (a deleted record has no current version)", hit.Version)
	}
}

// TestHistorySearchCandidatesScope pins the default rung: retrieval
// nominates the record from its live content, and the scan then
// surfaces past versions of it.
func TestHistorySearchCandidatesScope(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "the admission budget is 100 requests")
	reviseRecord(t, eng, id, "the admission budget is 250 requests", "")
	addRecord(t, eng, "an unrelated record about backups")

	resp, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "admission budget",
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if resp.Scope != "candidates" {
		t.Fatalf("scope = %q", resp.Scope)
	}
	var sawPast bool
	for _, h := range resp.Hits {
		if h.RecordID != id {
			t.Fatalf("hit on unexpected record: %+v", h)
		}
		if strings.HasPrefix(h.Version, "PAST VERSION") && strings.Contains(h.Snippet, "100") {
			sawPast = true
		}
	}
	if !sawPast {
		t.Fatalf("hits = %+v, want the past version with the old budget", resp.Hits)
	}
}

// TestHistorySearchBudgetTruncation pins honest coverage: a budget
// smaller than the scope reports truncation instead of implying a
// full scan.
func TestHistorySearchBudgetTruncation(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "version one of the truncation target")
	reviseRecord(t, eng, id, "version two of the truncation target", "")
	reviseRecord(t, eng, id, "version three of the truncation target", "")

	resp, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
		Text: "truncation target", Scope: "store", Budget: 1,
	})
	if apiErr != nil {
		t.Fatalf("HistorySearch: %v", apiErr)
	}
	if !resp.Truncated {
		t.Fatalf("response not marked truncated: %+v", resp)
	}
	if !strings.Contains(resp.Coverage, "truncated at budget") {
		t.Fatalf("coverage = %q, want the truncation caveat", resp.Coverage)
	}
	if !strings.Contains(resp.Coverage, "scanned 1 of 3") {
		t.Fatalf("coverage = %q, want scanned/total counts", resp.Coverage)
	}
}

// TestHistorySearchInputErrors pins the validation surface.
func TestHistorySearchInputErrors(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	if _, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{}); apiErr == nil || apiErr.Code != "missing_field" {
		t.Fatalf("empty text: %+v, want missing_field", apiErr)
	}
	if _, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "x", ID: "some-id", Scope: "store"}); apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("id+store scope: %+v, want input_error", apiErr)
	}
	if _, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "x", Scope: "everything"}); apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("bad scope: %+v, want input_error", apiErr)
	}
	if _, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "x", Since: "not-a-date"}); apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("bad since: %+v, want input_error", apiErr)
	}
	if _, apiErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "x", ID: "missing-record"}); apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("unknown id: %+v, want not_found", apiErr)
	}
}

// TestHistorySearchMatchPhaseHoldsNoLock proves the blob-matching
// phase runs without any engine lock. The snapshot hook is a two-way
// handshake: the search blocks between phase 1 and the match phase
// until this test has ACQUIRED the write lock, so the entire match
// phase deterministically runs while the write lock is held (a
// no-hit query skips the phase-3 live lookup). Any engine-lock
// acquisition in the match phase deadlocks into the watchdog.
func TestHistorySearchMatchPhaseHoldsNoLock(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "content for the lock discipline pin")
	reviseRecord(t, eng, id, "revised content for the lock discipline pin", "")

	hook := make(chan struct{})
	srv.api.SetHistorySearchSnapshotHook(hook)

	done := make(chan *api.APIError, 1)
	go func() {
		_, apiErr := srv.api.HistorySearch(context.Background(), api.HistorySearchRequest{
			Text: "no such phrase anywhere", Scope: "store",
		})
		done <- apiErr
	}()

	<-hook // phase 1 complete, search parked before matching
	eng.Lock()
	hook <- struct{}{} // release the match phase under our write lock
	select {
	case apiErr := <-done:
		if apiErr != nil {
			t.Fatalf("HistorySearch: %v", apiErr)
		}
	case <-time.After(5 * time.Second):
		eng.Unlock()
		t.Fatal("match phase stalled while the write lock was held -- it must not touch the engine lock")
	}
	eng.Unlock()
}
