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

	// Engine.Close should cancel the sweeper and wait for it.
	closeStart := time.Now()
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	closeDur := time.Since(closeStart)

	// jobSweepDone closes when the goroutine exits — Close should
	// have waited for it before returning.
	select {
	case <-eng.jobSweepDone:
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
