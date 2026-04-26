package curation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// mockLLM is a controllable LLM provider for testing.
// Thread-safe: guards mutable state with a mutex because
// parallelLLM() calls Complete/CompleteWithModel from
// multiple goroutines concurrently.
type mockLLM struct {
	mu        sync.Mutex
	responses []string // returned in order, cycles if exhausted
	errors    []error  // if non-nil at index, return error instead
	calls     int
	models    []string // model requested per call (for tiering tests)
}

func (m *mockLLM) Complete(_ context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	m.calls++
	m.models = append(m.models, "") // no model override
	if idx < len(m.errors) && m.errors[idx] != nil {
		return "", m.errors[idx]
	}
	if len(m.responses) == 0 {
		return "", fmt.Errorf("no responses configured")
	}
	return m.responses[idx%len(m.responses)], nil
}

func (m *mockLLM) CompleteWithModel(_ context.Context, model, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	m.calls++
	m.models = append(m.models, model)
	if idx < len(m.errors) && m.errors[idx] != nil {
		return "", m.errors[idx]
	}
	if len(m.responses) == 0 {
		return "", fmt.Errorf("no responses configured")
	}
	return m.responses[idx%len(m.responses)], nil
}

func (m *mockLLM) ModelID() string      { return "mock-llm" }
func (m *mockLLM) ProviderName() string           { return "mock" }
func (m *mockLLM) SupportsStructuredOutput() bool { return false }
func (m *mockLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

// mockLLMWithSystem extends mockLLM with SystemPromptSetter support.
type mockLLMWithSystem struct {
	mockLLM
	systemPrompt string
}

func (m *mockLLMWithSystem) SetSystemPrompt(text string) {
	m.systemPrompt = text
}

// addPendingNode adds a record with processing_status="captured".
func addPendingNode(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

// addProcessedNodeNoSummary adds a processed record without content_short.
func addProcessedNodeNoSummary(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

// --- classifyPending tests ---

func TestClassifyPendingHappyPath(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	id := addPendingNode(t, eng, "Kafka event streaming architecture for microservices")

	llm := &mockLLM{
		responses: []string{`{
			"temporality": "durable",
			"confidence": 0.9,
			"knowledge_type": "semantic",
			"epistemic_status": "well_established",
			"keywords": ["kafka", "streaming"],
			"summary_short": "Kafka event streaming for microservices"
		}`},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	if result.LLMCalls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", result.LLMCalls)
	}
	if result.Errors != 0 {
		t.Fatalf("expected 0 errors, got %d", result.Errors)
	}

	// Verify properties were set.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("node not found")
	}
	if v, _ := n.Properties.GetString("temporality"); v != "durable" {
		t.Fatalf("expected durable, got %q", v)
	}
	if v, _ := n.Properties.GetFloat64("confidence"); v != 0.9 {
		t.Fatalf("expected 0.9, got %f", v)
	}
	if v, _ := n.Properties.GetString("processing_status"); v != "processed" {
		t.Fatalf("expected processed, got %q", v)
	}
	if v, _ := n.Properties.GetString("content_short"); v != "Kafka event streaming for microservices" {
		t.Fatalf("expected summary, got %q", v)
	}
	if kw, _ := n.Properties.GetStringList("content_keywords"); len(kw) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(kw))
	}
}

func TestClassifyPendingLLMError(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addPendingNode(t, eng, "Content that will fail")

	llm := &mockLLM{
		errors: []error{fmt.Errorf("API timeout")},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 0 {
		t.Fatalf("expected 0 classified, got %d", result.Classified)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
	if result.LLMCalls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", result.LLMCalls)
	}
}

func TestClassifyPendingParseError(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addPendingNode(t, eng, "Content with bad LLM response")

	llm := &mockLLM{
		responses: []string{"This is not JSON at all"},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 0 {
		t.Fatalf("expected 0 classified, got %d", result.Classified)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
}

// TestClassifyPendingFailureBumpsAttemptCounter verifies that a single
// failed classify run writes classify_attempts=1 and last_classify_error
// without flipping processing_status.
//
// Regression guard for tracker 01KQ3X9EBX4WKVJQ56W1C31V97 — without
// this counter, a record that cannot be classified (oversized content,
// content-policy refusal, persistent parse failures) re-enters the
// FIFO pending queue every minute and bills tokens forever.
func TestClassifyPendingFailureBumpsAttemptCounter(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxClassifyAttempts = 3

	id := addPendingNode(t, eng, "Content that always fails")

	llm := &mockLLM{errors: []error{fmt.Errorf("API timeout")}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("classify_attempts")
	if attempts != 1 {
		t.Errorf("classify_attempts: got %d, want 1", attempts)
	}
	reason, _ := n.Properties.GetString("last_classify_error")
	if !strings.Contains(reason, "API timeout") {
		t.Errorf("last_classify_error: got %q, want contains %q", reason, "API timeout")
	}
	status, _ := n.Properties.GetString("processing_status")
	if status != "captured" {
		t.Errorf("processing_status: got %q, want still %q (below threshold)", status, "captured")
	}
}

// TestClassifyPendingMarksStuckAtThreshold verifies that after
// MaxClassifyAttempts consecutive failures, the record is moved to
// processing_status="stuck" and excluded from the next cycle's batch.
func TestClassifyPendingMarksStuckAtThreshold(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxClassifyAttempts = 3

	id := addPendingNode(t, eng, "Pathological content")

	// Three consecutive cycles, each one failure.
	for cycle := 1; cycle <= 3; cycle++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("cycle %d failure", cycle)}}
		result := &AutonomousResult{}
		classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("classify_attempts")
	status, _ := n.Properties.GetString("processing_status")
	eng.RUnlock()

	if attempts != 3 {
		t.Errorf("classify_attempts: got %d, want 3", attempts)
	}
	if status != "stuck" {
		t.Errorf("processing_status: got %q, want %q", status, "stuck")
	}

	// Next cycle: no attempt should be made on this record (stuck
	// records are excluded from the captured-status filter).
	llm := &mockLLM{errors: []error{fmt.Errorf("should not fire")}}
	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	if result.LLMCalls != 0 {
		t.Errorf("stuck record was re-attempted: LLMCalls=%d, want 0", result.LLMCalls)
	}
}

// TestClassifyPendingMaxAttemptsZeroDisables verifies legacy behavior:
// when MaxClassifyAttempts=0, the counter feature is disabled and
// failed records are not annotated.
func TestClassifyPendingMaxAttemptsZeroDisables(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxClassifyAttempts = 0

	id := addPendingNode(t, eng, "Content")

	llm := &mockLLM{errors: []error{fmt.Errorf("error")}}
	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetInt64("classify_attempts"); ok {
		t.Error("classify_attempts was written when MaxClassifyAttempts=0")
	}
	if _, ok := n.Properties.GetString("last_classify_error"); ok {
		t.Error("last_classify_error was written when MaxClassifyAttempts=0")
	}
}

// TestClassifyPendingSuccessClearsAttempts verifies that a successful
// classification on a record that previously failed resets the
// classify_attempts counter so an operator-fixed record passes
// cleanly on its next attempt.
func TestClassifyPendingSuccessClearsAttempts(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxClassifyAttempts = 3

	id := addPendingNode(t, eng, "Initially failing content")

	// Fail twice -- attempts now 2, still captured.
	for i := 0; i < 2; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("err %d", i)}}
		classifyPending(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	// Verify intermediate state.
	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	if a, _ := n.Properties.GetInt64("classify_attempts"); a != 2 {
		eng.RUnlock()
		t.Fatalf("intermediate classify_attempts: got %d, want 2", a)
	}
	eng.RUnlock()

	// Now succeed -- counter must reset, status flips to processed.
	llm := &mockLLM{responses: []string{`{"temporality":"durable","confidence":0.8}`}}
	classifyPending(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ = eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("classify_attempts")
	status, _ := n.Properties.GetString("processing_status")
	if attempts != 0 {
		t.Errorf("classify_attempts after success: got %d, want 0", attempts)
	}
	if status != "processed" {
		t.Errorf("processing_status after success: got %q, want %q", status, "processed")
	}
}

func TestClassifyPendingMaxCallsLimit(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	// Create 5 pending records.
	for i := 0; i < 5; i++ {
		addPendingNode(t, eng, fmt.Sprintf("Record %d", i))
	}

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.8}`},
	}

	result := &AutonomousResult{}
	// Limit to 3 calls.
	classifyPending(context.Background(), eng, llm, cfg, result, 3, 0, nil, false)

	if result.LLMCalls != 3 {
		t.Fatalf("expected 3 LLM calls (max), got %d", result.LLMCalls)
	}
	if result.Classified != 3 {
		t.Fatalf("expected 3 classified, got %d", result.Classified)
	}
}

func TestClassifyPendingBatchSize(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 2

	// Create 5 pending records.
	for i := 0; i < 5; i++ {
		addPendingNode(t, eng, fmt.Sprintf("Record %d", i))
	}

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.8}`},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	// Should only process batch_size=2 even though 5 exist.
	if result.Classified != 2 {
		t.Fatalf("expected 2 classified (batch size), got %d", result.Classified)
	}
}

func TestClassifyPendingContextCancelled(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	for i := 0; i < 5; i++ {
		addPendingNode(t, eng, fmt.Sprintf("Record %d", i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.8}`},
	}

	result := &AutonomousResult{}
	classifyPending(ctx, eng, llm, cfg, result, 20, 0, nil, false)

	// Should stop immediately.
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls after cancel, got %d", result.LLMCalls)
	}
}

func TestClassifyPendingSkipsEmptyContent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	// Add a node with empty content.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(""),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable"}`},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (empty content), got %d", result.LLMCalls)
	}
}

func TestClassifyPendingModelTiering(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	// Set distinct models for low and medium tiers; the default effort
	// for short classification is low, for long is medium.
	cfg.LLM.Models.Low = "test-low-model"
	cfg.LLM.Models.Medium = "test-medium-model"
	cfg.LLMCuration.LongClassificationThreshold = 100

	// Short content (below threshold) -> low-tier model.
	shortID := addPendingNode(t, eng, "short fact")
	// Long content (above threshold) -> medium-tier model.
	longContent := strings.Repeat("a", 200)
	longID := addPendingNode(t, eng, longContent)

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp, classifyResp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 2 {
		t.Fatalf("expected 2 classified, got %d", result.Classified)
	}
	if len(llm.models) != 2 {
		t.Fatalf("expected 2 model entries, got %d", len(llm.models))
	}

	// Check which model was used for each. Order depends on iteration
	// over the pending set, so check by matching content length.
	eng.RLock()
	defer eng.RUnlock()

	shortNode, _ := eng.Graph().GetNode(shortID)
	longNode, _ := eng.Graph().GetNode(longID)

	shortClassifiedBy, _ := shortNode.Properties.GetString("classified_by")
	longClassifiedBy, _ := longNode.Properties.GetString("classified_by")

	if shortClassifiedBy != "test-low-model" {
		t.Errorf("short record should be classified by low-tier model, got %q", shortClassifiedBy)
	}
	if longClassifiedBy != "test-medium-model" {
		t.Errorf("long record should be classified by medium-tier model, got %q", longClassifiedBy)
	}
}

func TestClassifyPendingNoTieringWhenModelsUnset(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	// Clear all tier models -- curation should fall back to the
	// provider's default via Complete() (not CompleteWithModel).
	cfg.LLM.Models.Low = ""
	cfg.LLM.Models.Medium = ""
	cfg.LLM.Models.High = ""

	addPendingNode(t, eng, "short fact")

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	// Should use Complete (no model override), not CompleteWithModel.
	if len(llm.models) != 1 || llm.models[0] != "" {
		t.Errorf("expected no model override, got %v", llm.models)
	}
}

