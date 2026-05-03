package api

import (
	"context"
	"errors"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
)

// CaptureBatchCancelRequest selects a job by ID.
type CaptureBatchCancelRequest struct {
	JobID string `json:"job_id" jsonschema:"the job_id returned by gramaton_capture_batch"`
}

// CaptureBatchCancelResponse echoes the post-cancel terminal state.
// If the job was already terminal (completed/failed/cancelled), the
// call is a no-op and Status reflects the prior terminal value;
// Cancelled is true only when this call moved the state.
type CaptureBatchCancelResponse struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	Cancelled bool   `json:"cancelled"`
}

// CaptureBatchCancelDescription is the MCP tool description for
// gramaton_capture_batch_cancel.
const CaptureBatchCancelDescription = `Cancel an async gramaton_capture_batch job that is still pending or running.

Items already committed in prior chunks remain in the store; the runner exits at the next chunk boundary (or before its first chunk if cancelled early enough). On a cancelled-while-embed run the runner short-circuits via context cancellation and no items commit.

Idempotent: cancelling an already-cancelled or already-terminal job returns the current state without error.`

// CaptureBatchCancel flips the Job's status to cancelled and signals
// the runner's context (which the runner observes between chunks and
// during embed). One retry on transient JobStore.Update failure to
// tolerate brief bbolt contention.
func (a *API) CaptureBatchCancel(_ context.Context, req CaptureBatchCancelRequest) (CaptureBatchCancelResponse, *APIError) {
	if req.JobID == "" {
		return CaptureBatchCancelResponse{}, ErrMissing("job_id is required")
	}
	store := a.engine.JobStore()
	if store == nil {
		return CaptureBatchCancelResponse{}, ErrUnavailable("jobstore unavailable")
	}

	// Read first to give early-terminal an idempotent no-op response
	// instead of letting AdvanceStatus convert it to ErrInvalidTransition.
	j, err := store.Get(req.JobID)
	if err != nil {
		if err == jobs.ErrNotFound {
			return CaptureBatchCancelResponse{}, ErrNotFound("job not found")
		}
		a.log.Warn("capture_batch_cancel: get failed", "job_id", req.JobID, "err", err)
		return CaptureBatchCancelResponse{}, ErrInternal("failed to read job")
	}
	if j.Status == jobs.StatusCompleted || j.Status == jobs.StatusFailed || j.Status == jobs.StatusCancelled {
		return CaptureBatchCancelResponse{
			JobID:     j.ID,
			Status:    j.Status,
			Cancelled: false,
		}, nil
	}

	now := time.Now().UTC()
	mutator := func(j *jobs.Job) {
		j.CompletedAt = now
	}
	advanceErr := a.injectFault(FaultPhaseJobstoreUpdate)
	if advanceErr == nil {
		advanceErr = store.AdvanceStatus(req.JobID, jobs.StatusCancelled, mutator)
	}
	if advanceErr != nil {
		// Transient bbolt contention deserves one retry. Invalid-
		// transition is NOT transient: someone raced us to a
		// terminal state. Re-read and return that.
		if errors.Is(advanceErr, jobs.ErrInvalidTransition) {
			j2, err := store.Get(req.JobID)
			if err == nil {
				return CaptureBatchCancelResponse{JobID: j2.ID, Status: j2.Status, Cancelled: false}, nil
			}
			return CaptureBatchCancelResponse{}, ErrInternal("failed to read job after race")
		}
		a.log.Warn("capture_batch_cancel: first attempt failed; retrying",
			"job_id", req.JobID, "err", advanceErr)
		retryErr := a.injectFault(FaultPhaseJobstoreUpdate)
		if retryErr == nil {
			retryErr = store.AdvanceStatus(req.JobID, jobs.StatusCancelled, mutator)
		}
		if retryErr != nil {
			if errors.Is(retryErr, jobs.ErrInvalidTransition) {
				j2, err := store.Get(req.JobID)
				if err == nil {
					return CaptureBatchCancelResponse{JobID: j2.ID, Status: j2.Status, Cancelled: false}, nil
				}
			}
			a.log.Error("capture_batch_cancel: retry failed",
				"job_id", req.JobID, "err", retryErr)
			return CaptureBatchCancelResponse{}, ErrInternal("failed to cancel job")
		}
	}

	// Signal the runner's context so an in-flight embed exits and the
	// runner observes cancellation at its next checkpoint.
	a.signalAsyncRunner(req.JobID)

	return CaptureBatchCancelResponse{
		JobID:     req.JobID,
		Status:    jobs.StatusCancelled,
		Cancelled: true,
	}, nil
}
