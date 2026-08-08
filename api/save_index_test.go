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
