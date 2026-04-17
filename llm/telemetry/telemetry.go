// Package telemetry holds context-attached helpers for passing per-call
// LLM metadata through the Provider interface without expanding it.
//
// Two things flow through the context:
//
//   - Task label: which curation task is making the call ("classify",
//     "contradiction", etc.). Set by call sites, read by Metered to
//     label metrics.
//   - UsageRecorder: a receiver for token counts reported by providers
//     after a call completes. Set by Metered (or cycle-scope callers);
//     written by the anthropic client when it parses a response.
//
// This package is a dependency of both llm and llm/anthropic, keeping the
// usage reporting path free of circular imports.
package telemetry

import (
	"context"
	"sync"
)

// CallUsage captures per-call token counts reported by providers.
//
// InputTokens is the total input-token count billed for the call;
// CacheReadTokens and CacheWriteTokens are the portions served from
// or written to the provider's prompt cache. CacheReadTokens is
// usually billed at a steep discount vs standard input; CacheWriteTokens
// is usually billed at a small premium. InputTokens includes both
// cached and uncached portions (match the provider's accounting so
// cost calculators can subtract cache_{read,write} from input).
type CallUsage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Add returns the sum of a and b field-by-field.
func (a CallUsage) Add(b CallUsage) CallUsage {
	return CallUsage{
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}

// Sub returns a minus b field-by-field. Used to compute per-call delta
// from before/after snapshots of a shared recorder.
func (a CallUsage) Sub(b CallUsage) CallUsage {
	return CallUsage{
		InputTokens:      a.InputTokens - b.InputTokens,
		OutputTokens:     a.OutputTokens - b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens - b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens - b.CacheWriteTokens,
	}
}

// UsageRecorder accumulates per-task and total token counts. Safe for
// concurrent use -- parallel LLM workers can share one recorder.
type UsageRecorder struct {
	mu     sync.Mutex
	total  CallUsage
	byTask map[string]CallUsage
}

// Record adds the given usage to the recorder under the given task
// label. Empty task is recorded as "unknown".
func (r *UsageRecorder) Record(task string, u CallUsage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if task == "" {
		task = "unknown"
	}
	if r.byTask == nil {
		r.byTask = make(map[string]CallUsage)
	}
	r.byTask[task] = r.byTask[task].Add(u)
	r.total = r.total.Add(u)
}

// Total returns a snapshot of the total usage.
func (r *UsageRecorder) Total() CallUsage {
	if r == nil {
		return CallUsage{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// ByTask returns a copy of the per-task breakdown.
func (r *UsageRecorder) ByTask() map[string]CallUsage {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]CallUsage, len(r.byTask))
	for k, v := range r.byTask {
		out[k] = v
	}
	return out
}

type taskKey struct{}
type recorderKey struct{}

// WithTask attaches a task label to the context. Providers pass ctx
// through to downstream wrappers; Metered reads the label when
// recording metrics.
func WithTask(ctx context.Context, task string) context.Context {
	return context.WithValue(ctx, taskKey{}, task)
}

// TaskFromContext returns the task label attached to ctx, or empty.
func TaskFromContext(ctx context.Context) string {
	v, _ := ctx.Value(taskKey{}).(string)
	return v
}

// WithUsageRecorder attaches a UsageRecorder to the context. Providers
// call RecorderFromContext to find it and Record usage after parsing a
// response. Thread-safe even if multiple workers share one recorder.
func WithUsageRecorder(ctx context.Context, r *UsageRecorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

// RecorderFromContext returns the UsageRecorder attached to ctx, or
// nil. Providers should call Record on it when nil-safe (the method is
// a no-op for nil receivers).
func RecorderFromContext(ctx context.Context) *UsageRecorder {
	r, _ := ctx.Value(recorderKey{}).(*UsageRecorder)
	return r
}
