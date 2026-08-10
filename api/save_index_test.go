package api

import (
	"context"
	"testing"
)

// TestSaveIndexesKeywordsForBM25: the save tool description promises
// "keywords are BM25 terms a future agent would type", and the index
// rebuild unions content, keywords, and meta -- so save-time indexing
// must include the keywords too, or the promise only becomes true
// after the first rebuild.
func TestSaveIndexesKeywordsForBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:  "a record about database tuning",
		Keywords: []string{"zephyrblue"},
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	hits := eng.BM25Full().Search([]string{"zephyrblue"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.ID {
		t.Fatalf("BM25 search on a save keyword = %+v, want the saved record %s", hits, resp.ID)
	}
}

// TestSaveBatchIndexesKeywordsForBM25: batch items follow the
// gramaton_save shape, keywords included -- a keyword saved through
// the batch path must be findable without waiting for an index
// rebuild, on both the sync path and the chunked pipeline.
func TestSaveBatchIndexesKeywordsForBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{{SaveRequest: SaveRequest{
			Content:  "batch record about cache tuning",
			Keywords: []string{"ambergold"},
		}}},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("added = %+v", resp.Added)
	}
	hits := eng.BM25Full().Search([]string{"ambergold"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.Added[0].ID {
		t.Fatalf("BM25 search on a batch keyword = %+v, want %s", hits, resp.Added[0].ID)
	}
}

// TestSaveIndexesSummaryForBM25: summary_short carries prospective
// search vocabulary (terms a future query would use that the prose
// may not); it must be lexically findable from the moment of save,
// not only after the first index rebuild.
func TestSaveIndexesSummaryForBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:      "we chose the embedded database for the storage layer",
		SummaryShort: "storage engine decision crimsonlake",
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	hits := eng.BM25Full().Search([]string{"crimsonlake"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.ID {
		t.Fatalf("BM25 search on a summary-only term = %+v, want the saved record %s", hits, resp.ID)
	}
}

// TestSaveBatchIndexesSummaryForBM25: the batch path shares the
// save-shape promise; a summary-only term must be findable without a
// rebuild.
func TestSaveBatchIndexesSummaryForBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{{SaveRequest: SaveRequest{
			Content:      "batch record about connection pooling",
			SummaryShort: "pooling decision umberfield",
		}}},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("added = %+v", resp.Added)
	}
	hits := eng.BM25Full().Search([]string{"umberfield"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.Added[0].ID {
		t.Fatalf("BM25 search on a batch summary term = %+v, want %s", hits, resp.Added[0].ID)
	}
}

// TestUpdateSummaryOnlyRefreshesBM25: a summary write alone changes
// the lexical document. The pre-fix update path re-indexed only on
// content changes, so vocabulary arriving in a summary-only update
// stayed unfindable until the next rebuild.
func TestUpdateSummaryOnlyRefreshesBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()
	resp, apiErr := a.Save(ctx, SaveRequest{Content: "original body prose"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	if _, apiErr := a.Update(ctx, UpdateRequest{
		ID:           resp.ID,
		SummaryShort: "revised anchor sepiavale",
	}); apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}

	hits := eng.BM25Full().Search([]string{"sepiavale"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.ID {
		t.Fatalf("BM25 search on the new summary term = %+v, want %s", hits, resp.ID)
	}
	// The content vocabulary must survive the refresh.
	hits = eng.BM25Full().Search([]string{"prose"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != resp.ID {
		t.Fatalf("content term lost after summary-only update: %+v", hits)
	}
}

// TestSessionSegmentIndexesSummaryForBM25: session-only segments are
// BM25-only by design, so their summary vocabulary has exactly one
// index to live in.
func TestSessionSegmentIndexesSummaryForBM25(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	result, svcErr := a.SessionStart(ctx, "summary-index-session", "")
	if svcErr != nil {
		t.Fatalf("SessionStart: %v", svcErr)
	}
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("SessionPrepare: %v", err)
	}

	noPromote := false
	const segContent = "we walked through the retrieval pipeline"
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{{
		Content:         segContent,
		TopicName:       "retrieval",
		SummaryShort:    "pipeline walkthrough ochreharbor",
		PromoteToMemory: &noPromote,
	}}, false); err != nil {
		t.Fatalf("SessionSave: %v", err)
	}

	hits := eng.BM25Full().Search([]string{"ochreharbor"}, 5, nil)
	if len(hits) != 1 {
		t.Fatalf("BM25 search on a segment summary term = %+v, want exactly the segment node", hits)
	}
	seg, ok := eng.Graph().GetNode(hits[0].NodeID)
	if !ok {
		t.Fatalf("hit node %s missing from graph", hits[0].NodeID)
	}
	if got, _ := seg.Properties.GetString("content"); got != segContent {
		t.Fatalf("hit is not the segment node: content = %q", got)
	}
}

// TestUpdateWithMetaReindexesSafely pins the meta-term loop on the
// update re-index path against every meta value type setMetaProps
// stores. The pre-fix loop called StringList() unchecked, so this
// test PANICKED (not merely failed) for any record carrying a
// scalar meta value.
func TestUpdateWithMetaReindexesSafely(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()
	resp, apiErr := a.Save(ctx, SaveRequest{
		Content: "record body prose",
		Meta: map[string]any{
			"assignee": "saffronvale",
			"sprint":   float64(23),
			"blocked":  true,
			"tags":     []any{"tanglewick"},
		},
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	if _, apiErr := a.Update(ctx, UpdateRequest{
		ID:           resp.ID,
		SummaryShort: "meta-safe anchor pewterglen",
	}); apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}

	// The new summary term is findable, and the meta vocabulary
	// survives the document refresh.
	for _, term := range []string{"pewterglen", "saffronvale", "tanglewick"} {
		hits := eng.BM25Full().Search([]string{term}, 5, nil)
		if len(hits) != 1 || hits[0].NodeID != resp.ID {
			t.Fatalf("term %q after meta-bearing update: hits = %+v, want %s", term, hits, resp.ID)
		}
	}
}

// TestClassifyReindexesSummaryAndKeywords: classify is a first-class
// summary/keyword-supplying path; its vocabulary must reach the
// lexical index at classify time, not at the next full rebuild.
func TestClassifyReindexesSummaryAndKeywords(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()
	resp, apiErr := a.Save(ctx, SaveRequest{Content: "an unclassified capture"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	if _, apiErr := a.Classify(ctx, ClassifyRequest{
		ID:           resp.ID,
		Temporality:  "durable",
		Keywords:     []string{"maroonhollow"},
		SummaryShort: "classification summary russetbay",
	}); apiErr != nil {
		t.Fatalf("Classify: %v", apiErr)
	}

	for _, term := range []string{"maroonhollow", "russetbay", "unclassified"} {
		hits := eng.BM25Full().Search([]string{term}, 5, nil)
		if len(hits) != 1 || hits[0].NodeID != resp.ID {
			t.Fatalf("term %q after classify: hits = %+v, want %s", term, hits, resp.ID)
		}
	}
}

// TestCollectionItemSummaryWriteKeepsCollectionContext: curation's
// summary write on a collection item re-derives the item's document
// WITH the owning collection's name/description context. The pre-fix
// refresh replaced the rich insert-time document with the narrow
// field-only text, so classified items stopped matching their
// collection's vocabulary.
func TestCollectionItemSummaryWriteKeepsCollectionContext(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:        "umberharbor tracker",
		Description: "widget backlog",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "fix the widget"},
	})
	if apiErr != nil {
		t.Fatalf("CollectionAdd: %v", apiErr)
	}

	hasHit := func(term, id string) bool {
		for _, h := range eng.BM25Full().Search([]string{term}, 10, nil) {
			if h.NodeID == id {
				return true
			}
		}
		return false
	}
	if !hasHit("umberharbor", item.ID) {
		t.Fatal("item should match its collection's name at insert time")
	}

	// Simulate the curation classification pass writing a summary.
	eng.Lock()
	eng.SetContentProp(item.ID, "content_short", "sprocket sepiaford alignment")
	eng.Unlock()

	if !hasHit("sepiaford", item.ID) {
		t.Fatal("item summary term should be findable after the summary write")
	}
	if !hasHit("umberharbor", item.ID) {
		t.Fatal("collection-name context lost from the item's document after a summary write")
	}
	if !hasHit("widget", item.ID) {
		t.Fatal("field vocabulary lost from the item's document after a summary write")
	}
}
