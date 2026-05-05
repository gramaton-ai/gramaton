package api

import (
	"context"
	"strings"
	"testing"
)

// makeMatchCollection seeds a curation=none collection with three
// items whose titles + details vary so substring tests can pick out
// specific subsets. curation=none avoids the standard-collection
// content_fields / refusal machinery; this test only exercises the
// match filter.
func makeMatchCollection(t *testing.T, a *API) (collID string) {
	t.Helper()
	ctx := context.Background()
	resp, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "MatchTest",
		Curation: "none",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
				{Name: "details", Type: FieldTypeString},
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
				{Name: "priority", Type: FieldTypeNumber},
			},
		},
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	for _, item := range []map[string]any{
		{"title": "Implement auth middleware", "details": "JWT-based session tokens", "status": "open", "priority": float64(1)},
		{"title": "Fix navigation bug", "details": "menu collapses on mobile", "status": "open", "priority": float64(2)},
		{"title": "Refresh AUTH tokens periodically", "details": "background goroutine", "status": "done", "priority": float64(3)},
		{"title": "Database migration script", "details": "schema v3 - mentions auth tangentially in comments", "status": "open", "priority": float64(4)},
	} {
		if _, apiErr := a.CollectionAdd(ctx, resp.ID, CollectionAddRequest{Fields: item}); apiErr != nil {
			t.Fatalf("add: %v", apiErr)
		}
	}
	return resp.ID
}

func TestCollectionItemsMatch_BasicSubstring(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, apiErr := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{
		Match: "auth",
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	// Three items mention "auth" (or "AUTH"): #1, #3, #4.
	if len(resp.Items) != 3 {
		t.Errorf("Match=auth returned %d items, want 3 (titles: %s)", len(resp.Items), titlesOf(resp.Items))
	}
}

func TestCollectionItemsMatch_CaseInsensitive(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	lower, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "auth"})
	upper, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "AUTH"})
	mixed, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "AuTh"})

	if len(lower.Items) != len(upper.Items) || len(lower.Items) != len(mixed.Items) {
		t.Errorf("case sensitivity leaked: lower=%d upper=%d mixed=%d",
			len(lower.Items), len(upper.Items), len(mixed.Items))
	}
}

func TestCollectionItemsMatch_ScansAllStringFields(t *testing.T) {
	// "navigation" appears only in a title; "tangentially" appears
	// only in a details field. Both should be reachable via Match.
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	titleHit, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "navigation"})
	if len(titleHit.Items) != 1 {
		t.Errorf("Match='navigation' returned %d, want 1 (title-only hit)", len(titleHit.Items))
	}

	detailsHit, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "tangentially"})
	if len(detailsHit.Items) != 1 {
		t.Errorf("Match='tangentially' returned %d, want 1 (details-only hit)", len(detailsHit.Items))
	}
}

func TestCollectionItemsMatch_ComposesWithFilter(t *testing.T) {
	// status=open AND match=auth should narrow further than either alone.
	// status=open: 3 items (#1, #2, #4). match=auth: 3 items (#1, #3, #4).
	// Intersection: #1, #4 => 2 items.
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, apiErr := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{
		Filter: map[string]any{"status": "open"},
		Match:  "auth",
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	if len(resp.Items) != 2 {
		t.Errorf("filter+match returned %d items, want 2 (titles: %s)", len(resp.Items), titlesOf(resp.Items))
	}
	for _, it := range resp.Items {
		if status, _ := it.Fields["status"].(string); status != "open" {
			t.Errorf("filter not applied: status=%q", status)
		}
		title, _ := it.Fields["title"].(string)
		details, _ := it.Fields["details"].(string)
		joined := strings.ToLower(title + " " + details)
		if !strings.Contains(joined, "auth") {
			t.Errorf("match not applied: title=%q details=%q", title, details)
		}
	}
}

func TestCollectionItemsMatch_SkipsNonStringFields(t *testing.T) {
	// Match="3" should NOT match the priority=3 item via the number
	// field. Matching only applies to string-typed fields. (The "3"
	// in "schema v3" of item #4's details DOES match — so we expect
	// 1 hit, the database-migration one.)
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "3"})
	for _, it := range resp.Items {
		title, _ := it.Fields["title"].(string)
		details, _ := it.Fields["details"].(string)
		// Hit must be reachable via a string field, not via the
		// number 3 in the priority column.
		joined := title + " " + details
		if !strings.Contains(joined, "3") {
			t.Errorf("non-string number field leaked into match: %+v", it.Fields)
		}
	}
}

func TestCollectionItemsMatch_EmptyMatchReturnsAll(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: ""})
	if len(resp.Items) != 4 {
		t.Errorf("empty Match returned %d items, want 4 (full collection)", len(resp.Items))
	}
}

func TestCollectionItemsMatch_NoHitsReturnsEmpty(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: "zzzzznope"})
	if len(resp.Items) != 0 {
		t.Errorf("no-hit Match returned %d items, want 0", len(resp.Items))
	}
}

func TestCollectionItemsMatch_TooLongRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	huge := strings.Repeat("x", MaxMatchLength+1)
	_, apiErr := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{Match: huge})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for over-long match string")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("error code = %q, want input_error", apiErr.Code)
	}
}

func TestCollectionItemsMatch_RespectsProjection(t *testing.T) {
	// Projection drops fields from the response, but Match should
	// still scan ALL string fields (not just projected ones). Setup:
	// project only "title" but match on "tangentially" (details).
	// Item #4 still surfaces; response has only title.
	a, _ := setupTestAPI(t)
	collID := makeMatchCollection(t, a)

	resp, _ := a.CollectionItems(context.Background(), collID, CollectionItemsRequest{
		Match:  "tangentially",
		Fields: []string{"title"},
	})
	if len(resp.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(resp.Items))
	}
	if _, has := resp.Items[0].Fields["details"]; has {
		t.Error("projection didn't drop 'details' from response")
	}
	if _, has := resp.Items[0].Fields["title"]; !has {
		t.Error("projection dropped 'title' (should have kept it)")
	}
}

// titlesOf is a small debug helper used in error messages above so
// test failures show which items came back, not just the count.
func titlesOf(items []CollectionItem) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		t, _ := it.Fields["title"].(string)
		out = append(out, t)
	}
	return strings.Join(out, " | ")
}
