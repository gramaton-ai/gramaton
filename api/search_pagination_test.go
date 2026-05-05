package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// seedRecordsForPagination creates N captures with predictable
// content so tests can reason about which ones a query should hit.
// Capture creates Memory records (knowledge_type=semantic by
// default), which gramaton_search ranks by BM25/vector. Each
// record has a distinct content body containing the marker term so
// a Match query against the marker hits all of them.
func seedRecordsForPagination(t *testing.T, a *API, marker string, n int) {
	t.Helper()
	ctx := context.Background()
	conf := 0.9
	for i := 0; i < n; i++ {
		req := CaptureRequest{
			Content:         marker + " record body number " + strconv.Itoa(i) + " mentions the marker term",
			SummaryShort:    marker + " summary " + strconv.Itoa(i),
			Temporality:     "durable",
			Confidence:      &conf,
			KnowledgeType:   "semantic",
			EpistemicStatus: "well_established",
		}
		if _, apiErr := a.Capture(ctx, req); apiErr != nil {
			t.Fatalf("capture #%d: %v", i, apiErr)
		}
	}
}

func TestSearchPagination_FreshSearchEmitsPageTable(t *testing.T) {
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "zzzpaginatemarker", 25)

	resp, apiErr := a.Search(context.Background(), SearchRequest{
		Match:    "zzzpaginatemarker",
		PageSize: 10,
	})
	if apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	if resp.Total < 25 {
		t.Errorf("Total = %d, want >= 25", resp.Total)
	}
	if resp.PageSize != 10 {
		t.Errorf("PageSize = %d, want 10", resp.PageSize)
	}
	if resp.Page != 1 {
		t.Errorf("Page = %d, want 1", resp.Page)
	}
	if len(resp.Results) > 10 {
		t.Errorf("Results count = %d, want <= 10 (sliced to PageSize)", len(resp.Results))
	}
	if resp.QueryID == "" {
		t.Error("QueryID empty on fresh search")
	}
	if len(resp.Pages) < 3 {
		t.Errorf("Pages count = %d, want >= 3 for 25 records / 10 per page", len(resp.Pages))
	}
	if resp.NextCursor == "" {
		t.Error("NextCursor empty even though more results exist")
	}
}

func TestSearchPagination_CursorReturnsNextPage(t *testing.T) {
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "xxxcursortest", 25)

	first, _ := a.Search(context.Background(), SearchRequest{
		Match:    "xxxcursortest",
		PageSize: 10,
	})
	if first.NextCursor == "" {
		t.Fatal("first page had no NextCursor; can't test pagination")
	}
	collectIDs := func(res []any) string { return "" } // for debugging

	page1IDs := make(map[string]struct{}, len(first.Results))
	for _, r := range first.Results {
		page1IDs[r.ID] = struct{}{}
	}
	_ = collectIDs

	second, apiErr := a.Search(context.Background(), SearchRequest{
		Cursor: first.NextCursor,
	})
	if apiErr != nil {
		t.Fatalf("cursor Search: %v", apiErr)
	}
	if second.QueryID != first.QueryID {
		t.Errorf("cursor pagination changed QueryID: %q -> %q", first.QueryID, second.QueryID)
	}
	if second.Page != 2 {
		t.Errorf("Page = %d, want 2", second.Page)
	}
	if len(second.Results) == 0 {
		t.Error("page 2 returned no results")
	}
	for _, r := range second.Results {
		if _, dup := page1IDs[r.ID]; dup {
			t.Errorf("page 2 returned a record from page 1: %s", r.ID)
		}
	}
}

func TestSearchPagination_RandomPageAccessViaPageTable(t *testing.T) {
	// Page-table cursors should let an agent fetch any page
	// directly, not just paginate forward.
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "rrrrandomaccess", 30)

	first, _ := a.Search(context.Background(), SearchRequest{
		Match:    "rrrrandomaccess",
		PageSize: 10,
	})
	if len(first.Pages) < 3 {
		t.Fatalf("expected >= 3 pages in page table, got %d", len(first.Pages))
	}

	// Skip page 2; jump directly to page 3 via its cursor.
	page3Cursor := first.Pages[2].Cursor
	page3, apiErr := a.Search(context.Background(), SearchRequest{Cursor: page3Cursor})
	if apiErr != nil {
		t.Fatalf("random-access cursor Search: %v", apiErr)
	}
	if page3.Page != 3 {
		t.Errorf("Page = %d, want 3 (jumped via page table)", page3.Page)
	}
}

