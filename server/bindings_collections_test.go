package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

// makeCollection creates a schema-less collection for the batch tests
// and returns its id. Schema-aware coverage lives in the schema-specific
// test below.
func makeCollection(t *testing.T, srv *Server, name string) string {
	t.Helper()
	result, apiErr := srv.api.CollectionCreate(context.Background(), api.CollectionCreateRequest{Name: name})
	if apiErr != nil {
		t.Fatalf("create collection %q: %v", name, apiErr)
	}
	return result["id"].(string)
}

func TestCollectionAddBatchHappyPath(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "task one", "note": "alpha"}, ClientRef: "c1"},
			{Fields: map[string]any{"title": "task two", "note": "beta"}, ClientRef: "c2"},
			{Fields: map[string]any{"title": "task three"}, ClientRef: "c3"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("CollectionAddBatch: %v", apiErr)
	}
	if resp.CollectionID != collID {
		t.Errorf("collection_id = %q, want %q", resp.CollectionID, collID)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("Added = %d, want 3 (failed: %+v)", len(resp.Added), resp.Failed)
	}
	if len(resp.Failed) != 0 {
		t.Errorf("Failed = %d, want 0 (%+v)", len(resp.Failed), resp.Failed)
	}
	// Index and client_ref must round-trip.
	seen := make(map[string]string)
	for _, a := range resp.Added {
		seen[a.ClientRef] = a.ID
		if a.ID == "" {
			t.Errorf("added item index %d has empty ID", a.Index)
		}
	}
	for _, ref := range []string{"c1", "c2", "c3"} {
		if seen[ref] == "" {
			t.Errorf("client_ref %q missing in Added", ref)
		}
	}
}

func TestCollectionAddBatchEmptyItemsRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	_, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, api.CollectionAddBatchRequest{})
	if apiErr == nil {
		t.Fatal("expected ErrMissing for empty items")
	}
	if apiErr.Code != "missing_field" {
		t.Errorf("Code = %q, want missing_field", apiErr.Code)
	}
}

func TestCollectionAddBatchExceedsMaxRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	items := make([]api.CollectionAddItem, api.MaxCollectionBatchSize+1)
	for i := range items {
		items[i] = api.CollectionAddItem{Fields: map[string]any{"title": fmt.Sprintf("t%d", i)}}
	}
	_, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, api.CollectionAddBatchRequest{Items: items})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for oversize batch")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("Code = %q, want input_error", apiErr.Code)
	}
}

func TestCollectionAddBatchPerItemValidationFailure(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "valid one"}, ClientRef: "a"},
			{Fields: map[string]any{}, ClientRef: "empty"}, // fails: empty fields
			{Fields: map[string]any{"title": "valid two"}, ClientRef: "b"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("unexpected APIError: %v", apiErr)
	}
	if len(resp.Added) != 2 {
		t.Errorf("Added = %d, want 2", len(resp.Added))
	}
	if len(resp.Failed) != 1 {
		t.Fatalf("Failed = %d, want 1", len(resp.Failed))
	}
	f := resp.Failed[0]
	if f.Index != 1 {
		t.Errorf("failed index = %d, want 1", f.Index)
	}
	if f.ClientRef != "empty" {
		t.Errorf("failed client_ref = %q, want empty", f.ClientRef)
	}
	if f.Code != "input_error" {
		t.Errorf("failed code = %q, want input_error", f.Code)
	}
}

func TestCollectionAddBatchDedupAgainstExisting(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	// Seed one existing item.
	if _, apiErr := srv.api.CollectionAdd(context.Background(), collID, api.CollectionAddRequest{
		Fields: map[string]any{"title": "already here"},
	}); apiErr != nil {
		t.Fatalf("seed add: %v", apiErr)
	}

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "already here"}, ClientRef: "dup"},
			{Fields: map[string]any{"title": "ALREADY HERE"}, ClientRef: "dup-case"}, // case-insensitive match
			{Fields: map[string]any{"title": "fresh item"}, ClientRef: "ok"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("unexpected APIError: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Errorf("Added = %d, want 1", len(resp.Added))
	}
	if len(resp.Failed) != 2 {
		t.Fatalf("Failed = %d, want 2 (Added=%+v Failed=%+v)", len(resp.Failed), resp.Added, resp.Failed)
	}
	for _, f := range resp.Failed {
		if f.Code != "duplicate" {
			t.Errorf("expected duplicate code, got %q for client_ref %q", f.Code, f.ClientRef)
		}
	}
}

