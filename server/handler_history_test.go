package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/graph"
)

func TestDiffWithSince(t *testing.T) {
	srv, eng := setupTestServer(t)

	// First commit.
	addRecord(t, eng, "First record")

	// Second commit with different content.
	addRecord(t, eng, "Second record")

	// Use yesterday as since -- both commits should be after it,
	// and the first commit should be the "since" point.
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")

	w := doRequest(t, srv, "GET", "/v1/diff?since="+yesterday, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// Just verify it returns successfully with valid structure.
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["added"] == nil {
		t.Fatal("expected added field in diff response")
	}
}

func TestDiffInvalidDate(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Record")

	w := doRequest(t, srv, "GET", "/v1/diff?since=not-a-date", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date, got %d", w.Code)
	}
}

func TestDiffWithTopic(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create initial commit.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Seed"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		for k, v := range n.Properties {
			eng.PropIdx().Add(id, k, v)
		}
	}
	eng.Save("seed")
	eng.Unlock()

	time.Sleep(10 * time.Millisecond)
	sinceTime := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	time.Sleep(10 * time.Millisecond)

	// Add records with different topics.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("About Kafka streaming"),
		"content_short":     graph.StringProperty("Kafka streaming setup"),
		"content_keywords":  graph.StringListProperty([]string{"kafka", "streaming"}),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("About Redis caching"),
		"content_short":     graph.StringProperty("Redis cache setup"),
		"content_keywords":  graph.StringListProperty([]string{"redis", "cache"}),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.Save("add topics")
	eng.Unlock()

	w := doRequest(t, srv, "GET", "/v1/diff?since="+sinceTime+"&topic=kafka", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	added := data["added"].([]any)
	// Should filter to only kafka records.
	for _, a := range added {
		rec := a.(map[string]any)
		if ss, ok := rec["summary_short"].(string); ok {
			if ss == "Redis cache setup" {
				t.Fatal("redis record should be filtered out by topic=kafka")
			}
		}
	}
}

func TestLogWithLimit(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Commit 1")
	addRecord(t, eng, "Commit 2")
	addRecord(t, eng, "Commit 3")

	w := doRequest(t, srv, "GET", "/v1/log?limit=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	commits := data["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits with limit, got %d", len(commits))
	}
}

func TestLogCommitFields(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Log test")

	w := doRequest(t, srv, "GET", "/v1/log?limit=1", nil)
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	commits := data["commits"].([]any)
	if len(commits) < 1 {
		t.Fatal("expected at least 1 commit")
	}
	commit := commits[0].(map[string]any)
	if commit["hash"] == nil {
		t.Fatal("expected hash field")
	}
	if commit["timestamp"] == nil {
		t.Fatal("expected timestamp field")
	}
	if commit["action"] == nil {
		t.Fatal("expected action field")
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "Backup round-trip test")

	// Create backup.
	w := doRequest(t, srv, "POST", "/v1/backup", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("backup: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	archivePath := data["path"].(string)

	// Add another record that shouldn't survive restore.
	addRecord(t, eng, "Should disappear after restore")

	// Restore from backup.
	w2 := doRequest(t, srv, "POST", "/v1/restore", map[string]any{
		"path":  archivePath,
		"force": true,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	// Verify only the original record exists.
	w3 := doRequest(t, srv, "POST", "/v1/search", map[string]any{"top": 10})
	resp3 := parseResponse(t, w3)
	data3 := resp3["data"].(map[string]any)
	results := data3["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 record after restore, got %d", len(results))
	}
}

func TestClassifyNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/records/nonexistent/classify", map[string]any{
		"temporality": "durable",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeleteNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "DELETE", "/v1/records/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
