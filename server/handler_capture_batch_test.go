package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

// TestCaptureBatchHTTPHappyPath drives the POST /v1/capture/batch
// route end-to-end. Pins the wire shape (status code 201, JSON body
// with job_id and added array).
func TestCaptureBatchHTTPHappyPath(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := map[string]any{
		"items": []map[string]any{
			{"content": "first record"},
			{"content": "second record"},
		},
	}
	w := doRequest(t, srv, "POST", "/v1/capture/batch", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.CaptureBatchResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.JobID == "" {
		t.Error("missing job_id")
	}
	if len(env.Data.Added) != 2 {
		t.Errorf("added: got %d want 2", len(env.Data.Added))
	}
}

// TestCaptureBatchHTTPEmpty validates the request-level error path
// surfaces as 400 with the api error code.
func TestCaptureBatchHTTPEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/capture/batch", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "input_error") {
		t.Errorf("expected input_error in body, got %s", w.Body.String())
	}
}
