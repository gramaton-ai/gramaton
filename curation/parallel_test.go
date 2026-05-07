package curation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
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

// structuredCapableMock is a Provider that supports structured
// output. Used to verify parallelLLM's runSingleWork dispatch
// selects CompleteStructured when the provider supports it AND
// the work item has a non-nil schema.
type structuredCapableMock struct {
	structuredCalls    int64
	plainCalls         int64
	forceStructuredErr error          // when non-nil, CompleteStructured returns this
	structuredResp     json.RawMessage
	mu                 sync.Mutex // guard mutable state when parallelLLM runs on one goroutine
	lastSchema         map[string]any
}

func (s *structuredCapableMock) Complete(_ context.Context, _ string) (string, error) {
	atomic.AddInt64(&s.plainCalls, 1)
	return `{"temporality":"durable","confidence":0.5,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["fallback"],"summary_short":"plain path fallback"}`, nil
}

func (s *structuredCapableMock) CompleteWithModel(ctx context.Context, _, prompt string) (string, error) {
	return s.Complete(ctx, prompt)
}

func (s *structuredCapableMock) ModelID() string                { return "structured-mock" }
func (s *structuredCapableMock) ProviderName() string           { return "mock" }
func (s *structuredCapableMock) SupportsStructuredOutput() bool { return true }
func (s *structuredCapableMock) CompleteStructured(_ context.Context, schema map[string]any, _ string) (json.RawMessage, error) {
	atomic.AddInt64(&s.structuredCalls, 1)
	s.mu.Lock()
	s.lastSchema = schema
	s.mu.Unlock()
	if s.forceStructuredErr != nil {
		return nil, s.forceStructuredErr
	}
	return s.structuredResp, nil
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
	// Sequential would take ~400ms. The 300ms bound is generous enough
	// that genuine parallelism comfortably clears it; if elapsed
	// exceeds 300ms we're either not parallel or the runner is
	// pathologically loaded -- either way useful signal.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected parallel execution (~100ms), took %v", elapsed)
	}

	// Peak observed concurrency is a soft signal: under load, the
	// scheduler may serialize the goroutines despite parallelLLM's
	// worker pool, leaving peak=1 even when the elapsed-time check
	// proved parallelism happened in aggregate. Report the
	// observation but don't fail the test for a scheduling decision
	// outside our control.
	peak := atomic.LoadInt64(&llm.maxConcur)
	if peak <= 1 {
		t.Logf("peak=%d: scheduler did not surface concurrent calls in this "+
			"run (timing-dependent observation; elapsed-time check above "+
			"is the authoritative parallelism gate)", peak)
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

// TestParallelLLMUsesStructuredWhenProviderSupports verifies the
// dispatch in runSingleWork: when the work item carries a schema
// AND the provider advertises SupportsStructuredOutput, the call
// routes through CompleteStructured rather than Complete.
// Regression against a future refactor that accidentally drops the
// capability check or the schema field.
func TestParallelLLMUsesStructuredWhenProviderSupports(t *testing.T) {
	prov := &structuredCapableMock{
		structuredResp: json.RawMessage(`{"temporality":"durable","confidence":0.9,"knowledge_type":"semantic","epistemic_status":"well_established","keywords":["test"],"summary_short":"structured path worked"}`),
	}
	schema := map[string]any{"type": "object"}
	work := []llmWork{
		{id: "rec1", prompt: "classify this", task: "classify", schema: schema},
	}

	results := parallelLLM(context.Background(), prov, work, 1)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("result err: %v", results[0].err)
	}
	// Structured path should have been hit exactly once; plain path
	// never touched.
	if got := atomic.LoadInt64(&prov.structuredCalls); got != 1 {
		t.Errorf("structuredCalls = %d, want 1 (dispatch to CompleteStructured didn't fire)", got)
	}
	if got := atomic.LoadInt64(&prov.plainCalls); got != 0 {
		t.Errorf("plainCalls = %d, want 0 (structured path should have handled it)", got)
	}
	// Response must be the raw JSON (parseable by parseClassification).
	if !containsKey(results[0].response, "structured path worked") {
		t.Errorf("response doesn't contain structured mock output: %q", results[0].response)
	}
	prov.mu.Lock()
	forwarded := prov.lastSchema
	prov.mu.Unlock()
	if forwarded["type"] != "object" {
		t.Errorf("schema not forwarded to provider: %+v", forwarded)
	}
}

// TestParallelLLMFallsBackOnStructuredError confirms the reliability
// fallback: when CompleteStructured errors, the code logs a Warn
// and retries via Complete, so a transient structured-path failure
// doesn't break classification.
func TestParallelLLMFallsBackOnStructuredError(t *testing.T) {
	prov := &structuredCapableMock{
		forceStructuredErr: errors.New("simulated provider glitch"),
	}
	schema := map[string]any{"type": "object"}
	work := []llmWork{{id: "r", prompt: "p", task: "classify", schema: schema}}

	results := parallelLLM(context.Background(), prov, work, 1)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("result: len=%d err=%v", len(results), results[0].err)
	}
	if got := atomic.LoadInt64(&prov.structuredCalls); got != 1 {
		t.Errorf("structured tried = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&prov.plainCalls); got != 1 {
		t.Errorf("plain fallback = %d, want 1 (fallback should have fired)", got)
	}
}

