package curation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// mockLLM is a controllable LLM provider for testing.
type mockLLM struct {
	responses []string // returned in order, cycles if exhausted
	errors    []error  // if non-nil at index, return error instead
	calls     int
	models    []string // model requested per call (for tiering tests)
}

func (m *mockLLM) Complete(_ context.Context, _ string) (string, error) {
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

func (m *mockLLM) ModelID() string { return "mock-llm" }

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

	if result.Classified != 0 {
		t.Fatalf("expected 0 classified, got %d", result.Classified)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
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
	classifyPending(context.Background(), eng, llm, cfg, result, 3, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(ctx, eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (empty content), got %d", result.LLMCalls)
	}
}

func TestClassifyPendingModelTiering(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.LightModel = "claude-haiku-4-5"
	cfg.LLMCuration.LightModelThreshold = 100

	// Short content (below threshold) -> light model.
	shortID := addPendingNode(t, eng, "short fact")
	// Long content (above threshold) -> default model.
	longContent := strings.Repeat("a", 200)
	longID := addPendingNode(t, eng, longContent)

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp, classifyResp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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

	if shortClassifiedBy != "claude-haiku-4-5" {
		t.Errorf("short record should be classified by haiku, got %q", shortClassifiedBy)
	}
	if longClassifiedBy != "mock-llm" {
		t.Errorf("long record should be classified by default model, got %q", longClassifiedBy)
	}
}

func TestClassifyPendingNoTieringWhenLightModelEmpty(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.LightModel = "" // no tiering

	addPendingNode(t, eng, "short fact")

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp}}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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

func TestGenerateSummariesLLMError(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	addProcessedNodeNoSummary(t, eng, "Content")

	llm := &mockLLM{
		errors: []error{fmt.Errorf("rate limited")},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 2, nil, false)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil, true)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, true)

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

	// Two records with embeddings in the contradiction similarity range (~0.7 cosine).
	addProcessedNodeWithEmbedding(t, eng, "We use JWT tokens for auth", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We switched to session cookies for auth", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"contradicts","confidence":0.8,"explanation":"JWT vs session cookies are incompatible auth approaches"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, nil, false)

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

	addProcessedNodeWithEmbedding(t, eng, "We use PostgreSQL", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "We migrated to MySQL", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"supersedes","confidence":0.85,"explanation":"MySQL migration replaces PostgreSQL"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, nil, true)

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
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, nil, false)

	// No LLM calls should be made since records are too dissimilar.
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls for dissimilar records, got %d", result.LLMCalls)
	}
	if result.ContradictionsDetected != 0 {
		t.Fatalf("expected 0 contradictions, got %d", result.ContradictionsDetected)
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
	generateManifestSummary(context.Background(), eng, llm, result, nil)

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
	generateManifestSummary(context.Background(), eng, llm, result, nil)

	if result.ManifestSummary != "" {
		t.Fatal("should not generate summary with too few records")
	}
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls, got %d", result.LLMCalls)
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

	idA := addProcessedNodeWithEmbedding(t, eng, "Original API v1 design", []float32{1.0, 0.0, 0.0})
	addProcessedNodeWithEmbedding(t, eng, "Updated API v2 design", []float32{0.7, 0.7, 0.0})

	llm := &mockLLM{
		responses: []string{
			`{"relationship":"supersedes","confidence":0.85,"explanation":"v2 replaces v1"}`,
		},
	}

	result := &AutonomousResult{}
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, nil, false)

	if result.ContradictionsDetected != 1 {
		t.Fatalf("expected 1 supersession, got %d", result.ContradictionsDetected)
	}

	// Verify the older record got valid_until set.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(idA)
	if !ok {
		t.Fatal("node A should exist")
	}
	if _, ok := n.Properties.GetTimestamp("valid_until"); !ok {
		t.Fatal("superseded record should have valid_until set")
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
	detectContradictions(context.Background(), eng, llm, cfg, result, 20, nil, false)

	if result.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", result.Errors)
	}
	if result.ContradictionsDetected != 0 {
		t.Fatalf("expected 0 contradictions on error, got %d", result.ContradictionsDetected)
	}
}

