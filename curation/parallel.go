package curation

import (
	"context"
	"sync"

	"github.com/gramaton-ai/gramaton/llm"
)

// llmWork represents a single LLM call to be executed in a worker pool.
type llmWork struct {
	id     string // record ID or identifier for logging
	prompt string
	model  string // model override; empty = use provider default
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
		var resp string
		var err error
		if work[0].model != "" {
			resp, err = llmProv.CompleteWithModel(ctx, work[0].model, work[0].prompt)
		} else {
			resp, err = llmProv.Complete(ctx, work[0].prompt)
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
				var resp string
				var err error
				if work[idx].model != "" {
					resp, err = llmProv.CompleteWithModel(ctx, work[idx].model, work[idx].prompt)
				} else {
					resp, err = llmProv.Complete(ctx, work[idx].prompt)
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
