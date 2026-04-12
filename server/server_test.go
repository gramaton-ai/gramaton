package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// noopLLM is a minimal LLM provider for tests that don't need real responses.
type noopLLM struct{}

func (noopLLM) Complete(_ context.Context, _ string) (string, error)                { return "", nil }
func (noopLLM) CompleteWithModel(_ context.Context, _, _ string) (string, error) { return "", nil }
func (noopLLM) ModelID() string                                                    { return "test-noop" }

func setupTestServer(t *testing.T) (*Server, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}

	serverCfg := DefaultConfig()
	serverCfg.ConfigDir = dir
	logger := slog.Default()
	srv, err := New(eng, serverCfg, logger)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	return srv, eng
}

func addRecord(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

func doRequest(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		data, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(data)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	// Set RemoteAddr to loopback for loopback-restricted endpoints.
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func parseResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("parse response: %v (body: %s)", err, w.Body.String())
	}
	return result
}

// --- Status ---

func TestHandleStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/status", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	store := data["store"].(map[string]any)
	if store["nodes"].(float64) != 0 {
		t.Fatalf("expected 0 nodes, got %v", store["nodes"])
	}
	branch, ok := data["branch"].(string)
	if !ok || branch != "main" {
		t.Fatalf("expected branch 'main', got %v", data["branch"])
	}
}

// --- Records CRUD ---

func TestCreateRecord(t *testing.T) {
	srv, _ := setupTestServer(t)

	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":     "Test record content",
		"temporality": "durable",
		"confidence":  0.9,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["id"] == nil || data["id"] == "" {
		t.Fatal("expected record ID in response")
	}
}

func TestCreateRecordNoContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing content, got %d", w.Code)
	}
}

func TestGetRecord(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Get this record")

	w := doRequest(t, srv, "GET", "/v1/records/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	props := data["properties"].(map[string]any)
	if props["content_full"] != "Get this record" {
		t.Fatalf("expected content, got %v", props["content_full"])
	}
}

func TestGetRecordNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/records/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateRecord(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Update me")

	w := doRequest(t, srv, "PATCH", "/v1/records/"+id, map[string]any{
		"temporality": "temporal",
		"confidence":  0.5,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify update.
	w2 := doRequest(t, srv, "GET", "/v1/records/"+id, nil)
	resp := parseResponse(t, w2)
	data := resp["data"].(map[string]any)
	props := data["properties"].(map[string]any)
	if props["temporality"] != "temporal" {
		t.Fatalf("expected 'temporal', got %v", props["temporality"])
	}
}

func TestUpdateRecordValidUntil(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Expire me")

	w := doRequest(t, srv, "PATCH", "/v1/records/"+id, map[string]any{
		"valid_until": "2026-01-01",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteRecord(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Delete me")

	w := doRequest(t, srv, "DELETE", "/v1/records/"+id+"?reason=test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["deleted"] != true {
		t.Fatal("expected deleted: true")
	}
}

// --- Search ---

func TestSearch(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Search for this")

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"top": 10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchWithSort(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Record A")
	addRecord(t, eng, "Record B")

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"sort":  "created_at",
		"order": "desc",
		"top":   10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestSearchInvalidSort(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"sort": "invalid_field",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid sort, got %d", w.Code)
	}
}

func TestSearchFacets(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Record 1")
	addRecord(t, eng, "Record 2")

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{"top": 10})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["facets"] == nil {
		t.Fatal("expected facets in response")
	}
}

// --- Edges ---

func TestCreateEdge(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecord(t, eng, "Source")
	id2 := addRecord(t, eng, "Target")

	w := doRequest(t, srv, "POST", "/v1/records/"+id1+"/edges", map[string]any{
		"target_id":   id2,
		"edge_type":   "related_to",
		"edge_weight": 0.8,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Stats ---

func TestStats(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Stats record")

	w := doRequest(t, srv, "GET", "/v1/stats", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["total_records"].(float64) != 1 {
		t.Fatalf("expected 1 record, got %v", data["total_records"])
	}
}

// --- Pending ---

func TestPending(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add a pending record.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Pending record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "GET", "/v1/pending", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	records := data["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(records))
	}
}

// --- Curation status in envelope ---

func TestCurationInEnvelope(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/status", nil)
	resp := parseResponse(t, w)

	curation := resp["curation"].(map[string]any)
	if curation["pending_count"] == nil {
		t.Fatal("expected pending_count in curation")
	}
	if curation["overdue"] == nil {
		t.Fatal("expected overdue in curation")
	}
}

// --- Security headers ---

func TestSecurityHeaders(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/status", nil)

	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json, got %q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff header")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("expected no-store header")
	}
}

// --- Debug goroutines ---

func TestDebugGoroutines(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/debug/goroutines", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("goroutine")) {
		t.Fatal("expected goroutine dump in response")
	}
}

// --- Shutdown restricted to loopback ---

func TestShutdownNonLoopback(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/v1/shutdown", nil)
	req.RemoteAddr = "192.168.1.1:12345" // non-loopback
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback shutdown, got %d", w.Code)
	}
}

// --- Restore restricted to loopback ---

func TestRestoreNonLoopback(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/v1/restore", bytes.NewBufferString(`{"path":"x","force":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback restore, got %d", w.Code)
	}
}

// --- Input validation ---

func TestSearchTopCapped(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Test")

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"top": 999999,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Just verify it doesn't crash -- the cap is enforced internally.
}

func TestConfidenceRangeValidation(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"confidence_min": 2.0, // out of range
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range confidence, got %d", w.Code)
	}
}

// --- Response envelope ---

func TestResponseEnvelopeMeta(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/status", nil)
	resp := parseResponse(t, w)

	meta := resp["meta"].(map[string]any)
	if meta["version"] == nil || meta["version"] == "" {
		t.Fatal("expected version in meta")
	}
}