func TestSearchPagination_CursorIgnoresOtherArgsAndReportsThem(t *testing.T) {
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "iiiignoredtest", 12)

	first, _ := a.Search(context.Background(), SearchRequest{
		Match:    "iiiignoredtest",
		PageSize: 5,
	})
	// Cursor + filter args set together. Filters should be dropped
	// and named in IgnoredParams.
	resp, apiErr := a.Search(context.Background(), SearchRequest{
		Cursor:        first.NextCursor,
		Text:          "should be ignored",
		Match:         "should be ignored",
		Temporality:   "durable",
		KnowledgeType: "semantic",
	})
	if apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	got := strings.Join(resp.IgnoredParams, ",")
	for _, want := range []string{"text", "match", "temporality", "knowledge_type"} {
		if !strings.Contains(got, want) {
			t.Errorf("IgnoredParams missing %q; got %v", want, resp.IgnoredParams)
		}
	}
}

func TestSearchPagination_ExpiredSnapshotReturnsError(t *testing.T) {
	// The snapshot store has TTL >= 1s in production. We can't
	// easily simulate expiry in a unit test without time travel.
	// Instead, fabricate a cursor pointing at a non-existent
	// QueryID and verify we get the expired-snapshot error path.
	a, _ := setupTestAPI(t)
	bogusCursor := encodeCursor("01NEVERPUTINTOSTORE000000A", 0, 10)

	_, apiErr := a.Search(context.Background(), SearchRequest{Cursor: bogusCursor})
	if apiErr == nil {
		t.Fatal("expected error for cursor pointing at unknown snapshot")
	}
	if !strings.Contains(apiErr.Message, "snapshot_expired") && apiErr.Code != "unavailable" {
		t.Errorf("expected snapshot_expired-style error, got code=%q msg=%q", apiErr.Code, apiErr.Message)
	}
}

func TestSearchPagination_MalformedCursorRejected(t *testing.T) {
	a, _ := setupTestAPI(t)

	_, apiErr := a.Search(context.Background(), SearchRequest{Cursor: "not-a-valid-cursor!!!"})
	if apiErr == nil {
		t.Fatal("expected error for malformed cursor")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got code=%q", apiErr.Code)
	}
}

func TestSearchPagination_PageSizeRespected(t *testing.T) {
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "ppppagesizetest", 15)

	resp, _ := a.Search(context.Background(), SearchRequest{
		Match:    "ppppagesizetest",
		PageSize: 7,
	})
	if resp.PageSize != 7 {
		t.Errorf("PageSize = %d, want 7", resp.PageSize)
	}
	if len(resp.Results) > 7 {
		t.Errorf("Results count = %d, want <= 7", len(resp.Results))
	}
}

func TestSearchPagination_PageSizeAboveMaxClamped(t *testing.T) {
	// PageSizeMax defaults to 100. Requests above that are silently
	// clamped (matches MaxSearchTop pattern).
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "ccccclamptest", 5)

	resp, _ := a.Search(context.Background(), SearchRequest{
		Match:    "ccccclamptest",
		PageSize: 5000,
	})
	if resp.PageSize > 100 {
		t.Errorf("PageSize = %d, want <= 100 (PageSizeMax)", resp.PageSize)
	}
}

func TestSearchPagination_LegacyTopFallsBackToPageSize(t *testing.T) {
	// Callers using only the legacy Top field should still work --
	// pageSize falls back to Top when PageSize is unset.
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "lllllegacytop", 12)

	resp, _ := a.Search(context.Background(), SearchRequest{
		Match: "lllllegacytop",
		Top:   3,
	})
	if resp.PageSize != 3 {
		t.Errorf("PageSize = %d, want 3 (legacy Top fallback)", resp.PageSize)
	}
	if len(resp.Results) > 3 {
		t.Errorf("Results count = %d, want <= 3", len(resp.Results))
	}
}

func TestSearchPagination_LastPageHasNoNextCursor(t *testing.T) {
	a, _ := setupTestAPI(t)
	seedRecordsForPagination(t, a, "eeeendoftest", 11)

	first, _ := a.Search(context.Background(), SearchRequest{
		Match:    "eeeendoftest",
		PageSize: 5,
	})
	// Pages: [1-5], [6-10], [11-11]. Walk to last page via the
	// page table.
	if len(first.Pages) < 3 {
		t.Fatalf("expected 3 pages, got %d", len(first.Pages))
	}
	lastCursor := first.Pages[len(first.Pages)-1].Cursor

	last, _ := a.Search(context.Background(), SearchRequest{Cursor: lastCursor})
	if last.NextCursor != "" {
		t.Errorf("last page should have empty NextCursor, got %q", last.NextCursor)
	}
}
