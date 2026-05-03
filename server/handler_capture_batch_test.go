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

// submitCaptureBatch posts a tiny batch and returns the JobID. Used
// by the wiring tests below to seed a job for status/cancel/result
// requests.
func submitCaptureBatch(t *testing.T, srv *Server, content string) string {
	t.Helper()
	w := doRequest(t, srv, "POST", "/v1/capture/batch", map[string]any{
		"items": []map[string]any{{"content": content}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.CaptureBatchResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("seed: unmarshal: %v", err)
	}
	if env.Data.JobID == "" {
		t.Fatal("seed: empty JobID")
	}
	return env.Data.JobID
}

// TestCaptureBatchStatusHTTP: GET /v1/capture/batch/{job_id}/status
// returns 200 with the job snapshot.
func TestCaptureBatchStatusHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitCaptureBatch(t, srv, "status seed")
	w := doRequest(t, srv, "GET", "/v1/capture/batch/"+jobID+"/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.CaptureBatchStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.JobID != jobID {
		t.Errorf("JobID: got %q want %q", env.Data.JobID, jobID)
	}
}

// TestCaptureBatchStatusHTTPMissing: unknown job_id -> 404.
func TestCaptureBatchStatusHTTPMissing(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/capture/batch/01HQQQQQQQQQQQQQQQQQQQQQQQ/status", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCaptureBatchCancelHTTP: POST cancel returns 200; cancelling a
// completed job is idempotent (Cancelled=false in payload).
func TestCaptureBatchCancelHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitCaptureBatch(t, srv, "cancel seed")
	w := doRequest(t, srv, "POST", "/v1/capture/batch/"+jobID+"/cancel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.CaptureBatchCancelResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.JobID != jobID {
		t.Errorf("JobID: got %q want %q", env.Data.JobID, jobID)
	}
	// Sync mode finished before we got here, so Cancelled is false.
	if env.Data.Cancelled {
		t.Error("expected Cancelled=false for already-completed sync job")
	}
}

// TestCaptureBatchResultHTTP: GET result on a completed sync job
// returns the full payload immediately.
func TestCaptureBatchResultHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitCaptureBatch(t, srv, "result seed")
	w := doRequest(t, srv, "GET", "/v1/capture/batch/"+jobID+"/result?timeout_ms=5000", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.CaptureBatchResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Status != "completed" {
		t.Errorf("status: %q", env.Data.Status)
	}
	if len(env.Data.Added) != 1 {
		t.Errorf("added: %d", len(env.Data.Added))
	}
}

// TestJobsListHTTP: GET /v1/jobs returns 200 with a list of summaries.
func TestJobsListHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitCaptureBatch(t, srv, "jobs-list seed")
	w := doRequest(t, srv, "GET", "/v1/jobs?status=completed", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.JobsListResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, j := range env.Data.Jobs {
		if j.ID == jobID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seeded job %s not in list (%d returned)", jobID, len(env.Data.Jobs))
	}
}

// (HTTP cap on limit is enforced by parseIntParam clamping silently
// — the api-level TestJobsListPaginationCap exercises the rejection
// path directly. The HTTP layer's clamp behaviour matches the rest
// of the gramaton record/cluster routes.)
