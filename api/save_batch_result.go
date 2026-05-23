package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
)

// SaveBatchResultRequest selects a job by ID with an optional
// timeout. TimeoutMS=0 falls back to cfg.Jobs.ResultDefaultTimeout
// (30 minutes) so a caller doesn't accidentally hang forever on a
// stuck job.
type SaveBatchResultRequest struct {
	JobID     string `json:"job_id" jsonschema:"the job_id returned by gramaton_save_batch"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"max ms to wait for terminal state; 0 = use cfg.Jobs.ResultDefaultTimeout (30 min); max 30 minutes (1800000 ms)"`
}

// SaveBatchResultDescription is the MCP tool description for
// gramaton_save_batch_result.
const SaveBatchResultDescription = `Block until an async gramaton_save_batch job reaches a terminal state (completed/failed/cancelled), then return the full response payload (added/failed/edges/edges_failed/stats).

The call returns immediately if the job is already terminal. It honors the timeout_ms argument (default cfg.Jobs.ResultDefaultTimeout = 30 min); on timeout it returns the current Job snapshot with status=running and an unavailable error code so the caller can retry.

For polling progress without blocking use gramaton_save_batch_status.`

// SaveBatchResult blocks (with poll backoff) until the Job reaches
// a terminal state or the timeout elapses. On timeout returns the
// current Job snapshot and a "timeout" APIError so the caller knows
// the wait didn't complete.
//
// The timeout is bounded by MaxResultTimeoutMS even when the caller
// passes a larger value. Holding a connection for longer is a
// footgun; the caller should poll Status instead. Per-tenant Job
// access is enforced inside the poll loop.
func (a *API) SaveBatchResult(ctx context.Context, req SaveBatchResultRequest) (SaveBatchResponse, *APIError) {
	if req.JobID == "" {
		return SaveBatchResponse{}, ErrMissing("job_id is required")
	}
	if req.TimeoutMS < 0 {
		return SaveBatchResponse{}, ErrInvalid("timeout_ms must not be negative")
	}
	if req.TimeoutMS > MaxResultTimeoutMS {
		return SaveBatchResponse{}, ErrInvalid(fmt.Sprintf("timeout_ms exceeds %d (30 min)", MaxResultTimeoutMS))
	}
	store := a.engine.JobStore()
	if store == nil {
		return SaveBatchResponse{}, ErrUnavailable("jobstore unavailable")
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = a.engine.Config().Jobs.ResultDefaultTimeout
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
	}
	deadline := time.Now().Add(timeout)

	// Poll backoff: tight to start (small batches finish fast) and
	// stretch to a second for long-running ones to keep CPU low.
	delay := 25 * time.Millisecond
	const maxDelay = 1 * time.Second

	tenant := tenantFromContext(ctx)
	for {
		j, err := store.Get(req.JobID)
		if err != nil {
			if err == jobs.ErrNotFound {
				return SaveBatchResponse{}, ErrNotFound("job not found")
			}
			a.log.Warn("capture_batch_result: get failed", "job_id", req.JobID, "err", err)
			return SaveBatchResponse{}, ErrInternal("failed to read job")
		}
		if !tenantOwnsJob(tenant, j.TenantID) {
			return SaveBatchResponse{}, ErrNotFound("job not found")
		}
		switch j.Status {
		case jobs.StatusCompleted, jobs.StatusFailed, jobs.StatusCancelled:
			return responseFromJob(j), nil
		}

		if time.Now().After(deadline) {
			snap := responseFromJob(j)
			return snap, &APIError{
				Code:       "timeout",
				Message:    "result wait timed out; job still running",
				HTTPStatus: 504,
				Retryable:  true,
			}
		}
		select {
		case <-ctx.Done():
			return SaveBatchResponse{}, ErrUnavailable("context cancelled")
		case <-time.After(delay):
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

// responseFromJob projects a stored Job back into a
// SaveBatchResponse. Sync-mode jobs persist the full Result
// payload; async jobs that haven't finished yet rebuild a
// status-only snapshot.
func responseFromJob(j *jobs.Job) SaveBatchResponse {
	resp := SaveBatchResponse{
		JobID:  j.ID,
		Status: j.Status,
		Stats: CaptureBatchStats{
			TotalItems:  j.TotalItems,
			FailedCount: len(j.Errors),
		},
	}
	if len(j.Result) > 0 {
		_ = json.Unmarshal(j.Result, &resp)
		resp.JobID = j.ID
		resp.Status = j.Status
	} else if len(j.Errors) > 0 {
		resp.Failed = failuresFromErrors(j.Errors)
	}
	return resp
}
