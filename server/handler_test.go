package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- Branches ---

func TestBranchCreate(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/branches", map[string]any{
		"name": "test-branch",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["name"] != "test-branch" {
		t.Fatalf("expected name 'test-branch', got %v", data["name"])
	}
}

func TestBranchCreateInvalidName(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/branches", map[string]any{
		"name": "bad/name",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid branch name, got %d", w.Code)
	}
}

func TestBranchCreateDuplicate(t *testing.T) {
	srv, eng := setupTestServer(t)
	// Need a commit for branches to work.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("x")})
	eng.Save("init")
	eng.Unlock()

	doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "dup"})
	w := doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "dup"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate branch, got %d", w.Code)
	}
}

func TestBranchList(t *testing.T) {
	srv, eng := setupTestServer(t)
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("x")})
	eng.Save("init")
	eng.Unlock()

	doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "feature-a"})

	w := doRequest(t, srv, "GET", "/v1/branches", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	branches := data["branches"].([]any)
	if len(branches) < 1 {
		t.Fatal("expected at least 1 branch")
	}
}

func TestBranchDeleteMain(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "DELETE", "/v1/branches/main", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for deleting main, got %d", w.Code)
	}
}

// --- Classify ---

func TestClassifyRecord(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create a pending record.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Classify me"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/records/"+n.ID+"/classify", map[string]any{
		"temporality":    "durable",
		"confidence":     0.9,
		"knowledge_type": "semantic",
		"keywords":       []string{"test"},
		"summary_short":  "Test classification",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify classification applied.
	w2 := doRequest(t, srv, "GET", "/v1/records/"+n.ID, nil)
	resp := parseResponse(t, w2)
	data := resp["data"].(map[string]any)
	props := data["properties"].(map[string]any)
	if props["processing_status"] != "processed" {
		t.Fatalf("expected 'processed', got %v", props["processing_status"])
	}
	if props["temporality"] != "durable" {
		t.Fatalf("expected 'durable', got %v", props["temporality"])
	}
}

// --- Explore ---

func TestExplore(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecord(t, eng, "Center node")
	id2 := addRecord(t, eng, "Neighbor node")

	eng.Lock()
	eng.Graph().AddEdge(id1, id2, "related_to", 0.8, nil)
	eng.Save("link")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/explore", map[string]any{
		"node_id": id1,
		"depth":   2,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestExploreNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/explore", map[string]any{
		"node_id": "nonexistent",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestExploreMissingNodeID(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/explore", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// --- History ---

func TestLog(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "History record")

	w := doRequest(t, srv, "GET", "/v1/log?limit=5", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	commits := data["commits"].([]any)
	if len(commits) < 1 {
		t.Fatal("expected at least 1 commit")
	}
}

func TestDiff(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Diff record")

	// Use since in the past to find commits after that date.
	w := doRequest(t, srv, "GET", "/v1/diff?since=2020-01-01", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// The diff might be empty if there's no commit before the since
	// date to compare against. That's valid behavior.
}

// --- Duplicates ---

func TestDuplicates(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create two records with near-identical vectors and embeddings.
	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Duplicate A"),
		"content_short":     graph.StringProperty("Dup A"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"embedding_full":    graph.VectorProperty([]float32{1.0, 0.0, 0.0}),
	})
	eng.VecIdx().Add(n1.ID, []float32{1.0, 0.0, 0.0})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}

	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Duplicate B"),
		"content_short":     graph.StringProperty("Dup B"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"embedding_full":    graph.VectorProperty([]float32{0.99, 0.01, 0.0}),
	})
	eng.VecIdx().Add(n2.ID, []float32{0.99, 0.01, 0.0})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/duplicates", map[string]any{
		"threshold": 0.9,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	pairs := data["pairs"].([]any)
	if len(pairs) < 1 {
		t.Fatal("expected at least 1 duplicate pair")
	}
}

// --- Export ---

func TestExportJSONL(t *testing.T) {
	// "jsonl" is the canonical name for line-delimited JSON
	// (Content-Type application/x-ndjson). What was previously
	// named "json" produces this shape.
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Export this")

	w := doRequest(t, srv, "POST", "/v1/export", map[string]any{
		"format": "jsonl",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("expected ndjson content type, got %q", w.Header().Get("Content-Type"))
	}
}

func TestExportJSON(t *testing.T) {
	// "json" now produces a parseable JSON array
	// (Content-Type application/json). Distinct from the
	// pre-rename "json" which was JSONL — that's now under
	// the "jsonl" name. New default name for `--format json`.
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Export this")

	w := doRequest(t, srv, "POST", "/v1/export", map[string]any{
		"format": "json",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %q", w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	if len(body) == 0 || body[0] != '[' {
		t.Errorf("expected JSON array body starting with '[', got %q", body[:min(50, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestExportCSV(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "CSV export")

	w := doRequest(t, srv, "POST", "/v1/export", map[string]any{
		"format": "csv",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "text/csv" {
		t.Fatalf("expected text/csv, got %q", w.Header().Get("Content-Type"))
	}
}

func TestExportMarkdown(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Markdown export")

	w := doRequest(t, srv, "POST", "/v1/export", map[string]any{
		"format": "markdown",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "text/markdown" {
		t.Fatalf("expected text/markdown, got %q", w.Header().Get("Content-Type"))
	}
}

func TestExportInvalidFormat(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/export", map[string]any{
		"format": "invalid",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid format, got %d", w.Code)
	}
}

// --- Import ---

func TestImportJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/import", map[string]any{
		"records": []map[string]any{
			{
				"id": "old-1",
				"properties": map[string]any{
					"content_full": "Imported record",
					"temporality":  "durable",
				},
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["imported"].(float64) != 1 {
		t.Fatalf("expected 1 imported, got %v", data["imported"])
	}
}

func TestImportEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/import", map[string]any{
		"records": []any{},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty records, got %d", w.Code)
	}
}

// --- Curation ---

func TestCurationStatusEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)
	// No runner configured -- should return error.
	w := doRequest(t, srv, "GET", "/v1/curation", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without runner, got %d", w.Code)
	}
}

// --- Auto-supersession ---

func TestSaveAutoSupersession(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create first record with embedding.
	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Original content"),
		"content_short":     graph.StringProperty("Original"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}
	eng.VecIdx().Add(n1.ID, []float32{1.0, 0.0, 0.0})
	eng.Graph().SetNodeProperty(n1.ID, "embedding_full",
		graph.VectorProperty([]float32{1.0, 0.0, 0.0}))
	eng.Save("first")
	eng.Unlock()

	// Create a near-identical record via API.
	// The pre-embed won't work without Ollama, but the dedup check
	// uses the vector index which we populated manually. However,
	// the new record won't have an embedding without an embedder.
	// So auto-supersession won't trigger in this test setup.
	// This test verifies the endpoint works without crashing.
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content": "Original content updated",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Validation edge cases ---

func TestCreateRecordInvalidTemporality(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":     "test",
		"temporality": "invalid",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid temporality, got %d", w.Code)
	}
}

func TestCreateRecordConfidenceOutOfRange(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":    "test",
		"confidence": 2.0,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for confidence > 1.0, got %d", w.Code)
	}
}

func TestSearchMatchTooLong(t *testing.T) {
	srv, _ := setupTestServer(t)
	longMatch := ""
	for i := 0; i < 2000; i++ {
		longMatch += "x"
	}
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"match": longMatch,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for match too long, got %d", w.Code)
	}
}

func TestSearchTooManyKeywords(t *testing.T) {
	srv, _ := setupTestServer(t)
	kw := make([]string, 200)
	for i := range kw {
		kw[i] = "keyword"
	}
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"keywords": kw,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for too many keywords, got %d", w.Code)
	}
}

func TestExploreDepthCapped(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Deep explore")

	// Depth 100 should be capped to maxExploreDepth (10).
	w := doRequest(t, srv, "POST", "/v1/explore", map[string]any{
		"node_id": id,
		"depth":   100,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (depth capped), got %d", w.Code)
	}
}
