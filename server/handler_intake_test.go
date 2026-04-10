package server

import (
	"net/http"
	"testing"
	"time"
)

func TestIntakeDeliberateCapture(t *testing.T) {
	srv, eng := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"content":              "We decided to use PostgreSQL for the main database",
		"context_capture_reason": "recording architecture decision",
		"context_source_type":    "team discussion",
		"keywords":             []string{"postgresql", "database", "architecture"},
		"summary_short":        "Chose PostgreSQL for main database",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["route"] != "knowledge" {
		t.Fatalf("expected route=knowledge, got %v", data["route"])
	}
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected non-empty id in response")
	}

	// Verify the record exists with context signals stored.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("record not found after intake")
	}
	if v, _ := n.Properties.GetString("context_capture_reason"); v != "recording architecture decision" {
		t.Fatalf("context_capture_reason not stored: %q", v)
	}
	if v, _ := n.Properties.GetString("context_source_type"); v != "team discussion" {
		t.Fatalf("context_source_type not stored: %q", v)
	}
}

func TestIntakeObservedMode(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"mode":  "observed",
		"facts": []string{"User prefers dark mode", "Project uses Go 1.22 with generics"},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["route"] != "observed" {
		t.Fatalf("expected route=observed, got %v", data["route"])
	}
	if data["accepted"] != true {
		t.Fatal("expected accepted=true")
	}

	// Let the background goroutine complete before TempDir cleanup.
	time.Sleep(200 * time.Millisecond)
}

func TestIntakeRequiresContentOrFacts(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty intake, got %d: %s", w.Code, w.Body.String())
	}
}
