package curation

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/llm"
)

// Runner manages the background curation goroutine.
type Runner struct {
	engine *core.Engine
	llm    llm.Provider // may be nil
	cfg    config.Config
	state  *State
	logger *slog.Logger
	stopCh chan struct{}
	done   chan struct{}
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

// Trigger runs a curation cycle immediately. Returns false if a
// cycle is already in progress.
func (r *Runner) Trigger(ctx context.Context) bool {
	r.state.mu.Lock()
	if r.state.inProgress {
		r.state.mu.Unlock()
		return false
	}
	r.state.inProgress = true
	r.state.mu.Unlock()

	defer func() {
		r.state.mu.Lock()
		r.state.inProgress = false
		r.state.mu.Unlock()
	}()

	r.cycle(ctx)
	return true
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
		hasPending := result.Manifest != nil && result.Manifest.PendingCount > 0
		hasCandidates := len(result.ConceptCandidates) > 0
		if hasPending || hasCandidates {
			aResult := RunAutonomous(cycleCtx, r.engine, r.llm, r.cfg, r.logger)
			r.state.mu.Lock()
			r.state.LastAutonomous = aResult
			// Refresh pending count after autonomous classification.
			if aResult.Classified > 0 && r.state.LastDeterministic != nil && r.state.LastDeterministic.Manifest != nil {
				r.engine.RLock()
				captured := r.engine.PropIdx().Lookup("processing_status",
					graph.StringProperty("captured"))
				r.state.LastDeterministic.Manifest.PendingCount = len(captured)
				r.engine.RUnlock()
			}
			r.state.mu.Unlock()
		}
	}
}
