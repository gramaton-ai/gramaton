package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
	"github.com/gramaton-ai/gramaton/testutil"
)

// pollUntilTerminal blocks (with bounded retries) until the named job
// reaches a terminal status. Used to wait for async runners to finish
// without sleeping a fixed duration. The deadline is scaled via
// testutil.Timeout so Windows runners (slower under -race) don't
// trip the bound on otherwise-healthy paths.
func pollUntilTerminal(t *testing.T, a *API, jobID string, timeout time.Duration) *jobs.Job {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for {
		j, err := a.engine.JobStore().Get(jobID)
		if err != nil {
			t.Fatalf("pollUntilTerminal: get %s: %v", jobID, err)
		}
		switch j.Status {
		case jobs.StatusCompleted, jobs.StatusFailed, jobs.StatusCancelled:
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("pollUntilTerminal: %s did not reach terminal in %v (status=%s)", jobID, timeout, j.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestCaptureBatchAsyncHappyPath: wait=false returns immediately with
// pending; runner picks up and the Job ends in completed.
func TestCaptureBatchAsyncHappyPath(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	f := false
	resp, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("alpha", "beta", "gamma"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	j := pollUntilTerminal(t, a, resp.JobID, 5*time.Second)
	if j.Status != jobs.StatusCompleted {
		t.Errorf("status: %q want completed (reason=%q)", j.Status, j.FailureReason)
	}
	if j.ProcessedCount != 3 {
		t.Errorf("processed: %d want 3", j.ProcessedCount)
	}
}

// TestCaptureBatchAsyncOverCap: more than MaxAsyncBatchSize rejected.
func TestCaptureBatchAsyncOverCap(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	f := false
	items := make([]CaptureBatchItem, MaxAsyncBatchSize+1)
	for i := range items {
		items[i] = CaptureBatchItem{CaptureRequest: CaptureRequest{Content: "x"}}
	}
	_, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: items,
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
}

// TestCaptureBatchStatusUnknownJob: unknown JobID -> ErrNotFound.
func TestCaptureBatchStatusUnknownJob(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.CaptureBatchStatus(context.Background(), CaptureBatchStatusRequest{
		JobID: "01HQQQQQQQQQQQQQQQQQQQQQQQ",
	})
	if apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
}

// TestCaptureBatchStatusEmptyJobID: missing job_id -> ErrMissing.
func TestCaptureBatchStatusEmptyJobID(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.CaptureBatchStatus(context.Background(), CaptureBatchStatusRequest{})
	if apiErr == nil || apiErr.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %v", apiErr)
	}
}

// TestCaptureBatchStatusReadsRunningJob: status reflects an in-flight
// async job's metadata.
func TestCaptureBatchStatusReadsRunningJob(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	f := false
	resp, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("x"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	pollUntilTerminal(t, a, resp.JobID, 5*time.Second)
	st, apiErr := a.CaptureBatchStatus(context.Background(), CaptureBatchStatusRequest{JobID: resp.JobID})
	if apiErr != nil {
		t.Fatalf("Status: %v", apiErr)
	}
	if st.Status != jobs.StatusCompleted {
		t.Errorf("status: %q want completed", st.Status)
	}
	if st.TotalItems != 1 || st.ProcessedCount != 1 {
		t.Errorf("counts: total=%d processed=%d want 1/1", st.TotalItems, st.ProcessedCount)
	}
	if st.Kind != jobs.KindCaptureBatch {
		t.Errorf("kind: %q", st.Kind)
	}
}

// TestCaptureBatchCancelAlreadyCompleted: cancelling a terminal job
// returns its current state, Cancelled=false.
func TestCaptureBatchCancelAlreadyCompleted(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Items: mustItems("x"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	c, apiErr := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{
		JobID: resp.JobID,
	})
	if apiErr != nil {
		t.Fatalf("Cancel: %v", apiErr)
	}
	if c.Cancelled {
		t.Error("expected Cancelled=false for already-completed")
	}
	if c.Status != jobs.StatusCompleted {
		t.Errorf("status: %q want completed", c.Status)
	}
}

// TestCaptureBatchCancelAlreadyCancelled: cancelling a cancelled job
// is idempotent (no-op, Cancelled=false).
func TestCaptureBatchCancelAlreadyCancelled(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	f := false
	resp, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("x"),
	})
	first, _ := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{JobID: resp.JobID})
	pollUntilTerminal(t, a, resp.JobID, 5*time.Second)
	second, apiErr := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{JobID: resp.JobID})
	if apiErr != nil {
		t.Fatalf("Cancel #2: %v", apiErr)
	}
	_ = first
	if second.Cancelled {
		t.Error("expected Cancelled=false on idempotent retry")
	}
	if second.Status != jobs.StatusCancelled && second.Status != jobs.StatusCompleted {
		t.Errorf("status: %q (race-tolerated either cancelled or completed)", second.Status)
	}
}

// TestCaptureBatchCancelUnknownJob: ErrNotFound.
func TestCaptureBatchCancelUnknownJob(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{
		JobID: "01HQQQQQQQQQQQQQQQQQQQQQQQ",
	})
	if apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
}

