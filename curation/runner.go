package curation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
)

// Runner manages the background curation goroutine.
type Runner struct {
	engine        *core.Engine
	llm           llm.Provider // may be nil
	cfg           config.Config
	state         *State
	logger        *slog.Logger
	stopCh        chan struct{}
	done          chan struct{}
	postCycleHook func()
}

// State holds curation results for the response envelope. It has its
// own mutex, separate from the engine's RWMutex, to prevent deadlock
// when computing curation status during response writing.
type State struct {
	mu                sync.Mutex
	inProgress        bool
	LastRun           time.Time
	LastDeterministic *DeterministicResult
	LastAutonomous    *AutonomousResult

	// Circuit breaker: pause LLM curation after consecutive
	// high-error cycles to avoid burning credits on persistent
	// failures (billing errors, auth failures, etc.).
	ConsecutiveErrorCycles int
	LLMPaused              bool
	LLMPauseReason         string
	LLMPausedAt            time.Time

	// Manifest LLM cache: the fingerprint of the last store stats that
	// produced a qualitative summary, and the summary itself. When a
	// subsequent cycle's fingerprint matches, the cached summary is
	// reused without firing another LLM call. In-memory only; the store
	// regenerates on restart after one LLM call.
	LastManifestHash    string
	LastManifestSummary string
}

// EnhancedStatus is the curation info included in the response envelope.
type EnhancedStatus struct {
	PendingCount      int        `json:"pending_count"`
	Overdue           bool       `json:"overdue"`
	ConceptCandidates int        `json:"concept_candidates,omitempty"`
	StaleCount        int        `json:"stale_count,omitempty"`
	OrphanCount       int        `json:"orphan_count,omitempty"`
	LastCurated       *time.Time `json:"last_curated,omitempty"`
	Autonomous        bool       `json:"autonomous"`
	LLMPaused         bool       `json:"llm_paused,omitempty"`
	LLMPauseReason    string     `json:"llm_pause_reason,omitempty"`
}

