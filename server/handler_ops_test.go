package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- Revert ---

func TestRevert(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create a record (first commit).
	addRecord(t, eng, "Before revert")
	firstHash := eng.HeadHash()

	// Create another record (second commit).
	addRecord(t, eng, "After revert")
	if eng.HeadHash() == firstHash {
		t.Fatal("second commit should have different hash")
	}

	// Revert to first commit.
	w := doRequest(t, srv, "POST", "/v1/revert", map[string]any{
		"hash": firstHash,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["reverted_to"] == nil {
		t.Fatal("expected reverted_to in response")
	}
}

func TestRevertInvalidHash(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/revert", map[string]any{
		"hash": "nonexistent",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for invalid hash, got %d", w.Code)
	}
}

// --- Reembed ---

func TestReembed(t *testing.T) {
	srv, _ := setupTestServer(t)
	// Without an embedder configured, should return error.
	w := doRequest(t, srv, "POST", "/v1/reembed", map[string]any{})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without embedder, got %d", w.Code)
	}
}

// --- Ingest (file upload) ---

func TestIngestFiles(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/ingest", map[string]any{
		"files": []map[string]any{
			{"filename": "test.md", "content": "# Test file content"},
			{"filename": "notes.txt", "content": "Some notes here"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["ingested"].(float64) != 2 {
		t.Fatalf("expected 2 ingested, got %v", data["ingested"])
	}
}

func TestIngestEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/ingest", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing path/files, got %d", w.Code)
	}
}

func TestIngestEmptyContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/ingest", map[string]any{
		"files": []map[string]any{
			{"filename": "empty.txt", "content": ""},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	// Empty files get warnings but don't fail the request.
	if data["ingested"].(float64) != 0 {
		t.Fatalf("expected 0 ingested for empty content, got %v", data["ingested"])
	}
}

func TestIngestSetsCreatedAt(t *testing.T) {
	srv, eng := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/ingest", map[string]any{
		"files": []map[string]any{
			{"filename": "test.md", "content": "Test content for timestamp check"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Find the ingested node and verify created_at is set.
	eng.RLock()
	defer eng.RUnlock()
	found := false
	it := eng.Graph().NodeIterator()
	for it.Next() {
		n := it.Node()
		if c, ok := n.Properties.GetString("content_full"); ok && c == "Test content for timestamp check" {
			if _, ok := n.Properties.GetTimestamp("created_at"); !ok {
				t.Fatal("ingested record missing created_at")
			}
			found = true
			break
		}
	}
	it.Close()
	if !found {
		t.Fatal("ingested record not found")
	}
}

// --- Record history ---

func TestRecordHistory(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "History target")

	// Update to create another commit.
	doRequest(t, srv, "PATCH", "/v1/records/"+id, map[string]any{
		"temporality": "temporal",
	})

	w := doRequest(t, srv, "GET", "/v1/records/"+id+"/history", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["id"] != id {
		t.Fatalf("expected record ID %s, got %v", id, data["id"])
	}
	changes := data["changes"].([]any)
	if len(changes) < 1 {
		t.Fatal("expected at least 1 change in history")
	}
}

// --- Backup endpoint ---

func TestBackupEndpoint(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Backup this")

	// Override backup dir to temp dir to avoid polluting ~/.gramaton/backups.
	cfg := eng.Config()
	backupDir := t.TempDir()
	cfg.Backup.Dir = backupDir
	// Note: we can't easily change the config after engine creation.
	// The handler reads cfg.Backup.Dir which defaults to "".
	// For this test, just verify the endpoint works. The backup goes
	// to the default dir but that's okay for testing.
	w := doRequest(t, srv, "POST", "/v1/backup", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["path"] == nil || data["path"] == "" {
		t.Fatal("expected backup path in response")
	}
	if data["size_bytes"].(float64) <= 0 {
		t.Fatal("expected positive file size")
	}
}

// --- Restore endpoint ---

func TestRestoreRequiresForce(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/restore", map[string]any{
		"path": "/tmp/fake.tar.gz",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without force flag, got %d", w.Code)
	}
}

func TestRestoreMissingPath(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/restore", map[string]any{
		"force": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing path, got %d", w.Code)
	}
}

// --- Branch checkout/merge/discard ---

func TestBranchCheckout(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Need a commit first.
	addRecord(t, eng, "Initial")

	// Create and checkout.
	doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "feature"})
	w := doRequest(t, srv, "POST", "/v1/branches/feature/checkout", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["checked_out"] != true {
		t.Fatal("expected checked_out: true")
	}
}

func TestBranchCheckoutNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/branches/nonexistent/checkout", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestBranchMerge(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Initial")

	// Create branch and add a record on it.
	doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "to-merge"})
	doRequest(t, srv, "POST", "/v1/branches/to-merge/checkout", nil)
	addRecord(t, eng, "Branch record")

	// Switch back to main and merge.
	doRequest(t, srv, "POST", "/v1/branches/main/checkout", nil)
	w := doRequest(t, srv, "POST", "/v1/branches/to-merge/merge", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["merged"] != "to-merge" {
		t.Fatalf("expected merged: to-merge, got %v", data["merged"])
	}
}

func TestBranchMergeMain(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/branches/main/merge", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for merging main into itself, got %d", w.Code)
	}
}

func TestBranchDiscard(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Initial")

	doRequest(t, srv, "POST", "/v1/branches", map[string]any{"name": "throwaway"})
	w := doRequest(t, srv, "DELETE", "/v1/branches/throwaway", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["discarded"] != "throwaway" {
		t.Fatalf("expected discarded: throwaway, got %v", data["discarded"])
	}
}

func TestBranchDiscardNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "DELETE", "/v1/branches/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// --- Curation trigger ---

func TestCurationTriggerWithoutRunner(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/curation/trigger", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without runner, got %d", w.Code)
	}
}

func TestCurationTriggerRejectsUnauthenticatedRemoteOps(t *testing.T) {
	srv, _ := setupTestServer(t)
	// Curation is pathless tier-1 admin (open to authenticated
	// remotes); an unauthenticated non-loopback caller is stopped by
	// the auth middleware with 401.
	req := newNonLoopbackRequest(t, "POST", "/v1/curation/trigger", nil)
	w := serveRequest(t, srv, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated non-loopback trigger, got %d", w.Code)
	}
}

// --- Edge validation ---

func TestCreateEdgeMissingTarget(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Source")

	w := doRequest(t, srv, "POST", "/v1/records/"+id+"/edges", map[string]any{
		"edge_type": "related_to",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing target_id, got %d", w.Code)
	}
}

func TestCreateEdgeMissingType(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecord(t, eng, "Source")
	id2 := addRecord(t, eng, "Target")

	w := doRequest(t, srv, "POST", "/v1/records/"+id1+"/edges", map[string]any{
		"target_id": id2,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing edge_type, got %d", w.Code)
	}
}

func TestCreateEdgeInvalidWeight(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecord(t, eng, "Source")
	id2 := addRecord(t, eng, "Target")

	w := doRequest(t, srv, "POST", "/v1/records/"+id1+"/edges", map[string]any{
		"target_id":   id2,
		"edge_type":   "related_to",
		"edge_weight": 2.0,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for weight > 1.0, got %d", w.Code)
	}
}

func TestCreateEdgeTargetNotFound(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Source")

	w := doRequest(t, srv, "POST", "/v1/records/"+id+"/edges", map[string]any{
		"target_id": "nonexistent",
		"edge_type": "related_to",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing target, got %d", w.Code)
	}
}

// --- Update validation ---

func TestUpdateInvalidTemporality(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Test")

	w := doRequest(t, srv, "PATCH", "/v1/records/"+id, map[string]any{
		"temporality": "invalid",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestUpdateNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "PATCH", "/v1/records/nonexistent", map[string]any{
		"temporality": "durable",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateKeywordsAndSummary(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "Test")

	w := doRequest(t, srv, "PATCH", "/v1/records/"+id, map[string]any{
		"keywords":      []string{"new", "tags"},
		"summary_short": "New summary",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify.
	w2 := doRequest(t, srv, "GET", "/v1/records/"+id, nil)
	resp := parseResponse(t, w2)
	data := resp["data"].(map[string]any)
	props := data["properties"].(map[string]any)
	if props["content_short"] != "New summary" {
		t.Fatalf("expected 'New summary', got %v", props["content_short"])
	}
}

// --- Search edge cases ---

func TestSearchWithNegation(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add durable and temporal records.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Durable record"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Temporal record"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("temporal"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		for k, v := range n.Properties {
			eng.PropIdx().Add(id, k, v)
		}
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"temporality": "!durable",
		"top":         10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	for _, r := range results {
		rec := r.(map[string]any)
		if rec["temporality"] == "durable" {
			t.Fatal("negation should exclude durable records")
		}
	}
}

func TestSearchMissing(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add one classified, one unclassified.
	addRecord(t, eng, "Classified") // has temporality from addRecord

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Unclassified"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"missing": []string{"temporality"},
		"top":     10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result missing temporality, got %d", len(results))
	}
}

func TestSearchRandom(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Random 1")
	addRecord(t, eng, "Random 2")

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"random": true,
		"top":    1,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 random result, got %d", len(results))
	}
}

// --- Helpers for non-loopback tests ---

func newNonLoopbackRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:12345" // non-loopback
	return req
}

func serveRequest(t *testing.T, srv *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	return w
}