// TestCaptureBatchCancelPersistFailureRetrySucceeds: first persist
// attempt fails via the once-injector; the second succeeds; cancel
// reaches the cancelled state without surfacing the transient.
// Uses blockingInjector to park the runner so the cancel is
// guaranteed to find a non-terminal job AND we can assert the
// onceInjector actually fired (no race-tolerant escape).
func TestCaptureBatchCancelPersistFailureRetrySucceeds(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	park := newBlockingInjector()
	release := park.blockOn(FaultPhaseChunkSave)
	a.SetFaultInjector(park)
	defer func() {
		release()
		a.SetFaultInjector(nil)
	}()

	f := false
	resp, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("x"),
	})
	park.waitEntered(t, FaultPhaseChunkSave, 2*time.Second)

	once := &onceInjector{phase: FaultPhaseJobstoreUpdate, err: errors.New("transient")}
	a.SetFaultInjector(once)

	c, apiErr := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{JobID: resp.JobID})
	if apiErr != nil {
		t.Fatalf("Cancel: %v (expected retry to succeed)", apiErr)
	}
	if !c.Cancelled || c.Status != jobs.StatusCancelled {
		t.Errorf("expected Cancelled=true, status=cancelled; got Cancelled=%v status=%q", c.Cancelled, c.Status)
	}
	if !once.fired {
		t.Error("onceInjector did not fire — retry path was never exercised")
	}

	// Re-arm parking injector for cleanup release.
	a.SetFaultInjector(park)
}

// onceInjector returns err only on the FIRST call to the named phase.
// Subsequent calls return nil.
type onceInjector struct {
	phase string
	err   error
	fired bool
}

func (o *onceInjector) Inject(phase string) error {
	if phase != o.phase || o.fired {
		return nil
	}
	o.fired = true
	return o.err
}

// TestCaptureBatchCancelPersistFailureBothFail: both persist attempts
// fail; cancel returns ErrInternal so the caller knows the cancel
// didn't take. Uses blockingInjector to park the runner inside Phase
// 3 so the cancel is guaranteed to race a non-terminal job.
func TestCaptureBatchCancelPersistFailureBothFail(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	inj := newBlockingInjector()
	release := inj.blockOn(FaultPhaseChunkSave)
	a.SetFaultInjector(inj)
	defer func() {
		// Release the runner so cleanup can drain.
		release()
		a.SetFaultInjector(nil)
	}()

	f := false
	resp, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("y"),
	})
	inj.waitEntered(t, FaultPhaseChunkSave, 2*time.Second)

	// Now switch to a stub that fails BOTH AdvanceStatus calls. The
	// blockingInjector itself doesn't return errors for jobstore_update;
	// re-arm with a stub that does.
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseJobstoreUpdate: errors.New("forever"),
	}})
	_, apiErr := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{JobID: resp.JobID})
	if apiErr == nil || apiErr.Code != "internal_error" {
		t.Errorf("expected internal_error on persist-double-fail, got %v", apiErr)
	}

	// Restore the blocking injector so the deferred release+cleanup
	// path still drains the runner.
	a.SetFaultInjector(inj)
}

