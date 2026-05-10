package llm

import (
	"context"
	"encoding/json"
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

// ProviderName delegates to the wrapped provider so CallMetrics.Provider
// reflects the real backend (e.g. "anthropic") instead of the wrapper.
func (m *Metered) ProviderName() string { return m.inner.ProviderName() }

// Inner returns the wrapped provider for type assertions.
func (m *Metered) Inner() Provider { return m.inner }

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

// RecordCall accounts for an LLM call that bypassed the normal
// Complete / CompleteWithModel path -- most notably the Anthropic
// Message Batches API, which submits via SubmitBatch and gets
// results back out-of-band. Callers translate the provider-native
// response into a telemetry.CallUsage and pass it here so the
// bypassed call is counted identically: per-call log, tracker
// record, cap enforcement next call.
//
// latency may be zero when the batch API doesn't report per-sub-
// request timing; the tracker tolerates it.
func (m *Metered) RecordCall(model, task string, usage telemetry.CallUsage, latency time.Duration, err error) {
	m.record(model, task, usage, latency, err)
}

// SupportsStructuredOutput delegates to the inner provider's
// capability so callers can route structured-output calls correctly
// through the Metered wrapper without knowing about it.
func (m *Metered) SupportsStructuredOutput() bool {
	return m.inner.SupportsStructuredOutput()
}

// CompleteStructured is the schema-enforced counterpart to Complete.
// Wraps the inner provider's structured call with the same tracking
// envelope (cap check, task label, recorder attach, tracker record)
// so structured calls reconcile alongside plain calls in
// llm_usage.json.
func (m *Metered) CompleteStructured(ctx context.Context, schema map[string]any, prompt string) (json.RawMessage, error) {
	if err := m.checkCap(); err != nil {
		return nil, err
	}
	ctx, capture := m.ensureRecorder(ctx)
	task := telemetry.TaskFromContext(ctx)

	start := time.Now()
	raw, err := m.inner.CompleteStructured(ctx, schema, prompt)
	latency := time.Since(start)

	delta := capture()
	m.record(m.inner.ModelID(), task, delta, latency, err)
	return raw, err
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
		Provider:         m.inner.ProviderName(),
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
			// Failures stay at Warn -- those are operationally
			// interesting (rate limits, auth errors, timeouts).
			args = append(args, "error", errType)
			m.logger.Warn("llm call", args...)
		} else {
			// Successes drop to Debug. Per-call success logs at Info
			// produce 20-30k log lines per curation cycle on a 10k
			// record store, dominating log volume. Aggregate cycle
			// totals are reported separately by the curation runner.
			m.logger.Debug("llm call", args...)
		}
	}
}
