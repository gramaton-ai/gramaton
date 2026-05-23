package curation

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// isDeferrableLLMError reports whether err is a system-level signal
// that should not count against a record's (or pair's, or manifest's)
// retry budget. The caps and context-cancel errors mean "we never
// asked the LLM the question" -- the record is fine, the system
// paused. Treating these as per-record failures wrongly poisons the
// queue: three cap-fired cycles flip processing_status to "stuck"
// even though no record-shaped problem exists. Callers detect this
// class before feeding err into recordTaskFailure / soft-fail edges /
// the manifest negative cache.
//
// Provider-side transient errors (HTTP 5xx, rate-limit responses
// from providers other than the Anthropic batch API) are not in
// this set yet -- they arrive as opaque strings today; typed
// detection is a separate follow-up.
func isDeferrableLLMError(err error) bool {
	return errors.Is(err, llm.ErrCapped) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// llmWork represents a single LLM call to be executed in a worker pool.
// When schema is non-nil AND the provider supports structured output,
// parallelLLM routes the call through CompleteStructured instead of
// Complete — the response still comes back as a string in llmResult
// (the raw JSON text) so existing parsers like parseClassification
// handle both paths with no branching. Providers without structured-
// output support fall through to the regular Complete path for this
// work item, ignoring schema.
type llmWork struct {
	id     string // record ID or identifier for logging
	prompt string
	model  string // model override; empty = use provider default
	task   string // task label attached to ctx for usage metering
	schema map[string]any // optional JSON Schema for structured output
}

// completeWithModelOrDefault calls CompleteWithModel when model is
// non-empty, else falls back to the provider's default via Complete.
// Used by curation tasks that resolve their model via cfg.ModelForTask --
// an empty result there signals "no tier configured, let the provider
// pick." Attaches the task label to ctx so Metered records metrics
// under the right bucket.
func completeWithModelOrDefault(ctx context.Context, p llm.Provider, task, model, prompt string) (string, error) {
	if task != "" {
		ctx = telemetry.WithTask(ctx, task)
	}
	if model != "" {
		return p.CompleteWithModel(ctx, model, prompt)
	}
	return p.Complete(ctx, prompt)
}

// llmResult pairs a work item with its LLM response.
type llmResult struct {
	id       string
	response string
	err      error
}

// taskCtx attaches a telemetry task label to ctx when w carries one.
// Single helper used by both the single-item fast path and the
// worker loop in parallelLLM so the two stay in sync.
func taskCtx(ctx context.Context, w llmWork) context.Context {
	if w.task == "" {
		return ctx
	}
	return telemetry.WithTask(ctx, w.task)
}

// runSingleWork executes one llmWork item, dispatching to the
// structured-output path when the provider supports it and the work
// item carries a schema. Returns the response text (or raw JSON from
// the structured path, which parses identically with the same
// parser) so the caller's existing llmResult.response string field
// stays the uniform interface.
func runSingleWork(ctx context.Context, p llm.Provider, w llmWork) (string, error) {
	if w.schema != nil && p.SupportsStructuredOutput() {
		raw, err := p.CompleteStructured(ctx, w.schema, w.prompt)
		if err == nil {
			return string(raw), nil
		}
		// Structured path error: fall through to Complete. This is a
		// reliability fallback (provider hiccup, transient failure),
		// not a correctness concern — the prompt is self-contained
		// and returns JSON either way. Warn-log so a persistent
		// structured-path regression is visible in ops rather than
		// silently reverting every call.
		slog.Warn("structured-output call failed, falling back to Complete",
			"component", "curation",
			"record", w.id,
			"task", w.task,
			"err", err)
	}
	if w.model != "" {
		return p.CompleteWithModel(ctx, w.model, w.prompt)
	}
	return p.Complete(ctx, w.prompt)
}

// parallelLLM executes LLM calls concurrently with bounded parallelism.
// Returns results in the same order as the input work items.
// Respects context cancellation -- in-flight calls will be cancelled.
func parallelLLM(ctx context.Context, llmProv llm.Provider, work []llmWork, maxWorkers int) []llmResult {
	if len(work) == 0 {
		return nil
	}
	if maxWorkers <= 0 {
		maxWorkers = 4
	}
	if maxWorkers > len(work) {
		maxWorkers = len(work)
	}

	// For single item, skip the goroutine overhead.
	if len(work) == 1 {
		resp, err := runSingleWork(taskCtx(ctx, work[0]), llmProv, work[0])
		return []llmResult{{id: work[0].id, response: resp, err: err}}
	}

	results := make([]llmResult, len(work))
	workCh := make(chan int, len(work))

	var wg sync.WaitGroup
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range workCh {
				select {
				case <-ctx.Done():
					results[idx] = llmResult{
						id:  work[idx].id,
						err: ctx.Err(),
					}
					continue
				default:
				}
				resp, err := runSingleWork(taskCtx(ctx, work[idx]), llmProv, work[idx])
				results[idx] = llmResult{
					id:       work[idx].id,
					response: resp,
					err:      err,
				}
			}
		}()
	}

	for i := range work {
		workCh <- i
	}
	close(workCh)
	wg.Wait()

	return results
}
