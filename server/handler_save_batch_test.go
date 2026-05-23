package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

// TestSaveBatchHTTPHappyPath drives the POST /v1/save/batch
// route end-to-end. Pins the wire shape (status code 201, JSON body
// with job_id and added array).
func TestSaveBatchHTTPHappyPath(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := map[string]any{
		"items": []map[string]any{
			{"content": "first record"},
			{"content": "second record"},
		},
	}
	w := doRequest(t, srv, "POST", "/v1/save/batch", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.SaveBatchResponse `json:"data"`
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

// TestSaveBatchHTTPEmpty validates the request-level error path
// surfaces as 400 with the api error code.
func TestSaveBatchHTTPEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/save/batch", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "input_error") {
		t.Errorf("expected input_error in body, got %s", w.Body.String())
	}
}

// submitSaveBatch posts a tiny batch and returns the JobID. Used
// by the wiring tests below to seed a job for status/cancel/result
// requests.
func submitSaveBatch(t *testing.T, srv *Server, content string) string {
	t.Helper()
	w := doRequest(t, srv, "POST", "/v1/save/batch", map[string]any{
		"items": []map[string]any{{"content": content}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.SaveBatchResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("seed: unmarshal: %v", err)
	}
	if env.Data.JobID == "" {
		t.Fatal("seed: empty JobID")
	}
	return env.Data.JobID
}

// TestSaveBatchStatusHTTP: GET /v1/save/batch/{job_id}/status
// returns 200 with the job snapshot.
func TestSaveBatchStatusHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitSaveBatch(t, srv, "status seed")
	w := doRequest(t, srv, "GET", "/v1/save/batch/"+jobID+"/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.SaveBatchStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.JobID != jobID {
		t.Errorf("JobID: got %q want %q", env.Data.JobID, jobID)
	}
}

// TestSaveBatchStatusHTTPMissing: unknown job_id -> 404.
func TestSaveBatchStatusHTTPMissing(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/save/batch/01HQQQQQQQQQQQQQQQQQQQQQQQ/status", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSaveBatchCancelHTTP: POST cancel returns 200; cancelling a
// completed job is idempotent (Cancelled=false in payload).
func TestSaveBatchCancelHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitSaveBatch(t, srv, "cancel seed")
	w := doRequest(t, srv, "POST", "/v1/save/batch/"+jobID+"/cancel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.SaveBatchCancelResponse `json:"data"`
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

// TestSaveBatchResultHTTP: GET result on a completed sync job
// returns the full payload immediately.
func TestSaveBatchResultHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	jobID := submitSaveBatch(t, srv, "result seed")
	w := doRequest(t, srv, "GET", "/v1/save/batch/"+jobID+"/result?timeout_ms=5000", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data api.SaveBatchResponse `json:"data"`
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
	jobID := submitSaveBatch(t, srv, "jobs-list seed")
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
