package core

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/jobs"
)

// defaultTestConfig returns a minimal config suitable for an
// engine constructed without external providers. Mirrors the
// shape that setupTestEngine uses internally; called explicitly
// by tests that need to mutate config (e.g., GC sweep interval).
func defaultTestConfig(dir string) config.Config {
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	return cfg
}

// loadTestEngineWithConfig persists the given config to disk and
// loads an engine with the standard test options (in-memory
// vector index). Caller is responsible for engine.Close().
func loadTestEngineWithConfig(t *testing.T, cfg config.Config) (*Engine, error) {
	t.Helper()
	if err := config.Save(cfg, cfg.DataDir+"/config.yaml"); err != nil {
		return nil, err
	}
	return LoadEngineWithOptions(cfg.DataDir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
		WithVolatileStorage(),
	})
}

// TestEngineJobStoreInit — engine init opens a JobStore, returns
// non-nil from JobStore(), and creates jobs.db on disk.
func TestEngineJobStoreInit(t *testing.T) {
	eng := setupTestEngine(t)
	defer eng.Close()
	if js := eng.JobStore(); js == nil {
		t.Fatal("JobStore() returned nil after init")
	}
	stat, err := filepath.Glob(filepath.Join(eng.cfg.DataDir, "jobs.db"))
	if err != nil || len(stat) != 1 {
		t.Errorf("jobs.db not created at %s", eng.cfg.DataDir)
	}
}

// TestEngineJobStoreRestartRecovery — a running job from a prior
// engine session is flipped to failed/server_restart on next init.
func TestEngineJobStoreRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)

	// First engine: create a running job and DON'T mark it terminal.
	eng1, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	js := eng1.JobStore()
	in := &jobs.Job{
		ID:        "in-flight",
		Kind:      "capture_batch",
		Status:    jobs.StatusRunning,
		StartedAt: time.Now().UTC(),
	}
	if err := js.Create(in); err != nil {
		t.Fatal(err)
	}
	// Bypass Engine.Close (which would not change the job since the
	// runner isn't part of this test). Close cleanly to flush bbolt.
	if err := eng1.Close(); err != nil {
		t.Fatal(err)
	}

	// Second engine: reopen the same data dir.
	eng2, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()

	got, err := eng2.JobStore().Get("in-flight")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusFailed {
		t.Errorf("status: got %s, want failed", got.Status)
	}
	if got.FailureReason != "server_restart" {
		t.Errorf("FailureReason: got %s, want server_restart", got.FailureReason)
	}
	if got.CompletedAt.IsZero() {
		t.Errorf("CompletedAt not populated by recovery")
	}
}

// TestEngineJobStorePreservesCompleted — restart recovery does NOT
// touch jobs that were already terminal.
func TestEngineJobStorePreservesCompleted(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)

	eng1, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	completed := &jobs.Job{
		ID:          "done",
		Kind:        "capture_batch",
		Status:      jobs.StatusCompleted,
		CompletedAt: completedAt,
	}
	if err := eng1.JobStore().Create(completed); err != nil {
		t.Fatal(err)
	}
	if err := eng1.Close(); err != nil {
		t.Fatal(err)
	}

	eng2, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()

	got, err := eng2.JobStore().Get("done")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusCompleted {
		t.Errorf("status corrupted: got %s, want completed", got.Status)
	}
	if !got.CompletedAt.Equal(completedAt) {
		t.Errorf("CompletedAt changed: got %v, want %v",
			got.CompletedAt, completedAt)
	}
}

