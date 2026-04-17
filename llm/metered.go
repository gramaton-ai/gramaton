package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// ErrCapped is returned when the caller's UsageTracker is paused
// (daily or session call cap exceeded). The cap is enforced BEFORE
// the inner provider is invoked, so no tokens are consumed and no
// telemetry record is created. Callers that want to ignore the cap
// should not wrap their provider in Metered (or should call the
// inner provider directly).
//
// Use errors.Is(err, llm.ErrCapped) to detect.
var ErrCapped = errors.New("llm call cap reached")

// Metered wraps a Provider and records usage metrics + emits a
// per-call log on every Complete/CompleteWithModel. It:
//
//   - Reads the task label from the incoming context (set by callers
//     via telemetry.WithTask). Empty task is recorded as "unknown".
//   - Ensures a telemetry.UsageRecorder is attached to the context so
//     the inner provider (Anthropic) can report token counts. If the
//     caller attached their own recorder (for cycle-level aggregation),
//     Metered records into that one and reads its delta per call.
//   - Looks up model pricing via LookupPricing and computes a
//     per-call USD cost.
//   - Emits one Info log per call with model, task, token counts,
//     cache counts, latency, cost.
//   - Records to the UsageTracker for cap enforcement + summary.
type Metered struct {
	inner   Provider
	tracker *UsageTracker
	logger  *slog.Logger
}

// NewMetered wraps a provider with usage tracking and per-call logging.
// logger may be nil; in that case no per-call log is emitted (metrics
// still go to the tracker).
func NewMetered(inner Provider, tracker *UsageTracker, logger *slog.Logger) *Metered {
	return &Metered{inner: inner, tracker: tracker, logger: logger}
}

func (m *Metered) Complete(ctx context.Context, prompt string) (string, error) {
	if err := m.checkCap(); err != nil {
		return "", err
	}
	ctx, capture := m.ensureRecorder(ctx)
	task := telemetry.TaskFromContext(ctx)

	start := time.Now()
	resp, err := m.inner.Complete(ctx, prompt)
	latency := time.Since(start)

	delta := capture()
	m.record(m.inner.ModelID(), task, delta, latency, err)
	return resp, err
}

func (m *Metered) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if err := m.checkCap(); err != nil {
		return "", err
	}
	ctx, capture := m.ensureRecorder(ctx)
	task := telemetry.TaskFromContext(ctx)

	start := time.Now()
	resp, err := m.inner.CompleteWithModel(ctx, model, prompt)
	latency := time.Since(start)

	usedModel := model
	if usedModel == "" {
		usedModel = m.inner.ModelID()
	}
	delta := capture()
	m.record(usedModel, task, delta, latency, err)
	return resp, err
}

// checkCap returns ErrCapped (wrapped with the pause reason) if the
// tracker reports the caller is over its daily or session cap. A nil
// tracker means caps are not enforced.
func (m *Metered) checkCap() error {
	if m.tracker == nil {
		return nil
	}
	if paused, reason := m.tracker.IsPaused(); paused {
		if m.logger != nil {
			m.logger.Warn("llm call refused: cap reached",
				"component", "llm",
				"reason", reason)
		}
		return fmt.Errorf("%w: %s", ErrCapped, reason)
	}
	return nil
}

func (m *Metered) ModelID() string { return m.inner.ModelID() }

// Inner returns the wrapped provider for type assertions.
func (m *Metered) Inner() Provider { return m.inner }

// SetSystemPrompt delegates to the inner provider if it supports it.
func (m *Metered) SetSystemPrompt(text string) {
	if setter, ok := m.inner.(SystemPromptSetter); ok {
		setter.SetSystemPrompt(text)
	}
}

// ensureRecorder attaches a UsageRecorder to ctx if one isn't already
// present, and returns a closure that yields the per-call delta when
// invoked. The delta is computed against the pre-call snapshot of the
// recorder, which lets Metered work correctly whether the caller
// supplied its own cycle-scoped recorder (totals accumulate there) or
// didn't (Metered uses a scratch recorder).
func (m *Metered) ensureRecorder(ctx context.Context) (context.Context, func() telemetry.CallUsage) {
	recorder := telemetry.RecorderFromContext(ctx)
	if recorder == nil {
		recorder = &telemetry.UsageRecorder{}
		ctx = telemetry.WithUsageRecorder(ctx, recorder)
	}
	before := recorder.Total()
	return ctx, func() telemetry.CallUsage {
		return recorder.Total().Sub(before)
	}
}

func (m *Metered) record(model, task string, usage telemetry.CallUsage, latency time.Duration, err error) {
	if task == "" {
		task = "unknown"
	}
	cost := EstimateCost(model, usage)
	success := err == nil
	errType := ""
	if err != nil {
		errType = "error"
	}

	metrics := CallMetrics{
		Provider:         "metered",
		Model:            model,
		Task:             task,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		CostUSD:          cost,
		LatencyMs:        latency.Milliseconds(),
		Success:          success,
		ErrorType:        errType,
	}

	if m.tracker != nil {
		m.tracker.Record(metrics)
	}

	if m.logger != nil {
		args := []any{
			"component", "llm",
			"model", model,
			"task", task,
			"input_tokens", metrics.InputTokens,
			"output_tokens", metrics.OutputTokens,
			"cache_read", metrics.CacheReadTokens,
			"cache_write", metrics.CacheWriteTokens,
			"latency_ms", metrics.LatencyMs,
			"cost_usd", cost,
			"success", success,
		}
		if !success {
			args = append(args, "error", errType)
			m.logger.Warn("llm call", args...)
		} else {
			m.logger.Info("llm call", args...)
		}
	}
}