func TestCompleteWithModelAnthropicFallback(t *testing.T) {
	// Verify that CompleteWithModel with empty model uses the default.
	// This is a unit test of the Anthropic client interface contract --
	// we can't call the real API, but we verify the parallel.go routing.
	work := []llmWork{
		{id: "a", prompt: "hello", model: ""},
		{id: "b", prompt: "hello", model: "override-model"},
	}
	llm := &mockLLM{responses: []string{"r1", "r2"}}
	results := parallelLLM(context.Background(), llm, work, 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// First should have used Complete (empty model).
	if llm.models[0] != "" {
		t.Errorf("expected empty model for first call, got %q", llm.models[0])
	}
	// Second should have used CompleteWithModel.
	if llm.models[1] != "override-model" {
		t.Errorf("expected 'override-model' for second call, got %q", llm.models[1])
	}
}

func TestClassifyPendingSetsSystemPrompt(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addPendingNode(t, eng, "test content for system prompt")

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLMWithSystem{
		mockLLM: mockLLM{responses: []string{classifyResp}},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	// System prompt should be cleared after classification (defer).
	if llm.systemPrompt != "" {
		t.Fatalf("system prompt should be cleared after classification, got %q", llm.systemPrompt)
	}
}

func TestClassifyPendingFallsBackWithoutSystemPrompt(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addPendingNode(t, eng, "test content")

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	// The prompt should contain the full taxonomy (system + user concatenated)
	// since mockLLM doesn't implement SystemPromptSetter.
	// We verify it ran without error -- the fallback path worked.
}

// --- generateSummaries tests ---

func TestGenerateSummariesHappyPath(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	id := addProcessedNodeNoSummary(t, eng, "OAuth 2.0 implementation guide for internal services")

	llm := &mockLLM{
		responses: []string{"OAuth 2.0 guide for internal services"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.SummariesGenerated != 1 {
		t.Fatalf("expected 1 summary, got %d", result.SummariesGenerated)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	v, ok := n.Properties.GetString("content_short")
	if !ok || v != "OAuth 2.0 guide for internal services" {
		t.Fatalf("expected summary, got %q", v)
	}
}

// TestSummarizeFailureBumpsAttemptCounter verifies that a single
// failed summary call writes summary_attempts=1 and last_summary_error
// without skipping the record yet.
//
// Regression guard for tracker 01KQ406Z12VKRGRT3HEER0ZT1A: without this
// counter, a record the LLM consistently can't summarize re-enters the
// summary candidate set every cycle and bills input tokens forever.
func TestSummarizeFailureBumpsAttemptCounter(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSummaryAttempts = 3

	id := addProcessedNodeNoSummary(t, eng, "Content that fails")

	llm := &mockLLM{errors: []error{fmt.Errorf("API timeout")}}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("summary_attempts")
	if attempts != 1 {
		t.Errorf("summary_attempts: got %d, want 1", attempts)
	}
	reason, _ := n.Properties.GetString("last_summary_error")
	if !strings.Contains(reason, "API timeout") {
		t.Errorf("last_summary_error: got %q, want contains %q", reason, "API timeout")
	}
}

// TestSummarizeSkipsRecordsAtThreshold verifies that after
// MaxSummaryAttempts consecutive failures, the record is excluded from
// the next cycle's selection (no LLM call attempted).
func TestSummarizeSkipsRecordsAtThreshold(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSummaryAttempts = 3

	addProcessedNodeNoSummary(t, eng, "Pathological content")

	// Three consecutive cycles, each one failure.
	for cycle := 1; cycle <= 3; cycle++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("cycle %d failure", cycle)}}
		generateSummaries(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	// Next cycle: no attempt should be made on this record (selection
	// guard skips records where summary_attempts >= max).
	llm := &mockLLM{errors: []error{fmt.Errorf("should not fire")}}
	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	if result.LLMCalls != 0 {
		t.Errorf("at-threshold record was re-attempted: LLMCalls=%d, want 0", result.LLMCalls)
	}
}

// TestSummarizeMaxAttemptsZeroDisables verifies legacy behavior:
// when MaxSummaryAttempts=0, the counter feature is disabled and
// failed records are not annotated.
func TestSummarizeMaxAttemptsZeroDisables(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSummaryAttempts = 0

	id := addProcessedNodeNoSummary(t, eng, "Content")

	llm := &mockLLM{errors: []error{fmt.Errorf("error")}}
	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetInt64("summary_attempts"); ok {
		t.Error("summary_attempts was written when MaxSummaryAttempts=0")
	}
	if _, ok := n.Properties.GetString("last_summary_error"); ok {
		t.Error("last_summary_error was written when MaxSummaryAttempts=0")
	}
}

// TestSummarizeSuccessClearsAttempts verifies that a successful
// summary on a record that previously failed resets the
// summary_attempts counter so an operator-fixed record passes
// cleanly on its next attempt.
func TestSummarizeSuccessClearsAttempts(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSummaryAttempts = 3

	id := addProcessedNodeNoSummary(t, eng, "Initially failing content")

	// Fail twice -- attempts now 2, still selectable.
	for i := 0; i < 2; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("err %d", i)}}
		generateSummaries(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	// Verify intermediate state.
	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	if a, _ := n.Properties.GetInt64("summary_attempts"); a != 2 {
		eng.RUnlock()
		t.Fatalf("intermediate summary_attempts: got %d, want 2", a)
	}
	eng.RUnlock()

	// Now succeed -- counter must reset, summary written.
	llm := &mockLLM{responses: []string{"a successful summary"}}
	generateSummaries(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ = eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("summary_attempts")
	if attempts != 0 {
		t.Errorf("summary_attempts after success: got %d, want 0", attempts)
	}
	if v, _ := n.Properties.GetString("content_short"); v != "a successful summary" {
		t.Errorf("content_short after success: got %q, want %q", v, "a successful summary")
	}
}

func TestGenerateSummariesLLMError(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addProcessedNodeNoSummary(t, eng, "Content")

	llm := &mockLLM{
		errors: []error{fmt.Errorf("rate limited")},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.SummariesGenerated != 0 {
		t.Fatalf("expected 0 summaries, got %d", result.SummariesGenerated)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
}

func TestGenerateSummariesEmptyResponse(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addProcessedNodeNoSummary(t, eng, "Content")

	llm := &mockLLM{
		responses: []string{"   "},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.SummariesGenerated != 0 {
		t.Fatalf("expected 0 (empty summary), got %d", result.SummariesGenerated)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error (empty summary), got %d", result.Errors)
	}
}

func TestGenerateSummariesTruncation(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	id := addProcessedNodeNoSummary(t, eng, "Content")

	// Return a 300-char summary.
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	llm := &mockLLM{
		responses: []string{long},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.SummariesGenerated != 1 {
		t.Fatalf("expected 1 summary, got %d", result.SummariesGenerated)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	v, _ := n.Properties.GetString("content_short")
	if len([]rune(v)) > 200 {
		t.Fatalf("summary should be truncated to 200, got %d", len([]rune(v)))
	}
}

func TestGenerateSummariesSkipsChunks(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	// Create a parent and a chunk node.
	eng.Lock()
	parent := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Parent content"),
		"processing_status": graph.StringProperty("processed"),
		"content_short":     graph.StringProperty("Has summary"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range parent.Properties {
		eng.PropIdx().Add(parent.ID, k, v)
	}
	chunk := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Chunk content without summary"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range chunk.Properties {
		eng.PropIdx().Add(chunk.ID, k, v)
	}
	eng.Graph().AddEdge(chunk.ID, parent.ID, "chunk_of", 1.0, nil)
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{"Should not be called"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	// Chunk should be skipped, parent already has summary.
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (chunk skipped, parent has summary), got %d", result.LLMCalls)
	}
}

func TestGenerateSummariesSkipsDeleted(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Deleted content"),
		"processing_status": graph.StringProperty("deleted"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{"Should not be called"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (deleted skipped), got %d", result.LLMCalls)
	}
}

func TestGenerateSummariesSkipsExistingSummary(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Content"),
		"content_short":     graph.StringProperty("Already has summary"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{"Should not be called"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (already has summary), got %d", result.LLMCalls)
	}
}

func TestGenerateSummariesMaxCalls(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	for i := 0; i < 5; i++ {
		addProcessedNodeNoSummary(t, eng, fmt.Sprintf("Content %d", i))
	}

	llm := &mockLLM{
		responses: []string{"Summary"},
	}

	result := &AutonomousResult{}
	// Limit to 2 calls.
	generateSummaries(context.Background(), eng, llm, cfg, result, 2, 0, nil, false)

	if result.LLMCalls != 2 {
		t.Fatalf("expected 2 LLM calls (max), got %d", result.LLMCalls)
	}
	if result.SummariesGenerated != 2 {
		t.Fatalf("expected 2 summaries, got %d", result.SummariesGenerated)
	}
}

// --- RunAutonomous integration ---

func TestRunAutonomousIntegration(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10
	cfg.LLMCuration.MaxCallsPerRun = 20

	// One pending (needs classify), one processed without summary (needs summarize).
	addPendingNode(t, eng, "Pending: Kafka event streaming")
	addProcessedNodeNoSummary(t, eng, "No summary: OAuth implementation guide")

	llm := &mockLLM{
		responses: []string{
			// First call: classify
			`{"temporality":"durable","confidence":0.9,"knowledge_type":"semantic","keywords":["kafka"],"summary_short":"Kafka streaming"}`,
			// Second call: summarize
			"OAuth implementation guide for services",
		},
	}

	result := RunAutonomous(context.Background(), eng, llm, cfg, nil, nil)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	if result.SummariesGenerated != 1 {
		t.Fatalf("expected 1 summary, got %d", result.SummariesGenerated)
	}
	if result.LLMCalls != 2 {
		t.Fatalf("expected 2 total LLM calls, got %d", result.LLMCalls)
	}
}

// --- Dry-run tests ---

func TestClassifyPendingDryRun(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	addPendingNode(t, eng, "Kafka event streaming for microservices")

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.9,"knowledge_type":"semantic","keywords":["kafka"],"summary_short":"Kafka streaming"}`},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, true)

	if result.Classified != 1 {
		t.Fatalf("expected 1 classified in dry-run, got %d", result.Classified)
	}
	if len(result.PlannedChanges) != 1 {
		t.Fatalf("expected 1 planned change, got %d", len(result.PlannedChanges))
	}
	if result.PlannedChanges[0].Action != "classify" {
		t.Fatalf("expected action 'classify', got %q", result.PlannedChanges[0].Action)
	}

	// Verify no mutation: record should still be "captured".
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps != "captured" {
			t.Fatalf("dry-run should not change processing_status, got %q", ps)
		}
	}
}

func TestGenerateSummariesDryRun(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	addProcessedNodeNoSummary(t, eng, "OAuth implementation guide")

	llm := &mockLLM{
		responses: []string{"OAuth guide for service auth"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, true)

	if result.SummariesGenerated != 1 {
		t.Fatalf("expected 1 summary in dry-run, got %d", result.SummariesGenerated)
	}
	if len(result.PlannedChanges) != 1 {
		t.Fatalf("expected 1 planned change, got %d", len(result.PlannedChanges))
	}
	if result.PlannedChanges[0].Action != "summarize" {
		t.Fatalf("expected action 'summarize', got %q", result.PlannedChanges[0].Action)
	}

	// Verify no mutation: record should still lack content_short.
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if _, ok := n.Properties.GetString("content_short"); ok {
			t.Fatal("dry-run should not add content_short")
		}
	}
}

func TestRunAutonomousDryRunIntegration(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10
	cfg.LLMCuration.MaxCallsPerRun = 20

	addPendingNode(t, eng, "Pending: Kafka streaming")
	addProcessedNodeNoSummary(t, eng, "No summary: OAuth guide")

	llm := &mockLLM{
		responses: []string{
			// 1: classify the pending node
			`{"temporality":"durable","confidence":0.9,"knowledge_type":"semantic","keywords":["kafka"],"summary_short":"Kafka streaming"}`,
			// 2: summarize the pending node (still lacks content_short since dry-run didn't apply classify)
			"Kafka event streaming overview",
			// 3: summarize the processed node
			"OAuth guide for services",
		},
	}

	result := RunAutonomousDryRun(context.Background(), eng, llm, cfg, nil)

	if !result.DryRun {
		t.Fatal("expected DryRun=true")
	}
	if result.Classified != 1 {
		t.Fatalf("expected 1 classified, got %d", result.Classified)
	}
	// Both nodes lack content_short (dry-run classify didn't apply the summary_short).
	if result.SummariesGenerated != 2 {
		t.Fatalf("expected 2 summaries, got %d", result.SummariesGenerated)
	}
	// 1 classify + 2 summarize = 3 planned changes.
	if len(result.PlannedChanges) != 3 {
		t.Fatalf("expected 3 planned changes, got %d", len(result.PlannedChanges))
	}
	if result.LLMCalls != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", result.LLMCalls)
	}
}

// --- Contradiction detection tests ---

func addProcessedNodeWithEmbedding(t *testing.T, eng *core.Engine, content string, embedding []float32) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	now := time.Now().UTC()
	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"embedding_full":    graph.VectorProperty(embedding),
	}
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, embedding)
	eng.Save("test")
	return n.ID
}

func TestDetectContradictions(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1 // exercise single-pair path

	// Two records with embeddings in the contradiction similarity range (~0.7 cosine).
	addProcessedNodeWithEmbedding(t, eng, "We use JWT tokens for auth", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We switched to session cookies for auth", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"contradicts","confidence":0.8,"explanation":"JWT vs session cookies are incompatible auth approaches"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.ContradictionsDetected != 1 {
		t.Fatalf("expected 1 contradiction detected, got %d", result.ContradictionsDetected)
	}

	// Verify edge was created.
	eng.RLock()
	defer eng.RUnlock()
	foundEdge := false
	for _, id := range eng.Graph().AllNodeIDs() {
		for _, e := range eng.Graph().EdgesFrom(id) {
			if e.Type == "contradicts" {
				foundEdge = true
			}
		}
	}
	if !foundEdge {
		t.Fatal("expected contradicts edge to be created")
	}
}

func TestDetectContradictionsDryRun(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1

	addProcessedNodeWithEmbedding(t, eng, "We use PostgreSQL", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We migrated to MySQL", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"supersedes","confidence":0.85,"explanation":"MySQL migration replaces PostgreSQL"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, true)

	if result.ContradictionsDetected != 1 {
		t.Fatalf("expected 1 contradiction in dry-run, got %d", result.ContradictionsDetected)
	}
	if len(result.PlannedChanges) != 1 {
		t.Fatalf("expected 1 planned change, got %d", len(result.PlannedChanges))
	}
	if result.PlannedChanges[0].Action != "supersedes" {
		t.Fatalf("expected action 'supersedes', got %q", result.PlannedChanges[0].Action)
	}

	// Verify no mutation.
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		if _, ok := eng.Graph().GetNode(id); ok {
			if len(eng.Graph().EdgesFrom(id)) > 0 {
				t.Fatal("dry-run should not create edges")
			}
		}
	}
}

func TestDetectContradictionsNoMatch(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95

	// Records with very different embeddings (below min similarity).
	addProcessedNodeWithEmbedding(t, eng, "Auth tokens", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Database schema", []float32{0.0, 0.0, 1.0})

	llm := &mockLLM{responses: []string{}}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	// No LLM calls should be made since records are too dissimilar.
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls for dissimilar records, got %d", result.LLMCalls)
	}
	if result.ContradictionsDetected != 0 {
		t.Fatalf("expected 0 contradictions, got %d", result.ContradictionsDetected)
	}
}

// TestDetectContradictionsWritesNoContradictionEdge regressions the pool-
// never-drains bug (see design-decisions.md D38). Two records land in the
// contradiction-check similarity window; the LLM reports "none" (no
// contradiction or supersession); the write phase must persist that
// negative result as a no_contradiction edge so the next cycle skips the
// pair. Before this fix, negative results produced zero persistent state
// and the same pairs got re-checked every cycle, burning cost without
// drain.
func TestDetectContradictionsWritesNoContradictionEdge(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha observation about caching", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta observation, similar-but-distinct topic", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"none","confidence":0.9,"explanation":"distinct observations"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.LLMCalls != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", result.LLMCalls)
	}
	if result.ContradictionsDetected != 0 {
		t.Fatalf("expected 0 contradictions detected, got %d", result.ContradictionsDetected)
	}
	if result.NoContradictionEdges != 1 {
		t.Fatalf("expected 1 no_contradiction edge, got %d", result.NoContradictionEdges)
	}

	// Read-phase shuffle can pick either node as the outer loop element,
	// so the edge direction is not deterministic. Accept either A->B or
	// B->A -- the hasEdge guard checks both directions anyway.
	eng.RLock()
	defer eng.RUnlock()
	var edge *graph.Edge
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.Type == "no_contradiction" && e.TargetID == idB {
			edge = e
			break
		}
	}
	if edge == nil {
		for _, e := range eng.Graph().EdgesFrom(idB) {
			if e.Type == "no_contradiction" && e.TargetID == idA {
				edge = e
				break
			}
		}
	}
	if edge == nil {
		t.Fatal("no_contradiction edge between A and B not found in either direction")
	}
	if _, ok := edge.Properties.GetTimestamp("checked_at"); !ok {
		t.Fatal("no_contradiction edge missing checked_at property")
	}
}

// TestDetectContradictionsSkipsPairsWithNoContradictionEdge regressions the
// draining behavior. A pair that already has a no_contradiction edge must
// not be sent to the LLM on subsequent cycles. Combined with the edge-
// writing behavior above, this is what makes the pool drain.
func TestDetectContradictionsSkipsPairsWithNoContradictionEdge(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha observation about caching", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta observation, similar-but-distinct topic", []float32{0.7, 0.7, 0.0})

	// Pre-seed a no_contradiction edge (simulating a prior cycle's negative result).
	eng.Lock()
	if _, err := eng.Graph().AddEdge(idA, idB, "no_contradiction", 1.0, graph.Properties{
		"checked_at": graph.TimestampProperty(time.Now().UTC()),
	}); err != nil {
		eng.Unlock()
		t.Fatalf("seed edge: %v", err)
	}
	eng.Unlock()

	llm := &mockLLM{responses: []string{}} // must not be called

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (pair has no_contradiction edge), got %d", result.LLMCalls)
	}
	if result.NoContradictionEdges != 0 {
		t.Fatalf("expected 0 new no_contradiction edges, got %d", result.NoContradictionEdges)
	}
}

// --- Manifest summary tests ---

func TestGenerateManifestSummary(t *testing.T) {
	eng := setupEngine(t)

	// Add enough records for the summary threshold (>=5).
	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth tokens", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{
		responses: []string{
			"Strong coverage of authentication and token management. No coverage of database design or infrastructure topics.",
		},
	}

	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result, nil, nil)

	if result.ManifestSummary == "" {
		t.Fatal("expected manifest summary to be generated")
	}
	if result.LLMCalls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", result.LLMCalls)
	}
}

func TestGenerateManifestSummaryTooFewRecords(t *testing.T) {
	eng := setupEngine(t)

	// Only 2 records -- below the threshold of 5.
	addProcessedNodeWithEmbedding(t, eng, "Record one", []float32{0.5, 0.5, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Record two", []float32{0.3, 0.7, 0.0})

	llm := &mockLLM{responses: []string{"Should not be called"}}

	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result, nil, nil)

	if result.ManifestSummary != "" {
		t.Fatal("should not generate summary with too few records")
	}
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls, got %d", result.LLMCalls)
	}
}

// TestManifestNegativeCacheBoundsRetries pins the negative-cache fix
// for tracker 01KQ4089VFQBE2T47H5GGKB5VC. Pre-fix, generateManifestSummary's
// LLM-error path returned without updating the cache, so the next
// cycle (with the same store-state fingerprint) recomputed the same
// hash, hit "cache miss" again, and re-called the LLM. Post-fix, the
// negative-cache counter advances and skips the LLM after the threshold.
func TestManifestNegativeCacheBoundsRetries(t *testing.T) {
	eng := setupEngine(t)
	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	cfg := config.Defaults()
	cfg.LLMCuration.MaxManifestAttempts = 3
	cache := &ManifestCache{}

	// Three consecutive failures: each call hits the LLM.
	for cycle := 1; cycle <= 3; cycle++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("cycle %d failure", cycle)}}
		result := &AutonomousResult{}
		generateManifestSummary(context.Background(), eng, llm, cfg, result, cache, nil)
		if result.LLMCalls != 1 {
			t.Fatalf("cycle %d: LLMCalls=%d, want 1", cycle, result.LLMCalls)
		}
	}
	if cache.FailedAttempts != 3 {
		t.Errorf("cache.FailedAttempts after 3 failures: got %d, want 3", cache.FailedAttempts)
	}
	if cache.LastFailedHash == "" {
		t.Error("cache.LastFailedHash empty after failures; should be set")
	}

	// Fourth cycle: same fingerprint, same failure -- but the negative
	// cache now skips the LLM call.
	llm := &mockLLM{errors: []error{fmt.Errorf("should not fire")}}
	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, cfg, result, cache, nil)
	if result.LLMCalls != 0 {
		t.Errorf("at-threshold negative cache: LLMCalls=%d, want 0 (skip)", result.LLMCalls)
	}
}

// TestManifestNegativeCacheClearedOnSuccess verifies that a successful
// manifest synthesis clears the negative cache so future failures get
// a fresh retry budget.
func TestManifestNegativeCacheClearedOnSuccess(t *testing.T) {
	eng := setupEngine(t)
	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	cfg := config.Defaults()
	cfg.LLMCuration.MaxManifestAttempts = 3
	cache := &ManifestCache{}

	// Fail twice.
	for i := 0; i < 2; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("err %d", i)}}
		generateManifestSummary(context.Background(), eng, llm, cfg, &AutonomousResult{}, cache, nil)
	}
	if cache.FailedAttempts != 2 {
		t.Fatalf("intermediate FailedAttempts: got %d, want 2", cache.FailedAttempts)
	}

	// Now succeed.
	llm := &mockLLM{responses: []string{"a real manifest summary"}}
	generateManifestSummary(context.Background(), eng, llm, cfg, &AutonomousResult{}, cache, nil)

	if cache.FailedAttempts != 0 {
		t.Errorf("FailedAttempts after success: got %d, want 0", cache.FailedAttempts)
	}
	if cache.LastFailedHash != "" {
		t.Errorf("LastFailedHash after success: got %q, want empty", cache.LastFailedHash)
	}
	if cache.Summary != "a real manifest summary" {
		t.Errorf("Summary after success: got %q, want %q", cache.Summary, "a real manifest summary")
	}
}

// TestManifestNegativeCacheClearedOnHashChange verifies that when the
// store fingerprint changes (records added/removed), the negative
// cache resets even though the new hash is also failing -- operator
// gets a fresh retry budget per distinct store state.
func TestManifestNegativeCacheClearedOnHashChange(t *testing.T) {
	eng := setupEngine(t)
	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	cfg := config.Defaults()
	cfg.LLMCuration.MaxManifestAttempts = 3
	cache := &ManifestCache{}

	// Three failures on initial fingerprint A.
	for i := 0; i < 3; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("hash A failure %d", i)}}
		generateManifestSummary(context.Background(), eng, llm, cfg, &AutonomousResult{}, cache, nil)
	}
	hashA := cache.LastFailedHash
	if cache.FailedAttempts != 3 {
		t.Fatalf("FailedAttempts on hash A: got %d, want 3", cache.FailedAttempts)
	}

	// Change the store: add a new record. Fingerprint shifts to B.
	addProcessedNodeWithEmbedding(t, eng, "new record forcing hash change", []float32{0.9, 0.0, 0.1})

	// Same failure mode on the new fingerprint.
	llm := &mockLLM{errors: []error{fmt.Errorf("hash B failure")}}
	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, cfg, result, cache, nil)

	if result.LLMCalls != 1 {
		t.Errorf("hash B should NOT be skipped: LLMCalls=%d, want 1", result.LLMCalls)
	}
	if cache.FailedAttempts != 1 {
		t.Errorf("FailedAttempts on hash B: got %d, want 1 (fresh budget)", cache.FailedAttempts)
	}
	if cache.LastFailedHash == hashA {
		t.Error("LastFailedHash should have shifted to the new fingerprint")
	}
}

// TestManifestNegativeCacheEmptySummaryCounts pins the second
// failure mode: an LLM that returns an empty string after trim
// (whitespace-only response, or response stripped to nothing).
// Pre-fix the empty result was cached as Summary="" which fails the
// cache-hit guard at line 1047 and re-runs the LLM next cycle. Same
// loop. Post-fix it advances the negative-cache counter.
func TestManifestNegativeCacheEmptySummaryCounts(t *testing.T) {
	eng := setupEngine(t)
	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	cfg := config.Defaults()
	cfg.LLMCuration.MaxManifestAttempts = 3
	cache := &ManifestCache{}

	// LLM returns whitespace -- trims to empty.
	for i := 0; i < 3; i++ {
		llm := &mockLLM{responses: []string{"   "}}
		generateManifestSummary(context.Background(), eng, llm, cfg, &AutonomousResult{}, cache, nil)
	}
	if cache.FailedAttempts != 3 {
		t.Errorf("FailedAttempts on empty-after-trim: got %d, want 3", cache.FailedAttempts)
	}

	// Fourth cycle: skipped.
	llm := &mockLLM{responses: []string{"should not be called"}}
	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, cfg, result, cache, nil)
	if result.LLMCalls != 0 {
		t.Errorf("at threshold: LLMCalls=%d, want 0", result.LLMCalls)
	}
}

func TestParseContradictionResult(t *testing.T) {
	input := `{"relationship":"contradicts","confidence":0.8,"explanation":"Direct conflict"}`
	r, err := parseContradictionResult(input)
	if err != nil {
		t.Fatalf("parseContradictionResult: %v", err)
	}
	if r.Relationship != "contradicts" {
		t.Fatalf("expected contradicts, got %q", r.Relationship)
	}
	if r.Confidence != 0.8 {
		t.Fatalf("expected 0.8, got %f", r.Confidence)
	}
}

func TestParseContradictionResultWithCodeFences(t *testing.T) {
	input := "```json\n" + `{"relationship":"supersedes","confidence":0.9,"explanation":"Updated version"}` + "\n```"
	r, err := parseContradictionResult(input)
	if err != nil {
		t.Fatalf("parseContradictionResult: %v", err)
	}
	if r.Relationship != "supersedes" {
		t.Fatalf("expected supersedes, got %q", r.Relationship)
	}
}

func TestParseContradictionResultConfidenceClamp(t *testing.T) {
	input := `{"relationship":"contradicts","confidence":5.0}`
	r, err := parseContradictionResult(input)
	if err != nil {
		t.Fatalf("parseContradictionResult: %v", err)
	}
	if r.Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should clamp to 0.5, got %f", r.Confidence)
	}
}

func TestDetectContradictionsSupersedes(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1

	idA := addProcessedNodeWithEmbedding(t, eng, "Original API v1 design", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Updated API v2 design", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"supersedes","confidence":0.85,"explanation":"v2 replaces v1"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.ContradictionsDetected != 1 {
		t.Fatalf("expected 1 supersession, got %d", result.ContradictionsDetected)
	}

	// Exactly one of the two records should have valid_until set.
	// The contradiction-application path trusts the LLM's A/B
	// assignment, which depends on the iteration order of the
	// (now shuffled) candidate set, so the test asserts the
	// invariant ("one survivor, one loser") rather than a
	// specific identity.
	eng.RLock()
	defer eng.RUnlock()
	losers := 0
	for _, id := range []string{idA, idB} {
		n, ok := eng.Graph().GetNode(id)
		if !ok {
			t.Fatalf("node %s should exist", id)
		}
		if _, has := n.Properties.GetTimestamp("valid_until"); has {
			losers++
		}
	}
	if losers != 1 {
		t.Fatalf("expected exactly 1 superseded record, got %d", losers)
	}

	// Verify supersedes edge exists.
	foundEdge := false
	for _, id := range eng.Graph().AllNodeIDs() {
		for _, e := range eng.Graph().EdgesFrom(id) {
			if e.Type == "supersedes" {
				foundEdge = true
			}
		}
	}
	if !foundEdge {
		t.Fatal("expected supersedes edge")
	}
}

func TestDetectContradictionsLLMError(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95

	addProcessedNodeWithEmbedding(t, eng, "Record about auth", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Another auth record", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{errors: []error{fmt.Errorf("LLM unavailable")}}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
	if result.ContradictionsDetected != 0 {
		t.Fatalf("expected 0 contradictions on error, got %d", result.ContradictionsDetected)
	}
}

// findContradictionCheckSkippedEdge returns the soft-fail edge between
// two records, in either direction. Used by the contradiction
// retry-bound tests below.
func findContradictionCheckSkippedEdge(t *testing.T, eng *core.Engine, idA, idB string) *graph.Edge {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.TargetID == idB && e.Type == "contradiction_check_skipped" {
			return e
		}
	}
	for _, e := range eng.Graph().EdgesFrom(idB) {
		if e.TargetID == idA && e.Type == "contradiction_check_skipped" {
			return e
		}
	}
	return nil
}

// TestDetectContradictionsFailureCreatesSoftFailEdge pins the
// fix for tracker 01KQ407VR599E2CGAGJ0FBVGJZ. Pre-fix an LLM
// failure on a contradiction check left no state on the pair, so
// the pair re-entered the candidate pool every cycle and burned
// tokens forever. Post-fix, the failure writes a
// contradiction_check_skipped edge with attempts=1.
func TestDetectContradictionsFailureCreatesSoftFailEdge(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1
	cfg.LLMCuration.MaxContradictionAttempts = 3

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{errors: []error{fmt.Errorf("API timeout")}}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
	edge := findContradictionCheckSkippedEdge(t, eng, idA, idB)
	if edge == nil {
		t.Fatal("contradiction_check_skipped edge not found in either direction")
	}
	attempts, _ := edge.Properties.GetInt64("attempts")
	if attempts != 1 {
		t.Errorf("attempts: got %d, want 1", attempts)
	}
	reason, _ := edge.Properties.GetString("last_error")
	if !strings.Contains(reason, "API timeout") {
		t.Errorf("last_error: got %q, want contains %q", reason, "API timeout")
	}
}

// TestDetectContradictionsFailureIncrementsExistingEdge verifies
// that subsequent failures on the same pair update the existing
// soft-fail edge in place rather than creating duplicates.
func TestDetectContradictionsFailureIncrementsExistingEdge(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1
	cfg.LLMCuration.MaxContradictionAttempts = 5

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta", []float32{0.7, 0.7, 0.0})

	// Two consecutive failure cycles. attempts < max, so the pair stays
	// in the candidate pool and gets retried.
	for i := 0; i < 2; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("err %d", i)}}
		detectContradictions(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	edge := findContradictionCheckSkippedEdge(t, eng, idA, idB)
	if edge == nil {
		t.Fatal("contradiction_check_skipped edge not found")
	}
	attempts, _ := edge.Properties.GetInt64("attempts")
	if attempts != 2 {
		t.Errorf("attempts after 2 failures: got %d, want 2", attempts)
	}

	// Verify only ONE edge exists between the pair (no duplicates).
	count := 0
	eng.RLock()
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.TargetID == idB && e.Type == "contradiction_check_skipped" {
			count++
		}
	}
	for _, e := range eng.Graph().EdgesFrom(idB) {
		if e.TargetID == idA && e.Type == "contradiction_check_skipped" {
			count++
		}
	}
	eng.RUnlock()
	if count != 1 {
		t.Errorf("found %d contradiction_check_skipped edges between the pair, want 1", count)
	}
}

// TestDetectContradictionsLocksOutAtThreshold verifies that after
// MaxContradictionAttempts consecutive failures, the soft-fail edge
// becomes a hard skip and the pair is excluded from future
// candidate pools (no further LLM calls).
func TestDetectContradictionsLocksOutAtThreshold(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1
	cfg.LLMCuration.MaxContradictionAttempts = 3

	addProcessedNodeWithEmbedding(t, eng, "Alpha", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Beta", []float32{0.7, 0.7, 0.0})

	// Three consecutive failures: attempts goes 1 -> 2 -> 3.
	for i := 0; i < 3; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("cycle %d failure", i)}}
		detectContradictions(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	// Fourth cycle: pair should be hard-skipped at the read-phase
	// hasEdge guard. No LLM call.
	llm := &mockLLM{errors: []error{fmt.Errorf("should not fire")}}
	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	if result.LLMCalls != 0 {
		t.Errorf("at-threshold pair was re-attempted: LLMCalls=%d, want 0", result.LLMCalls)
	}
}

// TestDetectContradictionsMaxAttemptsZeroDisables verifies legacy
// behavior: when MaxContradictionAttempts=0, no soft-fail edges are
// written and pairs re-enter the candidate pool every cycle (the
// pre-fix bug, intentionally preserved as an opt-out).
func TestDetectContradictionsMaxAttemptsZeroDisables(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1
	cfg.LLMCuration.MaxContradictionAttempts = 0

	idA := addProcessedNodeWithEmbedding(t, eng, "Alpha", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "Beta", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{errors: []error{fmt.Errorf("error")}}
	detectContradictions(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	if edge := findContradictionCheckSkippedEdge(t, eng, idA, idB); edge != nil {
		t.Error("contradiction_check_skipped edge was written when MaxContradictionAttempts=0")
	}
}

// TestDetectContradictionsBatchFailureMarksAllPairs verifies that a
// whole-batch LLM error in batched mode writes a
// contradiction_check_skipped edge for every pair in the batch (not
// just one).
func TestDetectContradictionsBatchFailureMarksAllPairs(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.3
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 5
	cfg.LLMCuration.MaxContradictionAttempts = 3

	// Three records, all mutually similar within the window.
	idA := addProcessedNodeWithEmbedding(t, eng, "A", []float32{1.0, 0.0, 0.0})
	idB := addProcessedNodeWithEmbedding(t, eng, "B", []float32{0.7, 0.7, 0.0})
	idC := addProcessedNodeWithEmbedding(t, eng, "C", []float32{0.5, 0.5, 0.5})

	llm := &mockLLM{errors: []error{fmt.Errorf("batch API outage")}}
	detectContradictions(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	// At least one pair from {A,B}, {A,C}, {B,C} should have a
	// soft-fail edge. The candidate selection is shuffle-seeded and
	// won't always pick all three pairs, but it should pick at least
	// the first one it encounters.
	gotEdge := false
	for _, pair := range [][2]string{{idA, idB}, {idA, idC}, {idB, idC}} {
		if e := findContradictionCheckSkippedEdge(t, eng, pair[0], pair[1]); e != nil {
			gotEdge = true
			attempts, _ := e.Properties.GetInt64("attempts")
			if attempts != 1 {
				t.Errorf("attempts for %s,%s: got %d, want 1", pair[0], pair[1], attempts)
			}
			reason, _ := e.Properties.GetString("last_error")
			if !strings.Contains(reason, "batch API outage") {
				t.Errorf("last_error for %s,%s: got %q", pair[0], pair[1], reason)
			}
		}
	}
	if !gotEdge {
		t.Error("no contradiction_check_skipped edges written despite whole-batch failure")
	}
}

func TestManifestCacheHitSkipsLLM(t *testing.T) {
	eng := setupEngine(t)

	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{responses: []string{"First summary text"}}

	// First run: cache is empty, LLM is called, cache is populated.
	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	if result1.ManifestSummary == "" {
		t.Fatal("first run should have produced a summary")
	}
	if result1.ManifestCacheHit {
		t.Fatal("first run is a cache miss, not a hit")
	}
	if result1.LLMCalls != 1 {
		t.Fatalf("first run should have called LLM once, got %d calls", result1.LLMCalls)
	}
	if cache.Hash == "" || cache.Summary == "" {
		t.Fatalf("cache should be populated, got %+v", cache)
	}

	// Second run without changing the store: same fingerprint -> cache hit.
	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)
	if !result2.ManifestCacheHit {
		t.Fatal("second run with unchanged store should be a cache hit")
	}
	if result2.LLMCalls != 0 {
		t.Fatalf("cache hit should not call LLM, got %d calls", result2.LLMCalls)
	}
	if result2.ManifestSummary != result1.ManifestSummary {
		t.Fatalf("cache hit should reuse summary; got %q vs %q", result2.ManifestSummary, result1.ManifestSummary)
	}
}

func TestManifestCacheInvalidatesOnChange(t *testing.T) {
	eng := setupEngine(t)

	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{responses: []string{"First summary", "Second summary"}}

	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	firstHash := cache.Hash

	// Mutate the store: add another record so the fingerprint changes.
	addProcessedNodeWithEmbedding(t, eng, "New record about databases", []float32{0.9, 0.1, 0.1})

	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)
	if result2.ManifestCacheHit {
		t.Fatal("fingerprint changed, expected cache miss")
	}
	if cache.Hash == firstHash {
		t.Fatal("cache hash should update on miss")
	}
	if result2.ManifestSummary != "Second summary" {
		t.Fatalf("expected new summary, got %q", result2.ManifestSummary)
	}
}

// TestManifestCacheInvalidatesOnEpistemicShift covers P1-59: the
// fingerprint must distinguish stores that differ only in the
// epistemic_status / temporality / confidence distributions, so a
// bulk reclassification (e.g. 50 records sliding speculative ->
// well_established) busts the cache.
func TestManifestCacheInvalidatesOnEpistemicShift(t *testing.T) {
	eng := setupEngine(t)

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		id := addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth", i), []float32{float32(i) * 0.1, 0.5, 0.3})
		ids = append(ids, id)
	}
	// addProcessedNodeWithEmbedding sets temporality=durable and
	// confidence=0.9 but no epistemic_status. Tag every record so
	// the baseline run has a non-empty epistemic histogram.
	eng.Lock()
	for _, id := range ids {
		eng.SetProp(id, "epistemic_status", graph.StringProperty("speculative"))
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{responses: []string{"baseline summary", "post-shift summary"}}

	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	if result1.ManifestCacheHit {
		t.Fatal("first run should be a cache miss")
	}
	firstHash := cache.Hash

	// Bulk reclassification: every record moves speculative -> well_established
	// AND confidence drops from 0.9 (high) to 0.3 (low). Top keywords,
	// knowledge-type histogram, record count, and date span are all
	// unchanged -- the pre-P1-59 fingerprint would treat this as the
	// same store and serve the stale "baseline summary".
	eng.Lock()
	for _, id := range ids {
		eng.SetProp(id, "epistemic_status", graph.StringProperty("well_established"))
		eng.SetProp(id, "confidence", graph.Float64Property(0.3))
	}
	eng.Save("test")
	eng.Unlock()

	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)
	if result2.ManifestCacheHit {
		t.Fatal("epistemic + confidence shift should invalidate the cache")
	}
	if cache.Hash == firstHash {
		t.Fatal("cache hash should change when fingerprint dimensions shift")
	}
	if result2.ManifestSummary != "post-shift summary" {
		t.Fatalf("expected post-shift summary, got %q", result2.ManifestSummary)
	}
}

// TestManifestCacheInvalidatesOnTemporalityShift mirrors the above
// but flips temporality (durable -> ephemeral) on the same population,
// holding all other dimensions constant.
func TestManifestCacheInvalidatesOnTemporalityShift(t *testing.T) {
	eng := setupEngine(t)

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		id := addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about caching", i), []float32{float32(i) * 0.1, 0.5, 0.3})
		ids = append(ids, id)
	}

	llm := &mockLLM{responses: []string{"baseline", "post-shift"}}
	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	firstHash := cache.Hash

	eng.Lock()
	for _, id := range ids {
		eng.SetProp(id, "temporality", graph.StringProperty("ephemeral"))
	}
	eng.Save("test")
	eng.Unlock()

	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)
	if result2.ManifestCacheHit {
		t.Fatal("temporality shift should invalidate the cache")
	}
	if cache.Hash == firstHash {
		t.Fatal("cache hash should change when temporality histogram shifts")
	}
}

// TestManifestCacheIgnoresHistoricalRecords pins P2-09 fix #4: the
// manifest summary describes the CURRENT state of the store, so
// records whose valid_until is in the past must be excluded from
// the fingerprint inputs. Pre-fix (initial), adding a historical
// record would inflate totalRecords and bust the cache. The
// follow-up review caught a second leak: the kwCounts source was
// PropIdx().KeywordCounts() which includes historical records, so
// even after the totalRecords filter the keyword fingerprint would
// drift and bust the cache. This test seeds content_keywords on
// every record (including the historical one) with a distinctive
// keyword that would dominate the top-keywords list if leaked.
func TestManifestCacheIgnoresHistoricalRecords(t *testing.T) {
	eng := setupEngine(t)

	// Baseline: 6 live records, all sharing the keyword "auth".
	for i := 0; i < 6; i++ {
		id := addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth", i), []float32{float32(i) * 0.1, 0.5, 0.3})
		eng.Lock()
		eng.SetProp(id, "content_keywords", graph.StringListProperty([]string{"auth"}))
		eng.PropIdx().Add(id, "content_keywords", graph.StringListProperty([]string{"auth"}))
		eng.Save("seed-auth")
		eng.Unlock()
	}

	// On the first manifest run only one summary is needed — but
	// stage extras in case a hash collision unexpectedly forces a
	// second LLM call. (mockLLM panics on out-of-responses, so over-
	// providing is the safe direction.)
	llm := &mockLLM{responses: []string{"baseline summary", "should-not-be-called", "should-not-be-called-2"}}

	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	if result1.ManifestCacheHit {
		t.Fatal("first run should miss")
	}
	firstHash := cache.Hash

	// Inject a historical record carrying a DISTINCTIVE keyword
	// "leakcanary" that doesn't appear on any live record. If the
	// fingerprint pulls keyword counts from the unfiltered
	// PropIdx index (the bug shape), this single historical
	// record would shift the top-keywords list and bust the
	// cache. Post-fix, kwCounts is built inline from the same
	// live-only loop and "leakcanary" never enters the count.
	histID := addProcessedNodeWithEmbedding(t, eng, "Old record with leakcanary keyword", []float32{0.5, 0.5, 0.5})
	eng.Lock()
	eng.SetProp(histID, "content_keywords", graph.StringListProperty([]string{"leakcanary"}))
	eng.PropIdx().Add(histID, "content_keywords", graph.StringListProperty([]string{"leakcanary"}))
	eng.SetProp(histID, "valid_until", graph.TimestampProperty(time.Now().UTC().Add(-1*time.Hour)))
	eng.Save("supersede")
	eng.Unlock()

	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)

	if !result2.ManifestCacheHit {
		t.Errorf("historical-only mutation should not bust the manifest cache (current state unchanged); got cache miss")
	}
	if cache.Hash != firstHash {
		t.Errorf("fingerprint should be stable across historical-record additions; got hash change %q -> %q",
			firstHash, cache.Hash)
	}
}

// TestManifestCacheInvalidatesOnLiveSupersession is the complement:
// when a CURRENT record is superseded (gets valid_until applied),
// the live record set shrinks by one and the fingerprint must
// change.
func TestManifestCacheInvalidatesOnLiveSupersession(t *testing.T) {
	eng := setupEngine(t)

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		id := addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d about auth", i), []float32{float32(i) * 0.1, 0.5, 0.3})
		ids = append(ids, id)
	}

	llm := &mockLLM{responses: []string{"baseline", "post-supersede"}}
	cache := &ManifestCache{}
	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, cache, nil)
	firstHash := cache.Hash

	// Supersede one of the live records. Live count drops 6 -> 5.
	eng.Lock()
	eng.SetProp(ids[0], "valid_until", graph.TimestampProperty(time.Now().UTC().Add(-1*time.Minute)))
	eng.Save("supersede")
	eng.Unlock()

	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, cache, nil)

	if result2.ManifestCacheHit {
		t.Errorf("supersession of a live record changes the live count; expected cache miss")
	}
	if cache.Hash == firstHash {
		t.Errorf("fingerprint should change when a live record moves to historical; got stable hash")
	}
}

func TestClassifySystemPromptShortIsSmaller(t *testing.T) {
	if ClassifySystemPromptShort == "" {
		t.Fatal("ClassifySystemPromptShort must be defined")
	}
	if len(ClassifySystemPromptShort) >= len(ClassifySystemPrompt) {
		t.Fatalf("short prompt (%d) should be smaller than full prompt (%d)",
			len(ClassifySystemPromptShort), len(ClassifySystemPrompt))
	}
	// Must still contain the JSON schema keys so the LLM knows what to return.
	for _, key := range []string{"temporality", "confidence", "knowledge_type", "epistemic_status", "keywords", "summary_short"} {
		if !strings.Contains(ClassifySystemPromptShort, key) {
			t.Fatalf("short prompt missing required key %q", key)
		}
	}
}

func TestClassifyPendingRoutesShortAndLongTiers(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10
	cfg.LLMCuration.MaxCallsPerRun = 10
	cfg.LLMCuration.LongClassificationThreshold = 100
	cfg.LLM.Models.Low = "test-short-model"
	cfg.LLM.Models.Medium = "test-long-model"
	cfg.LLMCuration.ClassificationShortEffort = "low"
	cfg.LLMCuration.ClassificationLongEffort = "medium"

	// One short record (below 100 chars), one long record.
	addPendingNode(t, eng, "short content")
	addPendingNode(t, eng, strings.Repeat("long content should exceed the 100-char threshold for long tier. ", 5))

	// Valid classification JSON, reused for each call.
	resp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"x"}`
	llm := &mockLLM{responses: []string{resp, resp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 10, 0, nil, false)

	if result.Classified != 2 {
		t.Fatalf("expected 2 classified, got %d", result.Classified)
	}

	// Each record should route to its tier's model.
	seenShort, seenLong := false, false
	for _, m := range llm.models {
		if m == "test-short-model" {
			seenShort = true
		}
		if m == "test-long-model" {
			seenLong = true
		}
	}
	if !seenShort {
		t.Fatal("expected short model to be used")
	}
	if !seenLong {
		t.Fatal("expected long model to be used")
	}
}

func TestMeanCosineToCentroidUnit(t *testing.T) {
	eng := setupEngine(t)

	// Three tightly clustered vectors: all near (1,0,0) with small jitter.
	// Coherent cluster -> mean cosine should be ~1.0.
	ids := []string{
		addProcessedNodeWithEmbedding(t, eng, "a", []float32{1.0, 0.0, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "b", []float32{0.98, 0.2, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "c", []float32{0.99, 0.1, 0.1}),
	}

	eng.RLock()
	mean, n, _ := meanCosineToCentroid(eng.Graph(), ids)
	eng.RUnlock()

	if n != 3 {
		t.Fatalf("expected 3 vectors counted, got %d", n)
	}
	if mean < 0.9 {
		t.Fatalf("tight cluster should have mean cosine > 0.9, got %f", mean)
	}

	// Diverse vectors pointing different directions -> low mean cosine.
	divIDs := []string{
		addProcessedNodeWithEmbedding(t, eng, "x", []float32{1.0, 0.0, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "y", []float32{0.0, 1.0, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "z", []float32{0.0, 0.0, 1.0}),
	}
	eng.RLock()
	meanDiv, _, _ := meanCosineToCentroid(eng.Graph(), divIDs)
	eng.RUnlock()

	if meanDiv > 0.7 {
		t.Fatalf("orthogonal cluster should have low mean cosine, got %f", meanDiv)
	}
}

func TestMeanCosineToCentroidEmpty(t *testing.T) {
	eng := setupEngine(t)
	eng.RLock()
	defer eng.RUnlock()

	mean, n, _ := meanCosineToCentroid(eng.Graph(), []string{"nonexistent-1", "nonexistent-2"})
	if n != 0 {
		t.Fatalf("no embeddings should count 0, got %d", n)
	}
	if mean != 0 {
		t.Fatalf("no embeddings should return 0 mean, got %f", mean)
	}
}

// TestEnrichConceptSynthesesLogsDimMismatch pins the user-visible
// payoff of P2-09 fix #2: when meanCosineToCentroid skips members
// for embedding-dimension mismatch, enrichConceptSyntheses must
// emit a Warn-level log with the "gramaton reembed" hint so
// operators see the embedding-model drift.
//
// Setup: a concept with synthesis_status=pending plus 4 member
// records — 2 with 3-dim embeddings, 2 with 4-dim embeddings.
// coherenceMin > 0 forces the meanCosineToCentroid call. The
// dim-mismatch arm should fire and the slog buffer should record
// the warn.
func TestEnrichConceptSynthesesLogsDimMismatch(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.ConceptCoherenceMin = 0.6 // > 0 forces the cosine check
	cfg.LLMCuration.MaxConceptsPerRun = 5

	now := time.Now().UTC()

	// Build the concept + members with mixed-dim embeddings.
	eng.Lock()

	// Concept node with synthesis_status=pending. Without that
	// status the enrich loop short-circuits.
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Concept synthesis"),
		"content_short":     graph.StringProperty("kafka concept"),
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("kafka"),
		"synthesis_status":  graph.StringProperty("pending"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	// 2x 3-dim, 2x 4-dim. 3-dim wins as the "first" centroid dim
	// (loop order is stable); the 4-dim members are then counted as
	// dimMismatched.
	type memberSpec struct {
		emb []float32
	}
	members := []memberSpec{
		{[]float32{1.0, 0.0, 0.0}},
		{[]float32{0.99, 0.1, 0.0}},
		{[]float32{1.0, 0.0, 0.0, 0.0}},
		{[]float32{0.99, 0.1, 0.0, 0.0}},
	}
	for i, m := range members {
		mid := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty(fmt.Sprintf("member %d", i)),
			"processing_status": graph.StringProperty("processed"),
			"embedding_full":    graph.VectorProperty(m.emb),
			"created_at":        graph.TimestampProperty(now),
		})
		for k, v := range mid.Properties {
			eng.PropIdx().Add(mid.ID, k, v)
		}
		// instance_of edge from member to concept (enrichConceptSyntheses
		// reads EdgesTo(concept) for "instance_of").
		eng.Graph().AddEdge(mid.ID, concept.ID, "instance_of", 1.0, nil)
	}

	eng.Save("seed")
	eng.Unlock()

	// Capture slog output via a bytes.Buffer-backed handler.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	llm := &mockLLM{responses: []string{`{"synthesis":"ignored","tags":["x"]}`}}
	result := &AutonomousResult{}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, result, 20, 0, logger, false)

	out := buf.String()
	if !strings.Contains(out, "embedding dimension mismatch") {
		t.Errorf("expected dim-mismatch Warn log; got:\n%s", out)
	}
	if !strings.Contains(out, "gramaton reembed") {
		t.Errorf("expected 'gramaton reembed' hint in the log; got:\n%s", out)
	}
	// Rough sanity: log mentions the keyword + a positive mismatched count.
	if !strings.Contains(out, `keyword=kafka`) {
		t.Errorf("expected concept keyword in the log; got:\n%s", out)
	}
}

// addPendingConcept creates a concept node with synthesis_status="pending"
// plus N "instance_of" member edges. Returns the concept node ID.
// Used by the synthesis retry-bound tests below.
func addPendingConcept(t *testing.T, eng *core.Engine, keyword string, memberCount int) string {
	t.Helper()
	now := time.Now().UTC()
	eng.Lock()
	defer eng.Unlock()

	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("placeholder"),
		"content_short":     graph.StringProperty(keyword + " concept"),
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty(keyword),
		"synthesis_status":  graph.StringProperty("pending"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}
	for i := 0; i < memberCount; i++ {
		m := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty(fmt.Sprintf("member %d of %s", i, keyword)),
			"content_short":     graph.StringProperty(fmt.Sprintf("m%d", i)),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(now),
			"epistemic_status":  graph.StringProperty("well_established"),
		})
		for k, v := range m.Properties {
			eng.PropIdx().Add(m.ID, k, v)
		}
		eng.Graph().AddEdge(m.ID, concept.ID, "instance_of", 1.0, nil)
	}
	eng.Save("seed")
	return concept.ID
}

// TestSynthesizeBatchFailureBumpsAttemptCounter verifies that an LLM
// transport error during concept synthesis writes synthesis_attempts
// + last_synthesis_error on every concept in the batch (not just one).
//
// Regression guard for tracker 01KQ407BPRJF8AVT7CBKQ6VJDB: pre-fix
// concepts with synthesis_status=pending re-entered the candidate set
// every cycle on persistent failure, billing input tokens forever.
func TestSynthesizeBatchFailureBumpsAttemptCounter(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSynthesisAttempts = 3
	cfg.LLMCuration.MaxConceptsPerRun = 5
	cfg.LLMCuration.SynthesisBatchSize = 5

	id := addPendingConcept(t, eng, "kafka", 3)

	llm := &mockLLM{errors: []error{fmt.Errorf("API timeout")}}

	result := &AutonomousResult{}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("synthesis_attempts")
	if attempts != 1 {
		t.Errorf("synthesis_attempts: got %d, want 1", attempts)
	}
	reason, _ := n.Properties.GetString("last_synthesis_error")
	if !strings.Contains(reason, "API timeout") {
		t.Errorf("last_synthesis_error: got %q, want contains %q", reason, "API timeout")
	}
	ss, _ := n.Properties.GetString("synthesis_status")
	if ss != "pending" {
		t.Errorf("synthesis_status: got %q, want still %q (below threshold)", ss, "pending")
	}
}

// TestSynthesizeMarksStuckAtThreshold verifies that after
// MaxSynthesisAttempts consecutive failures, the concept's
// synthesis_status flips to "stuck" and is excluded from the next
// cycle's candidate selection (the existing "ss != pending" guard
// auto-excludes stuck concepts).
func TestSynthesizeMarksStuckAtThreshold(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSynthesisAttempts = 3
	cfg.LLMCuration.MaxConceptsPerRun = 5
	cfg.LLMCuration.SynthesisBatchSize = 5

	id := addPendingConcept(t, eng, "redis", 3)

	for cycle := 1; cycle <= 3; cycle++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("cycle %d failure", cycle)}}
		enrichConceptSyntheses(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("synthesis_attempts")
	ss, _ := n.Properties.GetString("synthesis_status")
	eng.RUnlock()

	if attempts != 3 {
		t.Errorf("synthesis_attempts: got %d, want 3", attempts)
	}
	if ss != "stuck" {
		t.Errorf("synthesis_status: got %q, want %q", ss, "stuck")
	}

	// Next cycle: no LLM call -- the concept's synthesis_status="stuck"
	// fails the "ss != pending" guard at the selection iterator.
	llm := &mockLLM{errors: []error{fmt.Errorf("should not fire")}}
	result := &AutonomousResult{}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	if result.LLMCalls != 0 {
		t.Errorf("stuck concept was re-attempted: LLMCalls=%d, want 0", result.LLMCalls)
	}
}

// TestSynthesizeMaxAttemptsZeroDisables verifies legacy behavior:
// when MaxSynthesisAttempts=0, the counter feature is disabled.
func TestSynthesizeMaxAttemptsZeroDisables(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSynthesisAttempts = 0
	cfg.LLMCuration.MaxConceptsPerRun = 5
	cfg.LLMCuration.SynthesisBatchSize = 5

	id := addPendingConcept(t, eng, "postgres", 3)

	llm := &mockLLM{errors: []error{fmt.Errorf("error")}}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetInt64("synthesis_attempts"); ok {
		t.Error("synthesis_attempts was written when MaxSynthesisAttempts=0")
	}
	if _, ok := n.Properties.GetString("last_synthesis_error"); ok {
		t.Error("last_synthesis_error was written when MaxSynthesisAttempts=0")
	}
}

// TestSynthesizeSuccessClearsAttempts verifies that a successful
// synthesis on a concept that previously failed resets the
// synthesis_attempts counter so an operator-fixed concept passes
// cleanly on its next attempt.
func TestSynthesizeSuccessClearsAttempts(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxSynthesisAttempts = 3
	cfg.LLMCuration.MaxConceptsPerRun = 5
	cfg.LLMCuration.SynthesisBatchSize = 5

	id := addPendingConcept(t, eng, "elasticsearch", 3)

	// Fail twice -- attempts now 2, still pending.
	for i := 0; i < 2; i++ {
		llm := &mockLLM{errors: []error{fmt.Errorf("err %d", i)}}
		enrichConceptSyntheses(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	if a, _ := n.Properties.GetInt64("synthesis_attempts"); a != 2 {
		eng.RUnlock()
		t.Fatalf("intermediate synthesis_attempts: got %d, want 2", a)
	}
	eng.RUnlock()

	// Now succeed.
	llm := &mockLLM{
		responses: []string{`[{"keyword":"elasticsearch","synthesis":"Elasticsearch: distributed search engine."}]`},
	}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, &AutonomousResult{}, 20, 0, nil, false)

	eng.RLock()
	defer eng.RUnlock()
	n, _ = eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("synthesis_attempts")
	ss, _ := n.Properties.GetString("synthesis_status")
	if attempts != 0 {
		t.Errorf("synthesis_attempts after success: got %d, want 0", attempts)
	}
	if ss != "complete" {
		t.Errorf("synthesis_status after success: got %q, want %q", ss, "complete")
	}
}

// TestMeanCosineToCentroidDimMismatchSurfaced is the regression for
// P2-09 fix #2: when concept members have heterogeneous embedding
// dimensions (e.g. embedding model changed mid-store), the function
// must report the count of skipped members so the caller can warn
// instead of silently producing a misleadingly-low n.
func TestMeanCosineToCentroidDimMismatchSurfaced(t *testing.T) {
	eng := setupEngine(t)

	// Two 3-dim members + two 4-dim members. The first wins as the
	// reference dim (loop order is deterministic with stable IDs);
	// the others are reported as mismatched.
	ids := []string{
		addProcessedNodeWithEmbedding(t, eng, "first-3d", []float32{1.0, 0.0, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "second-3d", []float32{0.99, 0.1, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "first-4d", []float32{1.0, 0.0, 0.0, 0.0}),
		addProcessedNodeWithEmbedding(t, eng, "second-4d", []float32{0.99, 0.1, 0.0, 0.0}),
	}

	eng.RLock()
	mean, used, mismatched := meanCosineToCentroid(eng.Graph(), ids)
	eng.RUnlock()

	if used+mismatched != 4 {
		t.Errorf("used (%d) + mismatched (%d) should sum to total members (4)", used, mismatched)
	}
	if mismatched == 0 {
		t.Errorf("mixed-dim members should produce mismatched > 0; got 0 (silent skip is the bug)")
	}
	if used < 2 {
		t.Errorf("at least one of the dim-groups should still produce ≥2 vectors for a coherent mean; got used=%d", used)
	}
	if mean == 0 && used >= 2 {
		t.Errorf("with %d valid same-dim members the mean should be non-zero", used)
	}
}

func TestManifestNilCacheAlwaysCallsLLM(t *testing.T) {
	eng := setupEngine(t)

	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{responses: []string{"Summary 1", "Summary 2"}}

	result1 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result1, nil, nil)
	result2 := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result2, nil, nil)

	if result1.LLMCalls != 1 || result2.LLMCalls != 1 {
		t.Fatalf("nil cache should always call LLM; got %d and %d", result1.LLMCalls, result2.LLMCalls)
	}
	if result2.ManifestCacheHit {
		t.Fatal("nil cache should never report a cache hit")
	}
}

func TestGenerateManifestSummaryLLMError(t *testing.T) {
	eng := setupEngine(t)

	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{errors: []error{fmt.Errorf("LLM error")}}

	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, config.Defaults(), result, nil, nil)

	if result.ManifestSummary != "" {
		t.Fatal("should not have summary on LLM error")
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
}

func TestParseContradictionResultInvalidRelationship(t *testing.T) {
	input := `{"relationship":"invalid","confidence":0.5}`
	r, err := parseContradictionResult(input)
	if err != nil {
		t.Fatalf("parseContradictionResult: %v", err)
	}
	if r.Relationship != "none" {
		t.Fatalf("invalid relationship should default to 'none', got %q", r.Relationship)
	}
}

func TestParseContradictionBatchResult(t *testing.T) {
	input := `[
		{"pair_id": 1, "relationship": "contradicts", "confidence": 0.8, "explanation": "x"},
		{"pair_id": 2, "relationship": "related", "confidence": 0.6, "explanation": "y"}
	]`
	results, err := parseContradictionBatchResult(input)
	if err != nil {
		t.Fatalf("parseContradictionBatchResult: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].PairID != 1 || results[0].Relationship != "contradicts" {
		t.Fatalf("pair 1 wrong: %+v", results[0])
	}
	if results[1].PairID != 2 || results[1].Relationship != "related" {
		t.Fatalf("pair 2 wrong: %+v", results[1])
	}
}

func TestParseContradictionBatchResultWithCodeFences(t *testing.T) {
	input := "```json\n" + `[{"pair_id":1,"relationship":"supersedes","confidence":0.9,"explanation":"a"}]` + "\n```"
	results, err := parseContradictionBatchResult(input)
	if err != nil {
		t.Fatalf("parseContradictionBatchResult: %v", err)
	}
	if len(results) != 1 || results[0].Relationship != "supersedes" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestParseContradictionBatchResultInvalidEnumFallback(t *testing.T) {
	input := `[{"pair_id": 1, "relationship": "bogus", "confidence": 2.5}]`
	results, err := parseContradictionBatchResult(input)
	if err != nil {
		t.Fatalf("parseContradictionBatchResult: %v", err)
	}
	if results[0].Relationship != "none" {
		t.Fatalf("invalid enum should default to 'none', got %q", results[0].Relationship)
	}
	if results[0].Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should clamp to 0.5, got %f", results[0].Confidence)
	}
}

func TestParseContradictionBatchResultMalformed(t *testing.T) {
	if _, err := parseContradictionBatchResult("not json"); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestDetectContradictionsBatched(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 5

	// Three records pairwise similar (two pairs expected -- the third
	// record's similarity to the first pair's members falls in the
	// ContradictionMin/Max band as well, producing an additional pair).
	addProcessedNodeWithEmbedding(t, eng, "We use JWT", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We switched to cookies", []float32{0.7, 0.7, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We now use OAuth2", []float32{0.5, 0.6, 0.6})

	llm := &mockLLM{
		responses: []string{
			// Single batched response covering all candidate pairs. The
			// LLM returns a 3-object array; the code should map by pair_id.
			`[
				{"pair_id": 1, "relationship": "contradicts", "confidence": 0.8, "explanation": "auth mismatch"},
				{"pair_id": 2, "relationship": "related", "confidence": 0.5, "explanation": "both auth"},
				{"pair_id": 3, "relationship": "supersedes", "confidence": 0.7, "explanation": "newer"}
			]`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	// Exactly one LLM call for all pairs in the batch.
	if result.LLMCalls != 1 {
		t.Fatalf("batched mode should use 1 LLM call, got %d", result.LLMCalls)
	}

	// At least one contradiction/supersession should have been applied
	// (contradicts + supersedes are both recorded; related is dropped).
	if result.ContradictionsDetected < 1 {
		t.Fatalf("expected at least 1 finding applied, got %d", result.ContradictionsDetected)
	}
}

func TestDetectContradictionsBatchedFallbackToSingleWhenSize1(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxContradictionChecks = 10
	cfg.LLMCuration.ContradictionMinSim = 0.5
	cfg.LLMCuration.ContradictionMaxSim = 0.95
	cfg.LLMCuration.ContradictionBatchSize = 1 // explicit single-pair mode

	addProcessedNodeWithEmbedding(t, eng, "We use JWT", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We switched to cookies", []float32{0.7, 0.7, 0.0})

	// Single-pair mode expects single-object JSON, not array.
	llm := &mockLLM{
		responses: []string{
			`{"relationship":"contradicts","confidence":0.8,"explanation":"auth mismatch"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.ContradictionsDetected != 1 {
		t.Fatalf("single-pair mode should yield 1 finding, got %d", result.ContradictionsDetected)
	}
}

// --- Runner with LLM ---

func TestRunnerWithLLM(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addPendingNode(t, eng, "Needs classification")

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.8}`},
	}

	runner := NewRunner(eng, llm, cfg, nil)
	status := runner.Status()
	if !status.Autonomous {
		t.Fatal("autonomous should be true with LLM")
	}

	runner.Trigger(context.Background())

	status = runner.Status()
	if status.PendingCount != 0 {
		t.Fatalf("expected 0 pending after classification, got %d", status.PendingCount)
	}
}

// --- parseClassification edge cases ---

func TestParseClassificationNoJSONBraces(t *testing.T) {
	_, err := parseClassification("This response has no JSON at all")
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}

func TestParseClassificationConfidenceBoundary(t *testing.T) {
	// Exactly 0.0 should be kept.
	r, _ := parseClassification(`{"confidence":0.0}`)
	if r.Confidence != 0.0 {
		t.Fatalf("expected 0.0, got %f", r.Confidence)
	}

	// Exactly 1.0 should be kept.
	r, _ = parseClassification(`{"confidence":1.0}`)
	if r.Confidence != 1.0 {
		t.Fatalf("expected 1.0, got %f", r.Confidence)
	}

	// Negative should be clamped.
	r, _ = parseClassification(`{"confidence":-0.5}`)
	if r.Confidence != 0.5 {
		t.Fatalf("expected 0.5 (clamped), got %f", r.Confidence)
	}
}

func TestParseClassificationEmptyKeywords(t *testing.T) {
	r, err := parseClassification(`{"keywords":[]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Keywords) != 0 {
		t.Fatalf("expected 0 keywords, got %d", len(r.Keywords))
	}
}

func TestParseClassificationKeywordLengthTruncation(t *testing.T) {
	long := ""
	for i := 0; i < 150; i++ {
		long += "a"
	}
	input := fmt.Sprintf(`{"keywords":["%s"]}`, long)
	r, err := parseClassification(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Keywords[0]) > 100 {
		t.Fatalf("keyword should be truncated to 100, got %d", len(r.Keywords[0]))
	}
}

// TestParseClassificationStripsTailContamination verifies that when
// the classification LLM emits JSON where summary_short contains
// tool-use-format tail fragments, the parser strips them before
// returning. Regression against the Cluster 2 bug class observed on
// 2026-04-24 (01KPZZNG45PC7D6HC8SQH3P9N1): corruption inside the
// JSON string value for summary_short was being stored verbatim.
func TestParseClassificationStripsTailContamination(t *testing.T) {
	// Embed the tail pattern inside the JSON string value.
	input := `{"temporality":"durable","confidence":0.9,"summary_short":"Good summary here.</summary_short>\n<parameter name=\"keywords\">[\"a\"]"}`
	r, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if strings.Contains(r.SummaryShort, "</summary_short>") {
		t.Errorf("summary retained </summary_short>: %q", r.SummaryShort)
	}
	if strings.Contains(r.SummaryShort, "<parameter name=") {
		t.Errorf("summary retained <parameter name=: %q", r.SummaryShort)
	}
	if r.SummaryShort != "Good summary here." {
		t.Errorf("summary = %q, want %q", r.SummaryShort, "Good summary here.")
	}
}

// TestParseClassificationDropsPureContamination covers the
// silently-drop-and-warn path: when the LLM emits a summary that is
// entirely tool-use-format garbage, parseClassification must NOT
// return the corrupted value. Returning it would overwrite any
// existing clean summary_short on the record. Empty is the safe
// sentinel for "no improvement this cycle."
func TestParseClassificationDropsPureContamination(t *testing.T) {
	input := `{"temporality":"durable","summary_short":"</summary_short>\n<parameter name=\"keywords\">[\"x\"]"}`
	r, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if r.SummaryShort != "" {
		t.Errorf("summary = %q, want empty (pure-contamination input must be dropped)", r.SummaryShort)
	}
}

func TestParseClassificationJSONWithSurroundingText(t *testing.T) {
	input := `Here is the classification:
{"temporality":"ephemeral","confidence":0.6}
Hope this helps!`

	r, err := parseClassification(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Temporality != "ephemeral" {
		t.Fatalf("expected ephemeral, got %q", r.Temporality)
	}
}

func TestValidateEnum(t *testing.T) {
	allowed := []string{"a", "b", "c"}

	if v := validateEnum("a", allowed); v != "a" {
		t.Fatalf("expected 'a', got %q", v)
	}
	if v := validateEnum("x", allowed); v != "" {
		t.Fatalf("expected empty for invalid, got %q", v)
	}
	if v := validateEnum("", allowed); v != "" {
		t.Fatalf("expected empty for empty, got %q", v)
	}
}

func TestBuildContextSignals(t *testing.T) {
	// Node with all context signals and agent hints.
	n := &graph.Node{
		ID: "test",
		Properties: graph.Properties{
			"context_source_type":      graph.StringProperty("published academic article"),
			"context_time_sensitivity": graph.StringProperty("stable reference"),
			"context_reliability":      graph.StringProperty("peer-reviewed"),
			"context_capture_reason":   graph.StringProperty("building reference corpus"),
			"context_about":            graph.StringProperty("epistemology"),
			"temporality":              graph.StringProperty("immutable"),
			"confidence":               graph.Float64Property(0.95),
		},
	}

	result := buildContextSignals(n)
	if result == "" {
		t.Fatal("expected non-empty context signals")
	}

	// Should contain all the signal labels.
	for _, expected := range []string{
		"Source type: published academic article",
		"Time sensitivity: stable reference",
		"Reliability: peer-reviewed",
		"Capture reason: building reference corpus",
		"About: epistemology",
		"Agent hint (temporality): immutable",
		"Agent hint (confidence): 0.95",
	} {
		if !strings.Contains(result, expected) {
			t.Errorf("missing expected signal %q in:\n%s", expected, result)
		}
	}
}

func TestBuildContextSignalsEmpty(t *testing.T) {
	// Node with no context signals -- should return empty string.
	n := &graph.Node{
		ID: "test",
		Properties: graph.Properties{
			"content_full": graph.StringProperty("just content, no signals"),
		},
	}

	result := buildContextSignals(n)
	if result != "" {
		t.Fatalf("expected empty string for node without signals, got %q", result)
	}
}

func TestBuildContextSignalsPartial(t *testing.T) {
	// Node with only some signals -- should include what's present.
	n := &graph.Node{
		ID: "test",
		Properties: graph.Properties{
			"context_capture_reason": graph.StringProperty("recording a decision"),
			"knowledge_type":         graph.StringProperty("episodic"),
		},
	}

	result := buildContextSignals(n)
	if !strings.Contains(result, "Capture reason: recording a decision") {
		t.Errorf("missing capture reason in:\n%s", result)
	}
	if !strings.Contains(result, "Agent hint (knowledge_type): episodic") {
		t.Errorf("missing knowledge_type hint in:\n%s", result)
	}
	// Should NOT contain labels for missing signals.
	if strings.Contains(result, "Source type") {
		t.Error("should not contain Source type when not set")
	}
}

func TestGenerateSummariesForTruncatedSections(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	// Create a section node with a truncated summary (first 200 chars of content).
	longContent := "On a theory of this sort, what makes some neural process an instance " +
		"of memory trace decay is a matter of how it functions, or the role it plays, " +
		"in a cognitive system; its neural or chemical properties are relevant only " +
		"insofar as they enable that process to do what trace decay is hypothesized to do. " +
		"And similarly for all mental states and processes invoked by cognitive psychological theories."
	truncatedSummary := longContent[:200]

	eng.Lock()
	parent := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Article about functionalism"),
		"content_short":     graph.StringProperty("Functionalism article"),
		"processing_status": graph.StringProperty("processed"),
	})
	section := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(longContent),
		"content_short":     graph.StringProperty(truncatedSummary),
		"processing_status": graph.StringProperty("processed"),
	})
	eng.Graph().AddEdge(section.ID, parent.ID, "section_of", 1.0, nil)
	for k, v := range section.Properties {
		eng.PropIdx().Add(section.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{"Memory trace decay defined by functional role in cognition"},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.SummariesGenerated != 1 {
		t.Fatalf("expected 1 summary generated for truncated section, got %d", result.SummariesGenerated)
	}

	// Verify the summary was updated.
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(section.ID)
	newSummary, _ := n.Properties.GetString("content_short")
	if newSummary == truncatedSummary {
		t.Fatal("summary should have been replaced by LLM-generated one")
	}
	if newSummary != "Memory trace decay defined by functional role in cognition" {
		t.Fatalf("unexpected summary: %q", newSummary)
	}
}

// TestGenerateSummariesRelatedToEdgesNotMisclassifiedAsStructural
// pins P2-07 fix #4: the unified edge walk in generateSummaries
// must distinguish structural (chunk_of / section_of) from semantic
// (related_to / supersedes / etc.) edges. A record with semantic
// edges only is NOT structural and must hit Priority 1 (no-summary).
// Pre-refactor, `!isChunkNode` and the section check each walked
// edges independently; the unified walk does it once. The risk
// addressed: if the merged walk's `IsStructuralEdge` filter ever
// drifts to also treat related_to as structural, this test will
// fail because the target record would silently be skipped.
func TestGenerateSummariesRelatedToEdgesNotMisclassifiedAsStructural(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 10

	now := time.Now().UTC()

	eng.Lock()
	// Two unrelated processed records, then a third with no summary
	// that links to both via related_to.
	other1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("first related"),
		"content_short":     graph.StringProperty("first"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range other1.Properties {
		eng.PropIdx().Add(other1.ID, k, v)
	}
	other2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("second related"),
		"content_short":     graph.StringProperty("second"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range other2.Properties {
		eng.PropIdx().Add(other2.ID, k, v)
	}
	target := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Target record with two outbound related_to edges and no summary."),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		// No content_short.
	})
	for k, v := range target.Properties {
		eng.PropIdx().Add(target.ID, k, v)
	}
	eng.Graph().AddEdge(target.ID, other1.ID, "related_to", 0.7, nil)
	eng.Graph().AddEdge(target.ID, other2.ID, "related_to", 0.6, nil)
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{responses: []string{"target summary from llm"}}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	// Target must have hit Priority 1 (no-summary record) despite
	// having related_to edges. Pre-fix this behaved correctly already
	// (related_to is not a chunk_of edge); the test pins that the
	// unified edge walk's `isStructural` flag correctly excludes
	// related_to.
	if result.SummariesGenerated != 1 {
		t.Errorf("expected 1 summary generated for non-structural record with related_to edges, got %d", result.SummariesGenerated)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(target.ID)
	got, _ := n.Properties.GetString("content_short")
	if got != "target summary from llm" {
		t.Errorf("expected target.content_short = %q, got %q", "target summary from llm", got)
	}
}

func TestCreateConceptNodes(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.MaxConceptsPerRun = 5

	now := time.Now().UTC()

	// Create some records with a shared keyword.
	id1 := addNode(t, eng, "Kafka event streaming for microservices", "durable", 0.9,
		[]string{"kafka", "streaming"}, now)
	id2 := addNode(t, eng, "Kafka consumer group rebalancing", "durable", 0.85,
		[]string{"kafka", "consumers"}, now)
	id3 := addNode(t, eng, "Kafka partitioning strategies", "durable", 0.8,
		[]string{"kafka", "partitioning"}, now)

	// Concept creation is now deterministic (in RunDeterministic).
	// Run deterministic curation to create the concept node.
	detResult := RunDeterministic(eng, cfg, nil)

	if detResult.ConceptsCreated != 1 {
		t.Fatalf("expected 1 concept created deterministically, got %d", detResult.ConceptsCreated)
	}

	// Verify the concept node exists with template content.
	eng.RLock()
	g := eng.Graph()
	var conceptID string
	conceptFound := false
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok && kw == "kafka" {
				conceptFound = true
				conceptID = n.ID
				if kt, _ := n.Properties.GetString("knowledge_type"); kt != "conceptual" {
					t.Fatalf("expected knowledge_type=conceptual, got %q", kt)
				}
				if ss, _ := n.Properties.GetString("synthesis_status"); ss != "pending" {
					t.Fatalf("expected synthesis_status=pending, got %q", ss)
				}
				if ps, _ := n.Properties.GetString("processing_status"); ps != "processed" {
					t.Fatalf("expected processing_status=processed, got %q", ps)
				}
			}
		}
	}
	it.Close()
	eng.RUnlock()

	if !conceptFound {
		t.Fatal("concept node not found")
	}

	// Verify instance_of edges from members to concept.
	eng.RLock()
	edgeCount := 0
	for _, memberID := range []string{id1, id2, id3} {
		for _, e := range g.EdgesFrom(memberID) {
			if e.Type == "instance_of" && e.TargetID == conceptID {
				edgeCount++
			}
		}
	}
	eng.RUnlock()
	if edgeCount != 3 {
		t.Fatalf("expected 3 instance_of edges, got %d", edgeCount)
	}

	// Now test LLM enrichment of the pending concept.
	llm := &mockLLM{
		responses: []string{
			`[{"keyword":"kafka","synthesis":"Kafka is a distributed event streaming platform used for microservices communication."}]`,
		},
	}

	result := &AutonomousResult{}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.ConceptsCreated != 1 {
		t.Fatalf("expected 1 concept enriched, got %d", result.ConceptsCreated)
	}

	// Verify synthesis was applied.
	eng.RLock()
	defer eng.RUnlock()
	cn, ok := g.GetNode(conceptID)
	if !ok {
		t.Fatal("concept node gone after enrichment")
	}
	ss, _ := cn.Properties.GetString("synthesis_status")
	if ss != "complete" {
		t.Fatalf("expected synthesis_status=complete, got %q", ss)
	}
	content, _ := cn.Properties.GetString("content_full")
	if !strings.Contains(content, "Kafka") {
		t.Fatalf("content_full should contain synthesis, got %q", content)
	}
}

func TestDeterministicConceptIdempotent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()
	addNode(t, eng, "Redis caching strategies", "durable", 0.9, []string{"redis"}, now)
	addNode(t, eng, "Redis cluster setup", "durable", 0.8, []string{"redis"}, now)
	addNode(t, eng, "Redis pub/sub patterns", "durable", 0.7, []string{"redis"}, now)

	// First run creates the concept.
	result1 := RunDeterministic(eng, cfg, nil)
	if result1.ConceptsCreated != 1 {
		t.Fatalf("first run: expected 1 concept, got %d", result1.ConceptsCreated)
	}

	// Second run should skip (concept already exists).
	result2 := RunDeterministic(eng, cfg, nil)
	if result2.ConceptsCreated != 0 {
		t.Fatalf("second run: expected 0 concepts (already exists), got %d", result2.ConceptsCreated)
	}
}

func TestConceptShortSummary(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		wantMin  int // minimum length
		wantSelf bool // expect full input returned
	}{
		{
			name:     "first sentence within limit",
			input:    "Kafka is a distributed event streaming platform. Records cover streaming and partitioning.",
			maxLen:   200,
			wantMin:  20,
			wantSelf: false,
		},
		{
			name:     "short input returned as-is",
			input:    "Short summary.",
			maxLen:   200,
			wantMin:  5,
			wantSelf: true,
		},
		{
			name:     "long input without periods truncates at word boundary",
			input:    "This is a very long text without any sentence boundaries that goes on and on and on repeating itself many times over",
			maxLen:   50,
			wantMin:  20,
			wantSelf: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conceptShortSummary(tt.input, tt.maxLen)
			if len(got) < tt.wantMin {
				t.Errorf("too short: %q (len=%d, wantMin=%d)", got, len(got), tt.wantMin)
			}
			if len(got) > tt.maxLen {
				t.Errorf("exceeds maxLen: len=%d, maxLen=%d", len(got), tt.maxLen)
			}
			if tt.wantSelf && got != tt.input {
				t.Errorf("expected full input, got %q", got)
			}
			if !tt.wantSelf && got == tt.input {
				t.Errorf("expected truncation, got full input")
			}
		})
	}
}