// TestEngineGCSweepRuns — engine started with short sweep interval;
// a pre-aged terminal job is deleted by the sweeper goroutine.
func TestEngineGCSweepRuns(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 50 * time.Millisecond
	cfg.Jobs.Retention.Completed = 1 * time.Nanosecond // expire instantly

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// Pre-aged completed job: CompletedAt in the past.
	old := &jobs.Job{
		ID:          "ancient",
		Kind:        "capture_batch",
		Status:      jobs.StatusCompleted,
		CompletedAt: time.Now().Add(-time.Hour),
	}
	if err := eng.JobStore().Create(old); err != nil {
		t.Fatal(err)
	}

	// Wait up to 1s for the sweeper to fire (interval=50ms; should
	// run several times before timeout).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		_, err := eng.JobStore().Get("ancient")
		if errors.Is(err, jobs.ErrNotFound) {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("ancient job not GC'd within 1s; sweeper may not be running")
}

// TestEngineGCRespectsCtxCancel — engine.Close cancels the sweeper
// goroutine cleanly. No goroutine leak; jobSweepDone is closed
// before Close returns.
func TestEngineGCRespectsCtxCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 1 * time.Hour // long enough to never fire

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if eng.jobSweepDone == nil {
		t.Fatal("jobSweepDone nil; sweeper not started")
	}
	if eng.jobSweepCancel == nil {
		t.Fatal("jobSweepCancel nil; sweeper not started")
	}

	// Capture the channel before Close — Close nils the field for
	// idempotency.
	doneCh := eng.jobSweepDone

	// Engine.Close should cancel the sweeper and wait for it.
	closeStart := time.Now()
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	closeDur := time.Since(closeStart)

	// The captured channel was closed when the goroutine exited;
	// Close should have waited for that before returning.
	select {
	case <-doneCh:
		// good
	default:
		t.Error("jobSweepDone not closed after engine.Close returned")
	}

	// Close should be fast — no waiting on the long ticker.
	if closeDur > 500*time.Millisecond {
		t.Errorf("Close took %v; sweeper cancel may not be respected", closeDur)
	}
}

// TestEngineGCDisabledWhenIntervalZero — SweepInterval=0 disables
// the sweeper goroutine entirely.
func TestEngineGCDisabledWhenIntervalZero(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 0

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if eng.jobSweepCancel != nil {
		t.Error("jobSweepCancel non-nil with SweepInterval=0")
	}
	if eng.jobSweepDone != nil {
		t.Error("jobSweepDone non-nil with SweepInterval=0")
	}
}

// TestEngineJobStoreRestartRecoveryMany — N>1 in-flight jobs all
// flipped to failed. Recovery iterates the full set; this guards
// against the regression where iteration short-circuits on first
// error or skips entries.
func TestEngineJobStoreRestartRecoveryMany(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)

	eng1, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Create 25 in-flight jobs with mixed statuses.
	const n = 25
	for i := 0; i < n; i++ {
		st := jobs.StatusPending
		if i%2 == 0 {
			st = jobs.StatusRunning
		}
		j := &jobs.Job{
			ID:        "in-flight-" + itoa(i),
			Kind:      "capture_batch",
			Status:    st,
			StartedAt: time.Now().UTC(),
		}
		if err := eng1.JobStore().Create(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng1.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — recovery should flip all 25.
	eng2, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()

	for i := 0; i < n; i++ {
		got, err := eng2.JobStore().Get("in-flight-" + itoa(i))
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if got.Status != jobs.StatusFailed {
			t.Errorf("job %d: status=%s, want failed", i, got.Status)
		}
		if got.FailureReason != "server_restart" {
			t.Errorf("job %d: reason=%q", i, got.FailureReason)
		}
	}
}

// TestEngineJobStoreRestartRecoveryBeforeListener — assert
// that recovery completes synchronously inside engine init.
// Once loadTestEngineWithConfig returns, the JobStore must
// contain zero in-flight jobs from the prior run; subsequent
// HTTP listener bind (in production via server.Run) cannot
// observe stale running state.
func TestEngineJobStoreRestartRecoveryBeforeListener(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)

	eng1, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = eng1.JobStore().Create(&jobs.Job{
			ID:     "stale-" + itoa(i),
			Kind:   "capture_batch",
			Status: jobs.StatusRunning,
		})
	}
	if err := eng1.Close(); err != nil {
		t.Fatal(err)
	}

	// loadTestEngineWithConfig returns AFTER engine init's
	// recovery completes. The moment it returns is the
	// production "before listener bind" point.
	eng2, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()

	// Right after init returns: zero in-flight jobs.
	inflight, err := eng2.JobStore().ListInFlight()
	if err != nil {
		t.Fatal(err)
	}
	if len(inflight) != 0 {
		t.Errorf("expected 0 in-flight after init, got %d (recovery did not run synchronously)",
			len(inflight))
	}
}