func TestGenerateManifestSummaryLLMError(t *testing.T) {
	eng := setupEngine(t)

	for i := 0; i < 6; i++ {
		addProcessedNodeWithEmbedding(t, eng, fmt.Sprintf("Record %d", i), []float32{float32(i) * 0.1, 0.5, 0.3})
	}

	llm := &mockLLM{errors: []error{fmt.Errorf("LLM error")}}

	result := &AutonomousResult{}
	generateManifestSummary(context.Background(), eng, llm, result, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil, false)

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

	candidates := []ConceptCandidate{
		{
			Keyword: "kafka",
			Count:   3,
			NodeIDs: []string{id1, id2, id3},
		},
	}

	llm := &mockLLM{
		responses: []string{
			"Kafka is a distributed event streaming platform used for microservices communication. " +
				"Records cover event streaming, consumer group management, and partitioning strategies.",
		},
	}

	result := &AutonomousResult{}
	createConceptNodes(context.Background(), eng, llm, cfg, result, 20, nil, false, candidates)

	if result.ConceptsCreated != 1 {
		t.Fatalf("expected 1 concept created, got %d", result.ConceptsCreated)
	}
	if result.LLMCalls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", result.LLMCalls)
	}

	// Verify the concept node exists.
	eng.RLock()
	defer eng.RUnlock()

	g := eng.Graph()
	conceptFound := false
	var conceptID string
	for _, id := range g.AllNodeIDs() {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok && kw == "kafka" {
				conceptFound = true
				conceptID = id
				// Check properties.
				if kt, ok := n.Properties.GetString("knowledge_type"); !ok || kt != "conceptual" {
					t.Fatalf("expected knowledge_type=conceptual, got %q", kt)
				}
				if content, ok := n.Properties.GetString("content_full"); !ok || content == "" {
					t.Fatal("concept node should have content_full")
				}
				// content_short should be a proper summary, not the keyword.
				if cs, ok := n.Properties.GetString("content_short"); !ok || cs == "" {
					t.Fatal("concept node should have content_short")
				} else if cs == "kafka" {
					t.Fatalf("content_short should be a summary, not the keyword; got %q", cs)
				} else if len(cs) < 10 {
					t.Fatalf("content_short too short to be a summary: %q", cs)
				}
			}
		}
	}
	if !conceptFound {
		t.Fatal("concept node not found")
	}

	// Verify instance_of edges from members to concept.
	edgeCount := 0
	for _, memberID := range []string{id1, id2, id3} {
		for _, e := range g.EdgesFrom(memberID) {
			if e.Type == "instance_of" && e.TargetID == conceptID {
				edgeCount++
			}
		}
	}
	if edgeCount != 3 {
		t.Fatalf("expected 3 instance_of edges, got %d", edgeCount)
	}
}

func TestCreateConceptNodesIdempotent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	id1 := addNode(t, eng, "Redis caching strategies", "durable", 0.9,
		[]string{"redis"}, now)

	candidates := []ConceptCandidate{
		{Keyword: "redis", Count: 1, NodeIDs: []string{id1}},
	}

	llm := &mockLLM{
		responses: []string{"Redis is an in-memory data store used for caching."},
	}

	// First run creates the concept.
	result1 := &AutonomousResult{}
	createConceptNodes(context.Background(), eng, llm, cfg, result1, 20, nil, false, candidates)
	if result1.ConceptsCreated != 1 {
		t.Fatalf("first run: expected 1 concept, got %d", result1.ConceptsCreated)
	}

	// Second run should skip (concept already exists).
	result2 := &AutonomousResult{}
	createConceptNodes(context.Background(), eng, llm, cfg, result2, 20, nil, false, candidates)
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