// NewRunner creates a curation runner.
func NewRunner(engine *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *Runner {
	return &Runner{
		engine: engine,
		llm:    llmProv,
		cfg:    cfg,
		state:  &State{},
		logger: logger,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start launches the curation goroutine. Blocks until Stop is called
// or ctx is cancelled.
func (r *Runner) Start(ctx context.Context) {
	defer close(r.done)

	interval := r.cfg.Curation.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup.
	r.runIfIdle(ctx)

	for {
		select {
		case <-ticker.C:
			r.runIfIdle(ctx)
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop signals the runner to stop and waits for it to finish.
func (r *Runner) Stop() {
	close(r.stopCh)
	<-r.done
}

// Status returns the current curation status for the response envelope.
// Safe to call from any goroutine -- uses its own mutex, not the
// engine's RWMutex.
func (r *Runner) Status() EnhancedStatus {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()

	status := EnhancedStatus{
		Autonomous: r.llm != nil,
	}

	if r.state.LastDeterministic != nil && r.state.LastDeterministic.Manifest != nil {
		m := r.state.LastDeterministic.Manifest
		status.PendingCount = m.PendingCount
		status.Overdue = m.PendingCount > 0
		status.StaleCount = m.StaleCount
		status.OrphanCount = m.OrphanCount
		status.ConceptCandidates = len(r.state.LastDeterministic.ConceptCandidates)
	}

	if !r.state.LastRun.IsZero() {
		t := r.state.LastRun
		status.LastCurated = &t
	}

	status.LLMPaused = r.state.LLMPaused
	status.LLMPauseReason = r.state.LLMPauseReason

	return status
}

// ConceptCandidates returns the current concept candidates.
func (r *Runner) ConceptCandidates() []ConceptCandidate {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.LastDeterministic == nil {
		return nil
	}
	return r.state.LastDeterministic.ConceptCandidates
}

// Manifest returns the current store manifest.
func (r *Runner) Manifest() *StoreManifest {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.LastDeterministic == nil {
		return nil
	}
	return r.state.LastDeterministic.Manifest
}

// SetPostCycleHook registers a function to call after each curation
// cycle. Used by the server to trigger auto-backup.
func (r *Runner) SetPostCycleHook(hook func()) {
	r.postCycleHook = hook
}

// Trigger runs a curation cycle immediately. Returns false if a
// cycle is already in progress. Resets the circuit breaker so that
// manual triggers always attempt LLM work.
func (r *Runner) Trigger(ctx context.Context) bool {
	r.state.mu.Lock()
	if r.state.inProgress {
		r.state.mu.Unlock()
		return false
	}
	r.state.inProgress = true
	// Reset circuit breaker on manual trigger.
	r.state.LLMPaused = false
	r.state.LLMPauseReason = ""
	r.state.ConsecutiveErrorCycles = 0
	r.state.mu.Unlock()

	defer func() {
		r.state.mu.Lock()
		r.state.inProgress = false
		r.state.mu.Unlock()
	}()

	r.cycle(ctx)
	return true
}

// TriggerDryRun runs the autonomous curation pipeline without applying
// changes. Returns the planned changes that would be made. Deterministic
// curation still runs normally (it's already safe). Only autonomous
// (LLM-driven) changes are dry-run.
func (r *Runner) TriggerDryRun(ctx context.Context) *AutonomousResult {
	if r.llm == nil {
		return &AutonomousResult{DryRun: true}
	}

	cycleCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	return RunAutonomousDryRun(cycleCtx, r.engine, r.llm, r.cfg, r.logger)
}

func (r *Runner) runIfIdle(ctx context.Context) {
	r.state.mu.Lock()
	if r.state.inProgress {
		r.state.mu.Unlock()
		return
	}
	r.state.inProgress = true
	r.state.mu.Unlock()

	defer func() {
		r.state.mu.Lock()
		r.state.inProgress = false
		r.state.mu.Unlock()
	}()

	r.cycle(ctx)
}

func (r *Runner) cycle(ctx context.Context) {
	// Per-cycle timeout to prevent hangs.
	cycleCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_ = cycleCtx // used by autonomous below

	// 1. Always run deterministic curation.
	result := RunDeterministic(r.engine, r.cfg, r.logger)

	r.state.mu.Lock()
	r.state.LastDeterministic = result
	r.state.LastRun = time.Now().UTC()
	r.state.mu.Unlock()

	// 2. Run LLM curation if configured and there's work to do.
	if r.llm != nil {
		// Check circuit breaker.
		r.state.mu.Lock()
		llmPaused := r.state.LLMPaused
		// Auto-reset circuit breaker after 30 minutes.
		if llmPaused && !r.state.LLMPausedAt.IsZero() && time.Since(r.state.LLMPausedAt) > 30*time.Minute {
			r.state.LLMPaused = false
			r.state.LLMPauseReason = ""
			r.state.ConsecutiveErrorCycles = 0
			llmPaused = false
			r.logger.Info("circuit breaker auto-reset after 30m", "component", "curation")
		}
		r.state.mu.Unlock()
		if llmPaused {
			// Operators want to see this without enabling Debug --
			// the breaker tripping is the whole reason curation
			// is silent on this cycle. (Wave 7 P1-63.)
			r.logger.Info("LLM curation paused (circuit breaker)",
				"component", "curation")
			// Reset the LastAutonomous counters so /v1/status
			// doesn't keep showing stale "last cycle did N
			// classifications" while the breaker is engaged --
			// this cycle did zero LLM work.
			r.state.mu.Lock()
			r.state.LastAutonomous = &AutonomousResult{
				LastRunPaused: true,
				PauseReason:   r.state.LLMPauseReason,
			}
			r.state.mu.Unlock()
		}

		hasPending := result.Manifest != nil && result.Manifest.PendingCount > 0
		hasCandidates := len(result.ConceptCandidates) > 0
		needsSummary := result.Manifest != nil && result.Manifest.QualitativeSummary == ""
		if !llmPaused && (hasPending || hasCandidates || needsSummary) {
			// Snapshot the manifest cache under lock so the autonomous
			// run sees a consistent hash/summary pair. The cache struct
			// is mutated in place by generateManifestSummary; copy back
			// under lock after the run.
			r.state.mu.Lock()
			mcache := ManifestCache{
				Hash:    r.state.LastManifestHash,
				Summary: r.state.LastManifestSummary,
			}
			r.state.mu.Unlock()

			aResult := RunAutonomous(cycleCtx, r.engine, r.llm, r.cfg, &mcache, r.logger)
			r.state.mu.Lock()
			r.state.LastAutonomous = aResult
			r.state.LastManifestHash = mcache.Hash
			r.state.LastManifestSummary = mcache.Summary

			// Circuit breaker: if >80% of LLM calls errored, count as
			// an error cycle. After 3 consecutive error cycles, pause.
			if aResult.LLMCalls > 0 {
				errorRate := float64(aResult.Errors) / float64(aResult.LLMCalls)
				if errorRate > 0.8 {
					r.state.ConsecutiveErrorCycles++
					if r.state.ConsecutiveErrorCycles >= 3 {
						r.state.LLMPaused = true
						r.state.LLMPausedAt = time.Now()
						r.state.LLMPauseReason = fmt.Sprintf("circuit breaker: %d consecutive high-error cycles (last: %d/%d errors)",
							r.state.ConsecutiveErrorCycles, aResult.Errors, aResult.LLMCalls)
						r.logger.Warn("LLM curation paused by circuit breaker",
							"component", "curation",
							"consecutive_error_cycles", r.state.ConsecutiveErrorCycles,
							"last_errors", aResult.Errors,
							"last_calls", aResult.LLMCalls)
					}
				} else {
					r.state.ConsecutiveErrorCycles = 0
				}
			}

			// Refresh pending count after autonomous classification.
			if aResult.Classified > 0 && r.state.LastDeterministic != nil && r.state.LastDeterministic.Manifest != nil {
				r.engine.RLock()
				captured := r.engine.PropIdx().Lookup("processing_status",
					graph.StringProperty("captured"))
				r.state.LastDeterministic.Manifest.PendingCount = len(captured)
				r.engine.RUnlock()
			}
			// Apply manifest qualitative summary.
			if aResult.ManifestSummary != "" && r.state.LastDeterministic != nil && r.state.LastDeterministic.Manifest != nil {
				r.state.LastDeterministic.Manifest.QualitativeSummary = aResult.ManifestSummary
			}
			r.state.mu.Unlock()
		}
	}

	// 3. Run post-cycle hook (auto-backup).
	if r.postCycleHook != nil {
		r.postCycleHook()
	}
}