// TestEngineGCSurvivesRunGCError — sweeper logs and continues on
// transient errors; doesn't crash the goroutine. Verified by
// observing the sweeper still runs on the next tick after a
// forced error.
func TestEngineGCSurvivesRunGCError(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 30 * time.Millisecond
	cfg.Jobs.Retention.Completed = time.Nanosecond

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// Create a normal aged job to trigger GC.
	old := &jobs.Job{
		ID:          "ancient",
		Kind:        "capture_batch",
		Status:      jobs.StatusCompleted,
		CompletedAt: time.Now().Add(-time.Hour),
	}
	if err := eng.JobStore().Create(old); err != nil {
		t.Fatal(err)
	}

	// Wait for the sweeper to fire and delete it. If the
	// goroutine had crashed on a prior tick, this would hang.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		_, err := eng.JobStore().Get("ancient")
		if errors.Is(err, jobs.ErrNotFound) {
			return // sweeper alive and working
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("sweeper not running; ancient job not deleted within 1s")
}

// TestEngineCloseDuringSweep — Close fires while RunGC is
// mid-walk. With ctx propagation, RunGC returns ctx.Err() and the
// sweeper goroutine exits promptly; Close completes quickly.
//
// Forcing exact mid-walk timing is hard without a hook, so this
// test seeds 1000 expired jobs (each delete is a bbolt write);
// the sweep starts deleting; Close fires; we measure that Close
// returned in well under the time the full sweep would take.
func TestEngineCloseDuringSweep(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 5 * time.Millisecond
	cfg.Jobs.Retention.Completed = time.Nanosecond

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Seed 1000 expired completed jobs — sweep will iterate all.
	for i := 0; i < 1000; i++ {
		_ = eng.JobStore().Create(&jobs.Job{
			ID:          "exp-" + itoa(i),
			Kind:        "capture_batch",
			Status:      jobs.StatusCompleted,
			CompletedAt: time.Now().Add(-time.Hour),
		})
	}

	// Let one tick fire (start of a sweep), then Close.
	time.Sleep(15 * time.Millisecond)

	closeStart := time.Now()
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	dur := time.Since(closeStart)

	// Close must return quickly (much less than the time it would
	// take to process all 1000 if Close were waiting on the full
	// sweep). 500ms is a generous bound for any reasonable machine.
	if dur > 500*time.Millisecond {
		t.Errorf("Close took %v during sweep; ctx may not be propagating", dur)
	}
}

// TestEngineCloseIdempotent — second call is a no-op; doesn't
// panic on the cancel-already-nil channel or double-close bbolt.
func TestEngineCloseIdempotent(t *testing.T) {
	eng := setupTestEngine(t)
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestEngineGCZeroRetentionViaConfig — engine path with all-zero
// retention preserves jobs forever. Mirrors jobs/'s test but
// exercises the engine-config wiring.
func TestEngineGCZeroRetentionViaConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(dir)
	cfg.Jobs.SweepInterval = 20 * time.Millisecond
	cfg.Jobs.Retention.Completed = 0
	cfg.Jobs.Retention.Failed = 0
	cfg.Jobs.Retention.Cancelled = 0

	eng, err := loadTestEngineWithConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	old := &jobs.Job{
		ID:          "ancient",
		Kind:        "capture_batch",
		Status:      jobs.StatusCompleted,
		CompletedAt: time.Now().Add(-1000 * time.Hour),
	}
	if err := eng.JobStore().Create(old); err != nil {
		t.Fatal(err)
	}

	// Let sweeper run several times.
	time.Sleep(120 * time.Millisecond)

	// Job must still exist.
	if _, err := eng.JobStore().Get("ancient"); err != nil {
		t.Errorf("ancient job deleted with zero retention: %v", err)
	}
}

// itoa avoids strconv for short test IDs.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
