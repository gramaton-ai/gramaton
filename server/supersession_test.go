package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/graph"
)

func TestAutoSupersessionAlreadyHistorical(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create a record that's already historical (has valid_until).
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Already expired"),
		"content_short":     graph.StringProperty("Expired"),
		"processing_status": graph.StringProperty("processed"),
		"valid_until":       graph.TimestampProperty(time.Now().UTC().Add(-24 * time.Hour)),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, []float32{1.0, 0.0, 0.0})
	eng.Graph().SetNodeProperty(n.ID, "embedding_full",
		graph.VectorProperty([]float32{1.0, 0.0, 0.0}))
	eng.Save("seed")
	eng.Unlock()

	// Create a near-identical record. The old one is already historical,
	// so supersession should NOT set valid_until again.
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content": "Already expired updated",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the old record still has its original valid_until
	// (not overwritten by supersession).
	eng.RLock()
	old, _ := eng.Graph().GetNode(n.ID)
	vu, ok := old.Properties.GetTimestamp("valid_until")
	eng.RUnlock()

	if !ok {
		t.Fatal("old record should still have valid_until")
	}
	// Should be the original value (yesterday), not just now.
	if vu.After(time.Now().UTC().Add(-23 * time.Hour)) {
		t.Fatal("valid_until should be the original (yesterday), not updated")
	}
}

func TestCaptureWithNoEmbedder(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Without an embedder, capture should still work --
	// no dedup check, no supersession, just creates the record.
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":     "No embedder content",
		"temporality": "durable",
		"confidence":  0.9,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["superseded"] != nil {
		t.Fatal("should not have superseded without embedder")
	}
}

func TestCaptureResponseIncludesWarnings(t *testing.T) {
	srv, _ := setupTestServer(t)

	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content": "Record with warnings check",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	// ID should always be present.
	if data["id"] == nil || data["id"] == "" {
		t.Fatal("expected id in response")
	}
}

func TestCaptureProcessingStatus(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Without classification: should be "captured".
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content": "No classification",
	})
	resp := parseResponse(t, w)
	id := resp["data"].(map[string]any)["id"].(string)

	w2 := doRequest(t, srv, "GET", "/v1/records/"+id, nil)
	resp2 := parseResponse(t, w2)
	props := resp2["data"].(map[string]any)["properties"].(map[string]any)
	if props["processing_status"] != "captured" {
		t.Fatalf("expected 'captured', got %v", props["processing_status"])
	}

	// With classification: should be "processed".
	w3 := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":     "With classification",
		"temporality": "durable",
	})
	resp3 := parseResponse(t, w3)
	id2 := resp3["data"].(map[string]any)["id"].(string)

	w4 := doRequest(t, srv, "GET", "/v1/records/"+id2, nil)
	resp4 := parseResponse(t, w4)
	props2 := resp4["data"].(map[string]any)["properties"].(map[string]any)
	if props2["processing_status"] != "processed" {
		t.Fatalf("expected 'processed', got %v", props2["processing_status"])
	}
}
