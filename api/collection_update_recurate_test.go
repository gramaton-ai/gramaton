package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// makeStandardItem creates a curation=standard collection with a
// schema declaring content_fields=[title, details] and inserts one
// item, returning (collection_id, item_id, after-add processing_status).
// The processing_status starts at "captured" because curation=standard
// items enter the autonomous pipeline at insert.
func makeStandardItem(t *testing.T, a *API) (string, string) {
	t.Helper()
	ctx := context.Background()
	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "RecurateTest",
		Curation: "standard",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
				{Name: "details", Type: FieldTypeString},
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
			},
			ContentFields: []string{"title", "details"},
		},
	})
	if apiErr != nil {
		t.Fatalf("create collection: %v", apiErr)
	}
	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "T-04 caching", "details": "redis vs postgres", "status": "open"},
	})
	if apiErr != nil {
		t.Fatalf("add: %v", apiErr)
	}
	return coll.ID, item.ID
}

// snapshotItem returns the relevant post-update fields for assertions.
func snapshotItem(t *testing.T, eng *core.Engine, itemID string) (status, model string, vec []float32) {
	t.Helper()
	n, ok := eng.Graph().GetNode(itemID)
	if !ok {
		t.Fatal("item not found")
	}
	status, _ = n.Properties.GetString("processing_status")
	model, _ = n.Properties.GetString("embedding_model")
	vec, _ = n.Properties.GetVector("embedding_full")
	return
}

// TestCollectionUpdateContentChangedRefreshesBM25Index: editing a
// field declared in content_fields re-indexes the item so the new
// text surfaces via BM25 search and the old text no longer does.
// Vector freshness is symmetric (covered when an embedder is wired)
// but BM25 fires unconditionally and is the test path here.
func TestCollectionUpdateContentChangedRefreshesBM25Index(t *testing.T) {
	a, eng := setupTestAPI(t)
	collID, itemID := makeStandardItem(t, a)

	// Pre-condition: BM25 contains the seed term, not the new one.
	eng.RLock()
	hasOld := len(eng.BM25Full().Search(index.Tokenize("redis"), 10, nil)) > 0
	hasNew := len(eng.BM25Full().Search(index.Tokenize("kafka"), 10, nil)) > 0
	eng.RUnlock()
	if !hasOld {
		t.Fatalf("setup precondition: seed 'redis' missing from BM25")
	}
	if hasNew {
		t.Fatalf("setup precondition: 'kafka' should not exist pre-update")
	}

	if _, apiErr := a.CollectionUpdate(context.Background(), collID, itemID, CollectionUpdateRequest{
		Fields: map[string]any{"details": "kafka was a dark horse contender"},
	}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	// After edit: 'kafka' should surface, 'redis' should be gone.
	eng.RLock()
	defer eng.RUnlock()
	hits := eng.BM25Full().Search(index.Tokenize("kafka"), 10, nil)
	found := false
	for _, h := range hits {
		if h.NodeID == itemID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BM25 missing 'kafka' after content_fields edit (re-index didn't fire); hits: %v", hits)
	}
	staleHits := eng.BM25Full().Search(index.Tokenize("redis"), 10, nil)
	for _, h := range staleHits {
		if h.NodeID == itemID {
			t.Errorf("BM25 still surfaces stale 'redis' after edit; re-index should have replaced the document")
		}
	}
}

// TestCollectionUpdateContentChangedFlipsProcessingStatus: editing a
// content_fields field flips processing_status back to captured so the
// next curation cycle reclassifies/resummarizes.
func TestCollectionUpdateContentChangedFlipsProcessingStatus(t *testing.T) {
	a, eng := setupTestAPI(t)
	collID, itemID := makeStandardItem(t, a)
	// Simulate the curation cycle having processed the item.
	eng.Lock()
	eng.SetProp(itemID, "processing_status", graph.StringProperty("processed"))
	eng.Unlock()

	if _, apiErr := a.CollectionUpdate(context.Background(), collID, itemID, CollectionUpdateRequest{
		Fields: map[string]any{"details": "expanded analysis"},
	}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	status, _, _ := snapshotItem(t, eng, itemID)
	if status != "captured" {
		t.Errorf("processing_status = %q, want captured (content_fields edit on curation=standard)", status)
	}
}

// TestCollectionUpdateNonContentFieldDoesNotRecurate: editing a
// field NOT in content_fields (e.g. an enum status) leaves indexes
// and processing_status untouched. Recurate fires only when the
// LLM-input text changes. Verified via BM25 search: status flips
// don't trigger re-indexing, so a search for the new enum value
// shouldn't surface the item (assuming the enum value isn't already
// in the indexed text).
func TestCollectionUpdateNonContentFieldDoesNotRecurate(t *testing.T) {
	a, eng := setupTestAPI(t)
	collID, itemID := makeStandardItem(t, a)

	// Pretend curation completed.
	eng.Lock()
	eng.SetProp(itemID, "processing_status", graph.StringProperty("processed"))
	eng.Unlock()

	if _, apiErr := a.CollectionUpdate(context.Background(), collID, itemID, CollectionUpdateRequest{
		Fields: map[string]any{"status": "done"},
	}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	// processing_status should NOT flip back to captured -- the LLM
	// input didn't change, so there's nothing new to classify.
	statusAfter, _, _ := snapshotItem(t, eng, itemID)
	if statusAfter != "processed" {
		t.Errorf("processing_status = %q, want processed (non-content_fields edit shouldn't flip)", statusAfter)
	}

	// BM25 should NOT have been re-indexed with the new value: the
	// item's BM25 input was set at insert time with status="open"
	// and a status-only update doesn't refresh it. A search for
	// "done" therefore should not surface this item via BM25.
	eng.RLock()
	defer eng.RUnlock()
	for _, h := range eng.BM25Full().Search(index.Tokenize("done"), 10, nil) {
		if h.NodeID == itemID {
			t.Error("BM25 surfaced item by post-update enum 'done'; non-content_fields edit shouldn't refresh the index")
		}
	}
}

// TestCollectionUpdateOnCurationNoneSkipsStatusFlip: curation=none
// collections never enter the LLM pipeline, so the processing_status
// flip is skipped even when content_fields output changes. Vector
// freshness still applies (similarity search benefits regardless).
func TestCollectionUpdateOnCurationNoneSkipsStatusFlip(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "NoneList",
		Curation: "none",
		// curation=none doesn't require content_fields.
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "milk"},
	})
	if apiErr != nil {
		t.Fatalf("add: %v", apiErr)
	}
	statusInit, _, _ := snapshotItem(t, eng, item.ID)
	if statusInit != "processed" {
		t.Fatalf("setup precondition: curation=none items should start as processed, got %q", statusInit)
	}

	if _, apiErr := a.CollectionUpdate(ctx, coll.ID, item.ID, CollectionUpdateRequest{
		Fields: map[string]any{"title": "oat milk"},
	}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	statusAfter, _, _ := snapshotItem(t, eng, item.ID)
	if statusAfter != "processed" {
		t.Errorf("processing_status = %q, want processed (curation=none should never flip)", statusAfter)
	}
}
