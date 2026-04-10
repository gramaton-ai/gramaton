package llm

import (
	"context"
	"time"
)

// Metered wraps a Provider and records usage metrics on every call.
type Metered struct {
	inner   Provider
	tracker *UsageTracker
	task    string // default task label
}

// NewMetered wraps a provider with usage tracking.
func NewMetered(inner Provider, tracker *UsageTracker) *Metered {
	return &Metered{inner: inner, tracker: tracker}
}

// WithTask returns a shallow copy with a specific task label for
// metrics attribution. Does not affect the underlying provider.
func (m *Metered) WithTask(task string) *Metered {
	return &Metered{inner: m.inner, tracker: m.tracker, task: task}
}

func (m *Metered) Complete(ctx context.Context, prompt string) (string, error) {
	start := time.Now()
	resp, err := m.inner.Complete(ctx, prompt)
	m.record(m.inner.ModelID(), start, err)
	return resp, err
}

func (m *Metered) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	start := time.Now()
	resp, err := m.inner.CompleteWithModel(ctx, model, prompt)
	usedModel := model
	if usedModel == "" {
		usedModel = m.inner.ModelID()
	}
	m.record(usedModel, start, err)
	return resp, err
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

func (m *Metered) record(model string, start time.Time, err error) {
	if m.tracker == nil {
		return
	}
	task := m.task
	if task == "" {
		task = "unknown"
	}
	errType := ""
	if err != nil {
		errType = "error"
	}
	m.tracker.Record(CallMetrics{
		Provider:  "metered",
		Model:     model,
		Task:      task,
		LatencyMs: time.Since(start).Milliseconds(),
		Success:   err == nil,
		ErrorType: errType,
	})
}
