package curation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// concurrentMockLLM tracks concurrent calls to verify parallelism.
type concurrentMockLLM struct {
	delay       time.Duration
	maxConcur   int64 // peak concurrent calls observed
	curConcur   int64
	totalCalls  int64
}

func (m *concurrentMockLLM) ModelID() string                { return "test-mock" }
func (m *concurrentMockLLM) ProviderName() string           { return "mock" }
func (m *concurrentMockLLM) SupportsStructuredOutput() bool { return false }
func (m *concurrentMockLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

func (m *concurrentMockLLM) Complete(_ context.Context, prompt string) (string, error) {
	cur := atomic.AddInt64(&m.curConcur, 1)
	defer atomic.AddInt64(&m.curConcur, -1)

	// Track peak concurrency.
	for {
		old := atomic.LoadInt64(&m.maxConcur)
		if cur <= old || atomic.CompareAndSwapInt64(&m.maxConcur, old, cur) {
			break
		}
	}
	atomic.AddInt64(&m.totalCalls, 1)

	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return fmt.Sprintf("response for: %s", prompt[:20]), nil
}

func (m *concurrentMockLLM) CompleteWithModel(_ context.Context, _, prompt string) (string, error) {
	return m.Complete(context.Background(), prompt)
}

func TestParallelLLMBasic(t *testing.T) {
	llm := &concurrentMockLLM{delay: 10 * time.Millisecond}

	work := []llmWork{
		{id: "a", prompt: "classify this record about kafka"},
		{id: "b", prompt: "classify this record about redis"},
		{id: "c", prompt: "classify this record about nginx"},
	}

	results := parallelLLM(context.Background(), llm, work, 3)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("result[%d] error: %v", i, r.err)
		}
		if r.id != work[i].id {
			t.Fatalf("result[%d] id=%s, want %s", i, r.id, work[i].id)
		}
		if r.response == "" {
			t.Fatalf("result[%d] empty response", i)
		}
	}

	if atomic.LoadInt64(&llm.totalCalls) != 3 {
		t.Fatalf("expected 3 total calls, got %d", atomic.LoadInt64(&llm.totalCalls))
	}
}

func TestParallelLLMConcurrency(t *testing.T) {
	llm := &concurrentMockLLM{delay: 50 * time.Millisecond}

	work := make([]llmWork, 8)
	for i := range work {
		work[i] = llmWork{id: fmt.Sprintf("r%d", i), prompt: fmt.Sprintf("classify record number %d please", i)}
	}

	start := time.Now()
	results := parallelLLM(context.Background(), llm, work, 4)
	elapsed := time.Since(start)

	if len(results) != 8 {
		t.Fatalf("expected 8 results, got %d", len(results))
	}

	// With 4 workers and 50ms delay, 8 items should take ~100ms (2 batches).
	// Sequential would take ~400ms.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected parallel execution (~100ms), took %v", elapsed)
	}

	// Should have observed >1 concurrent calls.
	peak := atomic.LoadInt64(&llm.maxConcur)
	if peak <= 1 {
		t.Fatalf("expected parallel execution (peak concurrency > 1), got %d", peak)
	}
}

func TestParallelLLMSingleItem(t *testing.T) {
	llm := &concurrentMockLLM{}
	work := []llmWork{{id: "a", prompt: "classify single record please"}}

	results := parallelLLM(context.Background(), llm, work, 4)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("single item failed: %v", results)
	}
}

func TestParallelLLMEmpty(t *testing.T) {
	llm := &concurrentMockLLM{}
	results := parallelLLM(context.Background(), llm, nil, 4)
	if results != nil {
		t.Fatalf("expected nil for empty work, got %v", results)
	}
}

func TestParallelLLMContextCancel(t *testing.T) {
	llm := &concurrentMockLLM{delay: 200 * time.Millisecond}

	work := make([]llmWork, 10)
	for i := range work {
		work[i] = llmWork{id: fmt.Sprintf("r%d", i), prompt: fmt.Sprintf("classify record %d please now", i)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results := parallelLLM(ctx, llm, work, 2)

	// Some results should have context errors.
	hasErr := false
	for _, r := range results {
		if r.err != nil {
			hasErr = true
			break
		}
	}
	if !hasErr {
		t.Fatal("expected some context cancellation errors")
	}
}

func TestParallelLLMOrderPreserved(t *testing.T) {
	llm := &concurrentMockLLM{delay: 5 * time.Millisecond}

	work := make([]llmWork, 6)
	for i := range work {
		work[i] = llmWork{id: fmt.Sprintf("item_%d", i), prompt: fmt.Sprintf("process item %d right now ok", i)}
	}

	results := parallelLLM(context.Background(), llm, work, 3)

	for i, r := range results {
		expected := fmt.Sprintf("item_%d", i)
		if r.id != expected {
			t.Fatalf("result[%d] id=%s, want %s (order not preserved)", i, r.id, expected)
		}
	}
}
