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
