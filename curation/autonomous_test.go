package curation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
)

// mockLLM is a controllable LLM provider for testing.
type mockLLM struct {
	responses []string // returned in order, cycles if exhausted
	errors    []error  // if non-nil at index, return error instead
	calls     int
}

func (m *mockLLM) Complete(_ context.Context, _ string) (string, error) {
	idx := m.calls
	m.calls++
	if idx < len(m.errors) && m.errors[idx] != nil {
		return "", m.errors[idx]
	}
	if len(m.responses) == 0 {
		return "", fmt.Errorf("no responses configured")
	}
	return m.responses[idx%len(m.responses)], nil
}

func (m *mockLLM) ModelID() string { return "mock-llm" }

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 3, nil)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil)

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
	classifyPending(ctx, eng, llm, cfg, result, 20, nil)

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
	classifyPending(context.Background(), eng, llm, cfg, result, 20, nil)

	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls (empty content), got %d", result.LLMCalls)
	}
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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 20, nil)

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
	generateSummaries(context.Background(), eng, llm, cfg, result, 2, nil)

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

	result := RunAutonomous(context.Background(), eng, llm, cfg, nil)

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
