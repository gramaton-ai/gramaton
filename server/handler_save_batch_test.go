package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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

// TestJobsListLimitRejectedOverCap: GET /v1/jobs?limit=<over the cap>
// must reject with 400, matching api.JobsList's own rejection
// (api-level TestJobsListPaginationCap covers the same cap directly).
// Before the fix the HTTP route clamped an over-limit value down to
// api.MaxJobsListLimit via parseIntParam, so the same request that the
// MCP/CLI transports reject (calling api.JobsList directly) silently
// succeeded over HTTP with a smaller page instead.
func TestJobsListLimitRejectedOverCap(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", fmt.Sprintf("/v1/jobs?limit=%d", api.MaxJobsListLimit+1), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.Code != "input_error" {
		t.Errorf("code = %q, want input_error", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, fmt.Sprintf("%d", api.MaxJobsListLimit)) {
		t.Errorf("message %q should name the cap %d", env.Error.Message, api.MaxJobsListLimit)
	}
}

// TestJobsListLimitNonNumericRejected: GET /v1/jobs?limit=abc must
// reject with 400 at the HTTP layer (the parse itself fails, before
// api.JobsList ever sees a value) rather than silently falling back
// to the default page size.
func TestJobsListLimitNonNumericRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "GET", "/v1/jobs?limit=abc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestResultWaitBudgetFitsTransport pins the transport clamp on
// blocking result waits: the budget must leave room for the timeout
// snapshot to be written before the HTTP server aborts the response
// at httpWriteTimeout, the default (0) and any longer ask must both
// land on the budget, and a short ask passes through untouched.
// Without the clamp, a wait beyond the write timeout ends as a
// connection reset instead of the documented retryable-timeout
// snapshot.
func TestResultWaitBudgetFitsTransport(t *testing.T) {
	budget := time.Duration(resultWaitBudgetMS) * time.Millisecond
	if budget >= httpWriteTimeout {
		t.Fatalf("result wait budget %v must be under the HTTP write timeout %v", budget, httpWriteTimeout)
	}
	if got := clampResultWait(0); got != resultWaitBudgetMS {
		t.Errorf("clamp(0) = %d, want the budget %d (the api would expand 0 to 30 min)", got, resultWaitBudgetMS)
	}
	if got := clampResultWait(api.MaxResultTimeoutMS); got != resultWaitBudgetMS {
		t.Errorf("clamp(max) = %d, want the budget %d", got, resultWaitBudgetMS)
	}
	if got := clampResultWait(5000); got != 5000 {
		t.Errorf("clamp(5000) = %d, want the short ask passed through", got)
	}
}