// TestCaptureBatchResultBlocksUntilCompletion: the result endpoint
// returns the full payload once the runner finishes.
func TestCaptureBatchResultBlocksUntilCompletion(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	f := false
	resp, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("alpha", "beta"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	full, apiErr := a.CaptureBatchResult(context.Background(), CaptureBatchResultRequest{
		JobID:     resp.JobID,
		TimeoutMS: 5000,
	})
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if full.Status != jobs.StatusCompleted {
		t.Errorf("status: %q want completed", full.Status)
	}
	if len(full.Added) != 2 {
		t.Errorf("added: got %d want 2", len(full.Added))
	}
	if full.Stats.AddedCount != 2 {
		t.Errorf("stats: %+v", full.Stats)
	}
}

// (TestCaptureBatchResultTimeout moved to
// capture_batch_review_test.go as TestCaptureBatchResultTimeoutDeterministic.
// The prior version was race-tolerant — when the runner won the race,
// the timeout path was never exercised, leaving the test
// non-deterministic. The new version uses blockingInjector to park the
// runner past the deadline so the timeout always fires.)

// TestCaptureBatchResultUnknownJob: ErrNotFound.
func TestCaptureBatchResultUnknownJob(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.CaptureBatchResult(context.Background(), CaptureBatchResultRequest{
		JobID:     "01HQQQQQQQQQQQQQQQQQQQQQQQ",
		TimeoutMS: 100,
	})
	if apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("expected not_found, got %v", apiErr)
	}
}

// TestCaptureBatchResultAfterTerminalReturnsImmediately: result on a
// completed job returns inline with no polling.
func TestCaptureBatchResultAfterTerminalReturnsImmediately(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Items: mustItems("done"),
	})
	start := time.Now()
	full, apiErr := a.CaptureBatchResult(context.Background(), CaptureBatchResultRequest{
		JobID:     resp.JobID,
		TimeoutMS: 30000,
	})
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	dur := time.Since(start)
	if dur > 100*time.Millisecond {
		t.Errorf("expected immediate return for terminal job, took %v", dur)
	}
	if full.Status != jobs.StatusCompleted {
		t.Errorf("status: %q", full.Status)
	}
}

// TestJobsListFilters runs across several status/kind/token filter
// combinations to confirm the projection is consistent.
func TestJobsListFilters(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	// Seed three completed sync jobs with distinct tokens.
	tok1 := "11111111-1111-1111-1111-111111111111"
	tok2 := "22222222-2222-2222-2222-222222222222"
	r1, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{ClientToken: tok1, Items: mustItems("a")})
	r2, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{ClientToken: tok2, Items: mustItems("b")})
	r3, _ := a.CaptureBatch(context.Background(), CaptureBatchRequest{Items: mustItems("c")})

	// All three should appear with status=completed.
	all, apiErr := a.JobsList(context.Background(), JobsListRequest{Status: jobs.StatusCompleted})
	if apiErr != nil {
		t.Fatalf("JobsList all: %v", apiErr)
	}
	if all.Total < 3 {
		t.Errorf("expected >=3 completed jobs, got %d", all.Total)
	}

	// Filter by token returns a single job. ClientToken is no longer
	// surfaced in JobSummary (multi-tenant safety); confirm by ID
	// instead.
	one, apiErr := a.JobsList(context.Background(), JobsListRequest{ClientToken: tok1})
	if apiErr != nil {
		t.Fatalf("JobsList token: %v", apiErr)
	}
	if one.Total != 1 || one.Jobs[0].ID != r1.JobID {
		t.Errorf("token filter: got %d jobs (%+v)", one.Total, one.Jobs)
	}

	// Filter by unknown kind returns empty.
	none, _ := a.JobsList(context.Background(), JobsListRequest{Kind: "f4_import"})
	if none.Total != 0 {
		t.Errorf("unknown-kind filter: got %d jobs", none.Total)
	}

	// All test fixtures shadowed.
	_ = r1
	_ = r2
	_ = r3
}

// TestJobsListPaginationCap: limit > MaxJobsListLimit rejected.
func TestJobsListPaginationCap(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.JobsList(context.Background(), JobsListRequest{Limit: MaxJobsListLimit + 1})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
}

// TestJobsListEmpty: empty store returns Jobs=[] (not nil) so MCP
// callers can iterate without nil-checks.
func TestJobsListEmpty(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.JobsList(context.Background(), JobsListRequest{})
	if apiErr != nil {
		t.Fatalf("JobsList: %v", apiErr)
	}
	if resp.Jobs == nil {
		t.Error("expected non-nil empty Jobs slice, got nil")
	}
	if resp.Total != 0 {
		t.Errorf("expected 0 total, got %d", resp.Total)
	}
}

// TestJobsListInvalidStatus: ErrInvalid.
func TestJobsListInvalidStatus(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.JobsList(context.Background(), JobsListRequest{Status: "weird"})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
}

