package server

import (
	"net/http"
	"testing"
)

func TestSaveWithNoEmbedder(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Without an embedder, capture should still work -- no
	// similarity scan, no hold, no advisory; just creates the record
	// (marked for a deferred check when embeddings arrive).
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
	if data["held"] != nil {
		t.Fatal("should not hold without embedder")
	}
	if data["advisory"] != nil {
		t.Fatal("should not carry an advisory without embedder")
	}
}

func TestSaveResponseIncludesWarnings(t *testing.T) {
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

func TestSaveProcessingStatus(t *testing.T) {
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
