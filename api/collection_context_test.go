package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/index"
)

// TestCollectionAddPrependsCollectionContext pins the contract:
// new collection items inherit their collection's name (and
// description, when set) into the BM25 index. Without this,
// items in a collection named "Gramaton development" don't
// surface for queries like "Gramaton" via BM25 unless the
// item's own fields happen to contain the word.
//
// Verifies via the BM25 index directly so the test is independent
// of embedder availability.
func TestCollectionAddPrependsCollectionContext(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	// Use deliberately uncommon multi-token name + description so
	// they can't accidentally match field text in unrelated tests.
	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:        "Zzzunique BookBucket",
		Description: "Lorem-ipsum reading domain",
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}

	// Item title deliberately does NOT contain any token from the
	// collection's name or description.
	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "the fox sat on the mat"},
	})
	if apiErr != nil {
		t.Fatalf("add: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()

	// BM25 query for a token from the collection's NAME surfaces
	// the item.
	hits := eng.BM25Full().Search(index.Tokenize("zzzunique"), 10, nil)
	if !hitsContain(hits, item.ID) {
		t.Errorf("BM25 hit for collection-name token did not include item %s; got hits: %v", item.ID, hits)
	}

	// BM25 query for a token from the collection's DESCRIPTION
	// also surfaces the item.
	hits = eng.BM25Full().Search(index.Tokenize("lorem"), 10, nil)
	if !hitsContain(hits, item.ID) {
		t.Errorf("BM25 hit for collection-description token did not include item %s; got hits: %v", item.ID, hits)
	}

	// Sanity: the item's own field is still searchable.
	hits = eng.BM25Full().Search(index.Tokenize("fox"), 10, nil)
	if !hitsContain(hits, item.ID) {
		t.Errorf("BM25 hit for item-field token (regression check) did not include item %s; got hits: %v", item.ID, hits)
	}
}

// TestCollectionAddBatchPrependsCollectionContext mirrors the
// single-add test for the batch path: every item in the batch
// inherits the collection's context.
func TestCollectionAddBatchPrependsCollectionContext(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name: "Qqquniq Project",
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}

	resp, apiErr := a.CollectionAddBatch(ctx, coll.ID, CollectionAddBatchRequest{
		Items: []CollectionAddItem{
			{Fields: map[string]any{"title": "alpha task"}, ClientRef: "a"},
			{Fields: map[string]any{"title": "beta task"}, ClientRef: "b"},
		},
	})
	if apiErr != nil {
		t.Fatalf("batch: %v", apiErr)
	}
	if len(resp.Added) != 2 {
		t.Fatalf("Added = %d, want 2", len(resp.Added))
	}
	added := make(map[string]struct{}, 2)
	for _, a := range resp.Added {
		added[a.ID] = struct{}{}
	}

	eng.RLock()
	defer eng.RUnlock()

	hits := eng.BM25Full().Search(index.Tokenize("qqquniq"), 10, nil)
	matched := 0
	for _, h := range hits {
		if _, ok := added[h.NodeID]; ok {
			matched++
		}
	}
	if matched != 2 {
		t.Errorf("BM25 hits for collection name surfaced %d batch items; want 2 (hits: %v)", matched, hits)
	}
}

// TestCollectionContextOmitsEmptyDescription confirms that a
// collection without a description prepends only the name (no
// trailing whitespace or empty token noise in the BM25 input).
func TestCollectionContextOmitsEmptyDescription(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name: "Wwworth NoDescColl",
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}

	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "any value"},
	})
	if apiErr != nil {
		t.Fatalf("add: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()

	hits := eng.BM25Full().Search(index.Tokenize("wwworth"), 10, nil)
	if !hitsContain(hits, item.ID) {
		t.Errorf("BM25 hit for collection-name token did not include item %s (no-description case); got %v", item.ID, hits)
	}
}

// hitsContain returns true if any of the BM25 search results matches
// the given node ID.
func hitsContain(hits []index.SearchResult, nodeID string) bool {
	for _, h := range hits {
		if h.NodeID == nodeID {
			return true
		}
	}
	return false
}