// TestCollectionAddBatchIntraBatchDedup verifies the first-write-wins
// policy: two items in the same batch with the same title means the
// first succeeds and subsequent siblings fail with "duplicate". This
// is the contract documented on CollectionAddBatch and needed for
// deterministic batch loading.
func TestCollectionAddBatchIntraBatchDedup(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "same"}, ClientRef: "first"},
			{Fields: map[string]any{"title": "same"}, ClientRef: "second"},
			{Fields: map[string]any{"title": "different"}, ClientRef: "third"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("unexpected APIError: %v", apiErr)
	}
	if len(resp.Added) != 2 {
		t.Fatalf("Added = %d, want 2", len(resp.Added))
	}
	if len(resp.Failed) != 1 {
		t.Fatalf("Failed = %d, want 1", len(resp.Failed))
	}
	// First and third should succeed; second should fail.
	if resp.Added[0].ClientRef != "first" {
		t.Errorf("Added[0].ClientRef = %q, want first", resp.Added[0].ClientRef)
	}
	if resp.Failed[0].ClientRef != "second" {
		t.Errorf("Failed[0].ClientRef = %q, want second", resp.Failed[0].ClientRef)
	}
	if resp.Failed[0].Code != "duplicate" {
		t.Errorf("Failed[0].Code = %q, want duplicate", resp.Failed[0].Code)
	}
}

// TestCollectionAddBatchMinimalCurationIdempotent verifies that on
// curation=minimal collections, duplicate titles land in Added with
// Deduplicated=true pointing at the existing item -- matching single-
// add's idempotent behavior. Also exercises the shared title
// normalization: " already " with leading/trailing whitespace and
// different case still collides with the seeded "already".
func TestCollectionAddBatchMinimalCurationIdempotent(t *testing.T) {
	srv, _ := setupTestServer(t)
	result, apiErr := srv.api.CollectionCreate(context.Background(), api.CollectionCreateRequest{
		Name:     "Shopping",
		Curation: "minimal",
	})
	if apiErr != nil {
		t.Fatalf("create minimal collection: %v", apiErr)
	}
	collID := result["id"].(string)

	// Seed an existing item.
	seed, apiErr := srv.api.CollectionAdd(context.Background(), collID, api.CollectionAddRequest{
		Fields: map[string]any{"title": "already"},
	})
	if apiErr != nil {
		t.Fatalf("seed: %v", apiErr)
	}
	seedID := seed["id"].(string)

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "already"}, ClientRef: "exact"},
			{Fields: map[string]any{"title": "  ALREADY  "}, ClientRef: "normalized"},
			{Fields: map[string]any{"title": "brand new"}, ClientRef: "new"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("CollectionAddBatch: %v", apiErr)
	}
	if len(resp.Failed) != 0 {
		t.Fatalf("Failed = %d, want 0 on minimal collection (got %+v)", len(resp.Failed), resp.Failed)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("Added = %d, want 3", len(resp.Added))
	}
	byRef := make(map[string]api.BatchAddSuccess, len(resp.Added))
	for _, a := range resp.Added {
		byRef[a.ClientRef] = a
	}
	if got := byRef["exact"]; !got.Deduplicated || got.ID != seedID {
		t.Errorf("exact dup: Deduplicated=%v ID=%q, want Deduplicated=true ID=%q", got.Deduplicated, got.ID, seedID)
	}
	if got := byRef["normalized"]; !got.Deduplicated || got.ID != seedID {
		t.Errorf("normalized dup: Deduplicated=%v ID=%q, want Deduplicated=true ID=%q", got.Deduplicated, got.ID, seedID)
	}
	if got := byRef["new"]; got.Deduplicated || got.ID == "" || got.ID == seedID {
		t.Errorf("new item: Deduplicated=%v ID=%q, want Deduplicated=false and a fresh non-seed ID", got.Deduplicated, got.ID)
	}
}

