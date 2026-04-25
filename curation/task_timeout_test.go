package curation

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunTaskWithTimeoutCancelsHungTask is the load-bearing
// regression for P2-08 fix #1: a single hung task must NOT prevent
// downstream tasks from running. The pre-fix runner called each
// task under the parent ctx, so a stuck LLM call (HTTP 120s
// timeout) consumed the entire 1-minute curation cycle and
// downstream tasks (summaries, concepts, contradictions) silently
// never ran.
//
// Post-fix: runTaskWithTimeout wraps each task in a sub-context
// that expires after `timeout`. When the timeout fires, fn's ctx
// is cancelled — fn observes that and returns; the next task
// starts with a fresh sub-context.
func TestRunTaskWithTimeoutCancelsHungTask(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	hangStarted := make(chan struct{})
	var taskCancelled int32

	start := time.Now()
	runTaskWithTimeout(context.Background(), "classify", 50*time.Millisecond, logger,
		func(c context.Context) {
			close(hangStarted)
			<-c.Done() // task observes its sub-ctx and returns
			atomic.StoreInt32(&taskCancelled, 1)
		})
	elapsed := time.Since(start)

	select {
	case <-hangStarted:
	default:
		t.Fatal("task fn never started")
	}

	if atomic.LoadInt32(&taskCancelled) != 1 {
		t.Errorf("task fn did not observe ctx cancellation")
	}
	// Generous upper bound to absorb goroutine startup latency on
	// slow CI under -race; the load-bearing assertion is "this
	// returned at all", not "it returned at exactly 50ms". A 1s
	// ceiling is still 20x the timeout, so a regression that
	// genuinely doesn't cancel (e.g. a future bug that drops the
	// WithTimeout) would still fail this test on any reasonable
	// machine.
	if elapsed > time.Second {
		t.Errorf("runTaskWithTimeout took %v, expected well under 1s for a 50ms timeout (cancellation regressed?)", elapsed)
	}
	if !strings.Contains(buf.String(), `task=classify`) ||
		!strings.Contains(buf.String(), `per-task timeout`) {
		t.Errorf("expected timeout log with task=classify; got:\n%s", buf.String())
	}
}

// TestRunTaskWithTimeoutCompletesNormally pins the no-timeout path:
// a task that finishes well under the timeout shouldn't pay any
// extra cost and shouldn't log a timeout warning.
func TestRunTaskWithTimeoutCompletesNormally(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	var ran int32

	runTaskWithTimeout(context.Background(), "summarize", 5*time.Second, logger,
		func(c context.Context) {
			atomic.StoreInt32(&ran, 1)
		})

	if atomic.LoadInt32(&ran) != 1 {
		t.Errorf("task fn did not run")
	}
	if strings.Contains(buf.String(), "per-task timeout") {
		t.Errorf("a fast-completing task should not log a timeout; got:\n%s", buf.String())
	}
}

// TestRunTaskWithTimeoutZeroDisablesTimeout pins the legacy-behavior
// fallback: timeout=0 (or negative) bypasses the wrapper entirely
// and runs fn under the parent ctx. Useful for users who want the
// pre-fix sequential-blocking semantics.
func TestRunTaskWithTimeoutZeroDisablesTimeout(t *testing.T) {
	logger := slog.Default()

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var receivedCtx context.Context
	runTaskWithTimeout(parentCtx, "concept", 0, logger,
		func(c context.Context) {
			receivedCtx = c
		})

	if receivedCtx != parentCtx {
		t.Errorf("with timeout=0, fn should receive the parent ctx unchanged")
	}
}

// TestRunTaskWithTimeoutBailsOnCancelledParent pins that a cancelled
// parent ctx (server shutdown / cycle cancellation) skips fn entirely
// — no per-task setup cost paid for the remaining N tasks in the
// cycle when the cycle has already been told to stop.
func TestRunTaskWithTimeoutBailsOnCancelledParent(t *testing.T) {
	logger := slog.Default()

	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the call

	var ran int32

	runTaskWithTimeout(parent, "classify", 5*time.Second, logger,
		func(c context.Context) {
			atomic.StoreInt32(&ran, 1)
		})

	if atomic.LoadInt32(&ran) != 0 {
		t.Errorf("fn should NOT run when parent ctx is already cancelled")
	}
}

// TestRunTaskWithTimeoutNextTaskGetsFreshCtx pins the per-task
// isolation invariant: even after one task hits its timeout,
// subsequent tasks receive a non-cancelled ctx and run normally.
// This is the user-visible payoff of the fix.
func TestRunTaskWithTimeoutNextTaskGetsFreshCtx(t *testing.T) {
	logger := slog.Default()

	parent := context.Background()

	// First task hangs and is cancelled.
	runTaskWithTimeout(parent, "classify", 30*time.Millisecond, logger,
		func(c context.Context) {
			<-c.Done()
		})

	// Second task observes a NON-cancelled sub-context.
	var secondCancelled bool
	runTaskWithTimeout(parent, "summarize", 100*time.Millisecond, logger,
		func(c context.Context) {
			select {
			case <-c.Done():
				secondCancelled = true
			default:
				// expected: fresh, non-cancelled ctx
			}
		})

	if secondCancelled {
		t.Errorf("second task's sub-ctx leaked cancellation from the first task")
	}
}