// TestCaptureBatchAsyncCancelBeforeFirstChunk: cancel races with the
// runner; three outcomes are documented and acceptable:
//
//  1. Cancel beats the runner entirely -> Status=cancelled, 0 items.
//  2. Cancel lands mid-flight after a chunk has committed ->
//     Status=cancelled, N>0 items committed (chunked runner contract:
//     "finalize Job with whatever has committed so far"). On loaded
//     CI runners, the 2-item single-chunk batch frequently lands
//     before the cancel signal can short-circuit it; the in-store
//     items must equal the count Job.Result reports as added.
//  3. Cancel loses the race entirely -> Status=completed.
func TestCaptureBatchAsyncCancelBeforeFirstChunk(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	f := false
	resp, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("a", "b"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	c, _ := a.CaptureBatchCancel(context.Background(), CaptureBatchCancelRequest{JobID: resp.JobID})
	pollUntilTerminal(t, a, resp.JobID, 5*time.Second)
	// CaptureBatchCancel flips Status to cancelled but does NOT
	// populate Result; the runner does that asynchronously via
	// finalizeCancelledWithProgress. pollUntilTerminal returns when
	// Status is terminal, which can race ahead of the runner's
	// Result write. Wait for the runner to fully exit so Result is
	// stable before we read it. ShutdownAsync is idempotent
	// (t.Cleanup calls it again at test exit, which becomes a no-op).
	if err := a.ShutdownAsync(context.Background()); err != nil {
		t.Fatalf("ShutdownAsync: %v", err)
	}
	j, _ := a.engine.JobStore().Get(resp.JobID)
	switch j.Status {
	case jobs.StatusCancelled:
		eng.RLock()
		count := len(eng.Graph().AllNodeIDs())
		eng.RUnlock()
		// Items in the store must match what Result reports as added.
		// The chunked runner explicitly supports partial commits when
		// cancel arrives mid-flight; both 0 (early cancel won) and N>0
		// (cancel landed after a chunk commit) are valid, as long as
		// the store and Result agree.
		res, _ := a.CaptureBatchResult(context.Background(), CaptureBatchResultRequest{JobID: resp.JobID})
		if count != len(res.Added) {
			t.Errorf("store/result mismatch: store has %d items, Result.Added has %d",
				count, len(res.Added))
		}
	case jobs.StatusCompleted:
		// Race lost: runner finished before cancel landed. Acceptable.
		if !c.Cancelled && c.Status != jobs.StatusCompleted {
			t.Errorf("inconsistent: cancel returned status=%q but Job is completed", c.Status)
		}
	default:
		t.Errorf("unexpected terminal status %q", j.Status)
	}
}

// TestShutdownAsyncWaitsForRunners: ShutdownAsync blocks until
// in-flight runners exit; the caller can rely on this for ordered
// engine close.
func TestShutdownAsyncWaitsForRunners(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	f := false
	_, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("a", "b", "c"),
	})
	if apiErr != nil {
		t.Fatalf("CaptureBatch: %v", apiErr)
	}
	if err := a.ShutdownAsync(context.Background()); err != nil {
		t.Errorf("ShutdownAsync: %v", err)
	}
	// After shutdown, the registry must be empty.
	a.asyncMu.Lock()
	left := len(a.asyncRunners)
	a.asyncMu.Unlock()
	if left != 0 {
		t.Errorf("expected 0 runners after shutdown, got %d", left)
	}
}

// TestShutdownAsyncBlocksNewRunners: after ShutdownAsync, a new
// async submit returns ErrUnavailable instead of spawning.
func TestShutdownAsyncBlocksNewRunners(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	if err := a.ShutdownAsync(context.Background()); err != nil {
		t.Fatalf("ShutdownAsync: %v", err)
	}
	f := false
	_, apiErr := a.CaptureBatch(context.Background(), CaptureBatchRequest{
		Wait:  &f,
		Items: mustItems("x"),
	})
	if apiErr == nil || apiErr.Code != "unavailable" {
		t.Fatalf("expected unavailable, got %v", apiErr)
	}
}

// (TestCaptureBatchAsyncRestartRecovery removed as vacuous — its
// only assertion was `j.Kind == capture_batch` which doesn't test
// recovery at all. Restart-recovery semantics are exercised in
// core/jobstore_wiring_test.go for both sync- and async-created jobs;
// the api-level path uses the same JobStore so recovery applies
// uniformly.)