// TestCollectionAddBatchMinimalIntraBatchIdempotent covers the intra-
// batch first-write-wins path on a curation=minimal collection: two
// items in the same batch share a title, second lands as Added with
// Deduplicated=true pointing at the first's generated ID.
func TestCollectionAddBatchMinimalIntraBatchIdempotent(t *testing.T) {
	srv, _ := setupTestServer(t)
	result, apiErr := srv.api.CollectionCreate(context.Background(), api.CollectionCreateRequest{
		Name:     "Packing",
		Curation: "minimal",
	})
	if apiErr != nil {
		t.Fatalf("create minimal collection: %v", apiErr)
	}
	collID := result["id"].(string)

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "socks"}, ClientRef: "first"},
			{Fields: map[string]any{"title": "SOCKS"}, ClientRef: "second"},
			{Fields: map[string]any{"title": "toothbrush"}, ClientRef: "third"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("CollectionAddBatch: %v", apiErr)
	}
	if len(resp.Failed) != 0 {
		t.Fatalf("Failed = %d, want 0 (%+v)", len(resp.Failed), resp.Failed)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("Added = %d, want 3", len(resp.Added))
	}
	byRef := make(map[string]api.BatchAddSuccess, len(resp.Added))
	for _, a := range resp.Added {
		byRef[a.ClientRef] = a
	}
	first := byRef["first"]
	if first.Deduplicated {
		t.Errorf("first: Deduplicated=true, want false (it's the real insert)")
	}
	if got := byRef["second"]; !got.Deduplicated || got.ID != first.ID {
		t.Errorf("second: Deduplicated=%v ID=%q, want Deduplicated=true ID=%q", got.Deduplicated, got.ID, first.ID)
	}
	if got := byRef["third"]; got.Deduplicated {
		t.Errorf("third: Deduplicated=true, want false (distinct title)")
	}
}

// TestCollectionAddBatchTitleNormalization confirms batch-add matches
// single-add's title equivalence (trim + lowercase). Without the
// shared helper, whitespace-padded variants would slip through as
// new items rather than dup-failing.
func TestCollectionAddBatchTitleNormalization(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := makeCollection(t, srv, "Backlog")

	if _, apiErr := srv.api.CollectionAdd(context.Background(), collID, api.CollectionAddRequest{
		Fields: map[string]any{"title": "focus"},
	}); apiErr != nil {
		t.Fatalf("seed: %v", apiErr)
	}

	req := api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "  Focus  "}, ClientRef: "padded"},
		},
	}
	resp, apiErr := srv.api.CollectionAddBatch(context.Background(), collID, req)
	if apiErr != nil {
		t.Fatalf("CollectionAddBatch: %v", apiErr)
	}
	if len(resp.Added) != 0 {
		t.Errorf("Added = %d, want 0 (normalized dup should not create new item)", len(resp.Added))
	}
	if len(resp.Failed) != 1 || resp.Failed[0].Code != "duplicate" {
		t.Fatalf("Failed = %+v, want one duplicate entry", resp.Failed)
	}
}

func TestCollectionAddBatchCollectionNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, apiErr := srv.api.CollectionAddBatch(context.Background(), "nonexistent-id", api.CollectionAddBatchRequest{
		Items: []api.CollectionAddItem{
			{Fields: map[string]any{"title": "x"}},
		},
	})
	if apiErr == nil {
		t.Fatal("expected ErrNotFound")
	}
	if apiErr.Code != "not_found" {
		t.Errorf("Code = %q, want not_found", apiErr.Code)
	}
}
