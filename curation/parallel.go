package curation

import (
	"context"
	"sync"

	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// llmWork represents a single LLM call to be executed in a worker pool.
type llmWork struct {
	id     string // record ID or identifier for logging
	prompt string
	model  string // model override; empty = use provider default
	task   string // task label attached to ctx for usage metering
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
		callCtx := ctx
		if work[0].task != "" {
			callCtx = telemetry.WithTask(ctx, work[0].task)
		}
		var resp string
		var err error
		if work[0].model != "" {
			resp, err = llmProv.CompleteWithModel(callCtx, work[0].model, work[0].prompt)
		} else {
			resp, err = llmProv.Complete(callCtx, work[0].prompt)
		}
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
				callCtx := ctx
				if work[idx].task != "" {
					callCtx = telemetry.WithTask(ctx, work[idx].task)
				}
				var resp string
				var err error
				if work[idx].model != "" {
					resp, err = llmProv.CompleteWithModel(callCtx, work[idx].model, work[idx].prompt)
				} else {
					resp, err = llmProv.Complete(callCtx, work[idx].prompt)
				}
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