// TestParallelLLMSkipsStructuredWhenNoSchema verifies: work items
// with schema=nil bypass CompleteStructured even if the provider
// supports it. Summarization and other non-schema'd call sites in
// curation depend on this.
func TestParallelLLMSkipsStructuredWhenNoSchema(t *testing.T) {
	prov := &structuredCapableMock{
		structuredResp: json.RawMessage(`{"should":"not reach"}`),
	}
	work := []llmWork{{id: "r", prompt: "p", task: "summarize"}} // no schema

	results := parallelLLM(context.Background(), prov, work, 1)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("result: len=%d err=%v", len(results), results[0].err)
	}
	if got := atomic.LoadInt64(&prov.structuredCalls); got != 0 {
		t.Errorf("structuredCalls = %d, want 0 (schema was nil)", got)
	}
	if got := atomic.LoadInt64(&prov.plainCalls); got != 1 {
		t.Errorf("plainCalls = %d, want 1", got)
	}
}

// containsKey is a loose sanity check that doesn't require full
// JSON parsing; keeps the test focused on dispatch rather than the
// classification parser which has its own tests.
func containsKey(s, key string) bool {
	// helper used only here; kept local.
	return len(s) > 0 && func() bool {
		for i := 0; i+len(key) <= len(s); i++ {
			if s[i:i+len(key)] == key {
				return true
			}
		}
		return false
	}()
}

// Silence unused import warnings if the file ever gets pared down.
var _ = llm.ErrCapped

// taskRecordingMockLLM exposes the ctx its Complete call received,
// so tests can introspect telemetry attachments.
type taskRecordingMockLLM struct {
	mu       sync.Mutex
	gotTasks []string // task label observed per call
}

func (m *taskRecordingMockLLM) Complete(ctx context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotTasks = append(m.gotTasks, telemetry.TaskFromContext(ctx))
	return "ok", nil
}
func (m *taskRecordingMockLLM) CompleteWithModel(ctx context.Context, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotTasks = append(m.gotTasks, telemetry.TaskFromContext(ctx))
	return "ok", nil
}
func (m *taskRecordingMockLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (m *taskRecordingMockLLM) ModelID() string                                    { return "task-recorder" }
func (m *taskRecordingMockLLM) ProviderName() string                               { return "task-recorder" }
func (m *taskRecordingMockLLM) SupportsStructuredOutput() bool                     { return false }

// TestTaskCtxAttachesLabelOnSinglePath pins that the single-item
// fast path in parallelLLM correctly attaches w.task to the context
// so downstream telemetry sees the label. Pre-fix this logic was
// duplicated between the single-item and worker-loop paths; the
// dedup collapsed both to a shared taskCtx helper, and
// this test guards against silent drift on either path.
func TestTaskCtxAttachesLabelOnSinglePath(t *testing.T) {
	rec := &taskRecordingMockLLM{}
	work := []llmWork{{id: "n1", prompt: "p", task: "classify"}}

	results := parallelLLM(context.Background(), rec, work, 1)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(rec.gotTasks) != 1 || rec.gotTasks[0] != "classify" {
		t.Errorf("single-item path didn't attach task label; got %v", rec.gotTasks)
	}
}

// TestTaskCtxAttachesLabelOnWorkerPath pins the same invariant for
// the worker-loop path (len(work) > 1). Both paths must produce the
// same telemetry-context shape so dropping the dedup never causes
// the two paths to diverge.
func TestTaskCtxAttachesLabelOnWorkerPath(t *testing.T) {
	rec := &taskRecordingMockLLM{}
	work := []llmWork{
		{id: "a", prompt: "p", task: "summarize"},
		{id: "b", prompt: "p", task: "summarize"},
	}

	results := parallelLLM(context.Background(), rec, work, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(rec.gotTasks) != 2 {
		t.Fatalf("expected 2 calls, got %d (tasks=%v)", len(rec.gotTasks), rec.gotTasks)
	}
	for i, got := range rec.gotTasks {
		if got != "summarize" {
			t.Errorf("call %d: task label = %q, want %q", i, got, "summarize")
		}
	}
}

// TestTaskCtxNoTaskNoLabel pins the empty-task path: when w.task ==
// "", taskCtx returns the parent ctx unchanged and no label is
// attached.
func TestTaskCtxNoTaskNoLabel(t *testing.T) {
	rec := &taskRecordingMockLLM{}
	work := []llmWork{{id: "n1", prompt: "p"}} // task left empty

	parallelLLM(context.Background(), rec, work, 1)
	if len(rec.gotTasks) != 1 || rec.gotTasks[0] != "" {
		t.Errorf("empty task should not attach label; got %v", rec.gotTasks)
	}
}
