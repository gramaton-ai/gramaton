package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
)

// runCaptureBatchAsync is the goroutine entry point for the async
// capture-batch path. Lifecycle:
//
//  1. defer recover() so a panic inside the core path is captured as
//     status=failed/panicked rather than leaking a stuck job.
//  2. Re-read the Job's status; bail if a cancel-before-pickup raced
//     with the spawn (rare, but possible since Create-then-go is two
//     operations).
//  3. AdvanceStatus pending → running. If that's already cancelled,
//     ErrInvalidTransition tells us a cancel won the race; exit cleanly.
//  4. Run the same per-batch core that sync uses.
//  5. unregisterAsyncRunner so the registry doesn't leak the entry.
//
// The goroutine NEVER returns an APIError to a caller; it persists
// final state on the Job record. Callers poll via SaveBatchStatus /
// SaveBatchResult.
func (a *API) runCaptureBatchAsync(ctx context.Context, jobID string, req SaveBatchRequest) {
	defer a.unregisterAsyncRunner(jobID)
	defer a.recoverAsyncPanic(jobID)

	store := a.engine.JobStore()
	if store == nil {
		a.log.Error("capture_batch async runner: jobstore unavailable", "job_id", jobID)
		return
	}

	// Re-read the persisted Job. If a cancel landed between Create
	// and our goroutine pickup, exit before doing any work.
	current, err := store.Get(jobID)
	if err != nil {
		a.log.Warn("capture_batch async runner: get job failed", "job_id", jobID, "err", err)
		return
	}
	if current.Status == jobs.StatusCancelled || current.Status == jobs.StatusFailed {
		return
	}

	// Honor a cancel that arrived before this goroutine got scheduled.
	if ctx.Err() != nil {
		a.finalizeCancelled(jobID, "cancelled_before_start")
		return
	}

	// Advance pending → running. ErrInvalidTransition means a cancel
	// already flipped the state; exit cleanly without touching the
	// Job further.
	if err := store.AdvanceStatus(jobID, jobs.StatusRunning, func(j *jobs.Job) {
		j.StartedAt = time.Now().UTC()
	}); err != nil {
		if errors.Is(err, jobs.ErrInvalidTransition) {
			return
		}
		a.log.Warn("capture_batch async runner: advance to running failed",
			"job_id", jobID, "err", err)
		return
	}

	// The Job pointer we hand to the core needs the running state
	// reflected so per-chunk progress updates use the correct base.
	current.Status = jobs.StatusRunning
	current.StartedAt = time.Now().UTC()

	// L6 multi-chunk runner: split items into MaxSyncBatchSize chunks,
	// commit each in its own Save, persist progress between chunks,
	// then run the post-chunks edge-fixup commit. Cancellation flips
	// at chunk boundaries; per-chunk Save failures roll back only the
	// failing chunk. The L5 single-chunk shortcut is gone.
	a.runCaptureBatchAsyncChunked(ctx, jobID, req, current)
}

// recoverAsyncPanic is the deferred panic handler for the async
// runner. It marks the Job failed with a panicked reason so callers
// see a terminal state instead of a permanently-running phantom.
// Re-reads the Job before mutating so it doesn't clobber a state the
// core path already set (e.g., the panic happened AFTER the runner
// flipped to completed, in which case the recover does nothing).
func (a *API) recoverAsyncPanic(jobID string) {
	r := recover()
	if r == nil {
		return
	}
	a.log.Error("capture_batch async runner panicked",
		"job_id", jobID, "panic", fmt.Sprintf("%v", r))
	store := a.engine.JobStore()
	if store == nil {
		return
	}
	j, err := store.Get(jobID)
	if err != nil {
		return
	}
	if j.Status != jobs.StatusPending && j.Status != jobs.StatusRunning {
		return
	}
	j.Status = jobs.StatusFailed
	j.FailureReason = fmt.Sprintf("panicked: %v", r)
	j.CompletedAt = time.Now().UTC()
	if uerr := store.Update(j); uerr != nil {
		a.log.Error("capture_batch panic-recovery update failed",
			"job_id", jobID, "err", uerr)
	}
}

// finalizeCancelled marks a Job that never started its core work as
// cancelled with a specific reason. Used when ctx.Done() fires before
// the runner enters its core path.
//
// Race guard: if a concurrent SaveBatchCancel already flipped the
// Job to cancelled, exit without touching it -- the cancel handler's
// CompletedAt and FailureReason should win, not be clobbered.
func (a *API) finalizeCancelled(jobID, reason string) {
	store := a.engine.JobStore()
	if store == nil {
		return
	}
	j, err := store.Get(jobID)
	if err != nil {
		return
	}
	if j.Status == jobs.StatusCancelled {
		return
	}
	if j.Status != jobs.StatusPending && j.Status != jobs.StatusRunning {
		return
	}
	now := time.Now().UTC()
	if err := store.AdvanceStatus(jobID, jobs.StatusCancelled, func(j *jobs.Job) {
		j.FailureReason = reason
		j.CompletedAt = now
	}); err != nil {
		a.log.Warn("capture_batch finalize-cancelled failed",
			"job_id", jobID, "err", err)
	}
}
