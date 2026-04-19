package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

func doRequestFrom(t *testing.T, srv *Server, method, path string, body any, remote string) *httptest.ResponseRecorder {
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
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// --- Curation ---

// TestCurationStatusUnavailable: with no runner wired the api should
// return ErrUnavailable (HTTP 503), not 500. setupTestServer wires a
// no-op LLM but no curation runner.
func TestCurationStatusUnavailable(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.CurationStatus(context.Background())
	if apiErr == nil {
		t.Fatal("expected ErrUnavailable, got nil")
	}
	if apiErr.Code != "unavailable" {
		t.Errorf("code = %q, want unavailable", apiErr.Code)
	}
	if apiErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", apiErr.HTTPStatus)
	}
}

func TestCurationStatusHTTPSurfaces503(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/curation", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCurationTriggerLoopbackOnly(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequestFrom(t, srv, "POST", "/v1/curation/trigger", nil, "192.168.1.5:12345")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback trigger, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCurationBatchLoopbackOnly(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequestFrom(t, srv, "POST", "/v1/curation/batch", nil, "10.0.0.1:12345")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback batch, got %d: %s", w.Code, w.Body.String())
	}
}

// All four api curation methods must short-circuit with ErrUnavailable
// when the runner is missing. Guards the dispatch contract used by the
// MCP gramaton_curation tool.
func TestCurationAllMethodsUnavailableWithoutRunner(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	if _, e := srv.api.CurationStatus(ctx); e == nil || e.Code != "unavailable" {
		t.Errorf("Status: want unavailable, got %v", e)
	}
	if _, e := srv.api.CurationTrigger(ctx); e == nil || e.Code != "unavailable" {
		t.Errorf("Trigger: want unavailable, got %v", e)
	}
	if _, e := srv.api.CurationDryRun(ctx); e == nil || e.Code != "unavailable" {
		t.Errorf("DryRun: want unavailable, got %v", e)
	}
	if _, e := srv.api.CurationBatch(ctx); e == nil || e.Code != "unavailable" {
		t.Errorf("Batch: want unavailable, got %v", e)
	}
}

// --- Reembed ---

// TestReembedNoEmbedderUnavailable: setupTestServer wires no embedder.
// Reembed should refuse with ErrUnavailable rather than producing
// partial work or 500ing.
func TestReembedNoEmbedderUnavailable(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Reembed(context.Background(), api.ReembedRequest{})
	if apiErr == nil {
		t.Fatal("expected ErrUnavailable, got nil")
	}
	if apiErr.Code != "unavailable" {
		t.Errorf("code = %q, want unavailable", apiErr.Code)
	}
	if apiErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", apiErr.HTTPStatus)
	}
}

func TestReembedHTTPSurfaces503WithoutEmbedder(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/reembed", map[string]any{})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestReembedBatchCap: requesting an oversized batch caps at MaxReembedBatch.
// We can't actually run a successful pass without an embedder, but we
// can verify the request shape is accepted (the cap kicks in inside
// Reembed before the embedder check; either order is fine for the
// contract). Here we just confirm the binding doesn't choke on the
// large value.
func TestReembedAcceptsLargeBatch(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/reembed", map[string]any{"batch": 10000})
	// Without an embedder the response is 503; we only assert that
	// the request body parsed and the binding dispatched.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (no embedder), got %d: %s", w.Code, w.Body.String())
	}
}
