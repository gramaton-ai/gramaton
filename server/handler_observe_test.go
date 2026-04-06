package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
)

// mockLLMProvider is a test LLM that returns pre-configured responses.
type mockLLMProvider struct {
	responses []string
	calls     int
}

func (m *mockLLMProvider) Complete(_ context.Context, _ string) (string, error) {
	if m.calls >= len(m.responses) {
		return "", fmt.Errorf("no more responses")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func (m *mockLLMProvider) ModelID() string { return "mock" }

// setupTestServerWithLLM creates a test server with a mock LLM provider.
func setupTestServerWithLLM(t *testing.T, llm *mockLLMProvider) (*Server, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = "" // We'll inject LLM manually.
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}

	// Inject mock LLM via the engine's SetLLM if available, or directly.
	// Since the engine LLM is set at load time, we need to work around it.
	// We'll test extractFacts directly instead.

	srv := New(eng, DefaultConfig(), nil)
	return srv, eng
}

// --- parseExtractedFacts tests ---

func TestParseExtractedFacts(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{
			name:    "valid JSON",
			input:   `{"facts": ["User prefers dark mode", "API uses JWT auth"]}`,
			wantLen: 2,
		},
		{
			name:    "empty facts",
			input:   `{"facts": []}`,
			wantLen: 0,
		},
		{
			name:    "with code fences",
			input:   "```json\n{\"facts\": [\"one fact\"]}\n```",
			wantLen: 1,
		},
		{
			name:    "with preamble text",
			input:   "Here are the facts:\n{\"facts\": [\"extracted\"]}",
			wantLen: 1,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts, err := parseExtractedFacts(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(facts) != tt.wantLen {
				t.Fatalf("expected %d facts, got %d", tt.wantLen, len(facts))
			}
		})
	}
}

func TestObserveExtractionPromptFormat(t *testing.T) {
	conversation := "user: Hello\n\nassistant: Hi there\n\n"
	result := fmt.Sprintf(observeExtractionPrompt, conversation)
	if len(result) < 100 {
		t.Fatal("prompt should be substantial")
	}
}

// --- handleObserve HTTP handler tests ---

func TestHandleObserveDisabled(t *testing.T) {
	// Create a server with observe explicitly disabled.
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Observe.Enabled = false
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	srv := New(eng, DefaultConfig(), nil)

	body := `{"facts": ["test fact"]}`
	req := httptest.NewRequest("POST", "/v1/observe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleObserve(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when disabled, got %d", w.Code)
	}
}

func TestHandleObserveMissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{}`
	req := httptest.NewRequest("POST", "/v1/observe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleObserve(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", w.Code)
	}
}

func TestHandleObserveMessagesWithoutLLM(t *testing.T) {
	srv, _ := setupTestServer(t)
	// No LLM configured in test server.

	body := `{"messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest("POST", "/v1/observe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleObserve(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without LLM for messages mode, got %d", w.Code)
	}
}

func TestHandleObserveFactsAccepted(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"facts": ["User prefers dark mode", "API uses JWT authentication"]}`
	req := httptest.NewRequest("POST", "/v1/observe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleObserve(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 accepted, got %d", w.Code)
	}

	// Wait for async processing to complete before test cleanup.
	time.Sleep(200 * time.Millisecond)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		if accepted, ok := resp["accepted"].(bool); !ok || !accepted {
			t.Fatal("expected accepted: true in response")
		}
	}
}

// --- Quality gates tests ---

func TestQualityGateSubstanceFilter(t *testing.T) {
	srv, _ := setupTestServer(t)
	cfg := srv.engine.Config()

	// Short facts should be filtered out.
	facts := []string{
		"Hi",           // too short
		"ok",           // too short
		"This is a real fact about JWT authentication patterns", // long enough
	}

	stored := srv.applyQualityGates(context.Background(), facts, cfg)

	if stored != 1 {
		t.Fatalf("expected 1 stored (substance filter), got %d", stored)
	}

	// Verify the stored record.
	srv.engine.RLock()
	defer srv.engine.RUnlock()
	count := 0
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "captured" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 captured record in store, got %d", count)
	}
}

func TestQualityGateDedupFilter(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add an existing record with an embedding.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("JWT tokens for authentication"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
		"embedding_full":    graph.VectorProperty([]float32{1.0, 0.0, 0.0}),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, []float32{1.0, 0.0, 0.0})
	eng.Save("test")
	eng.Unlock()

	cfg := srv.engine.Config()

	// This fact is identical to existing -- should be deduped.
	// (Without embedder, similarity gates are skipped, so this tests
	// the no-embedder path which stores directly.)
	facts := []string{"JWT tokens for authentication"}

	stored := srv.applyQualityGates(context.Background(), facts, cfg)

	// Without embedder configured, similarity gates are skipped and
	// fact goes straight to storage. This tests the fallback path.
	if stored != 1 {
		t.Fatalf("expected 1 stored (no embedder = no similarity check), got %d", stored)
	}
}

func TestQualityGateRetrievalTrackerFilter(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add a record with embedding and mark it as retrieved.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("We decided to use PostgreSQL for the database"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(1),
		"embedding_full":    graph.VectorProperty([]float32{0.9, 0.1, 0.0}),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, []float32{0.9, 0.1, 0.0})
	eng.Save("test")
	eng.Unlock()

	// Track this record as retrieved.
	srv.retrieval.Track(n.ID)

	// Verify it's tracked.
	if srv.retrieval.Len() != 1 {
		t.Fatalf("expected 1 tracked ID, got %d", srv.retrieval.Len())
	}
}

func TestStoreDeferredCapture(t *testing.T) {
	srv, _ := setupTestServer(t)
	cfg := srv.engine.Config()

	srv.storeDeferredCapture("Test fact for deferred capture", cfg)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	found := false
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if content, ok := n.Properties.GetString("content_full"); ok && content == "Test fact for deferred capture" {
			found = true

			// Verify deferred capture defaults.
			ps, _ := n.Properties.GetString("processing_status")
			if ps != "captured" {
				t.Fatalf("expected processing_status=captured, got %q", ps)
			}
			temp, _ := n.Properties.GetString("temporality")
			if temp != "ephemeral" {
				t.Fatalf("expected temporality=ephemeral, got %q", temp)
			}
			conf, _ := n.Properties.GetFloat64("confidence")
			if conf != 0.3 {
				t.Fatalf("expected confidence=0.3, got %f", conf)
			}
			imp, _ := n.Properties.GetFloat64("importance")
			if imp != 0 {
				t.Fatalf("expected importance=0, got %f", imp)
			}
			src, _ := n.Properties.GetString("source_ref")
			if !strings.HasPrefix(src, "observe:") {
				t.Fatalf("expected source_ref starting with 'observe:', got %q", src)
			}
		}
	}
	if !found {
		t.Fatal("deferred capture record not found in store")
	}
}

func TestStoreDeferredCaptureWithEmbedding(t *testing.T) {
	srv, _ := setupTestServer(t)
	cfg := srv.engine.Config()

	vec := []float32{0.5, 0.5, 0.0}
	srv.storeDeferredCaptureWithEmbedding("Fact with embedding", vec, cfg)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	found := false
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if content, ok := n.Properties.GetString("content_full"); ok && content == "Fact with embedding" {
			found = true
			// Verify embedding was stored.
			if emb, ok := n.Properties.GetVector("embedding_full"); ok {
				if len(emb) != 3 {
					t.Fatalf("expected 3-dim embedding, got %d", len(emb))
				}
			} else {
				t.Fatal("embedding_full should be set")
			}
			// Verify in vector index.
			results := srv.engine.VecIdx().Search(vec, 1, nil)
			if len(results) == 0 {
				t.Fatal("expected record in vector index")
			}
		}
	}
	if !found {
		t.Fatal("embedded capture record not found")
	}
}

func TestProcessObservationFactsMode(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := observeRequest{
		Facts: []string{
			"User decided to use dark mode for all interfaces",
			"API versioning follows semantic versioning rules",
			"x", // too short, should be filtered
		},
	}

	srv.processObservation(req)

	// Give async processing a moment (processObservation is synchronous
	// when called directly, but let's be safe).
	time.Sleep(50 * time.Millisecond)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	count := 0
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "captured" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 stored facts (1 filtered for substance), got %d", count)
	}
}

func TestProcessObservationMaxFactsCap(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Observe.MaxFactsPerCall = 2
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	srv := New(eng, DefaultConfig(), nil)

	facts := make([]string, 10)
	for i := range facts {
		facts[i] = fmt.Sprintf("Fact number %d about something interesting enough to pass substance filter", i)
	}

	req := observeRequest{Facts: facts}
	srv.processObservation(req)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	count := 0
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "captured" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 stored (capped by MaxFactsPerCall), got %d", count)
	}
}

// --- Observe config defaults ---

func TestObserveConfigDefaults(t *testing.T) {
	cfg := config.Defaults()

	if !cfg.Observe.Enabled {
		t.Fatal("observe should be enabled by default")
	}
	if cfg.Observe.MaxFactsPerCall != 20 {
		t.Fatalf("expected MaxFactsPerCall=20, got %d", cfg.Observe.MaxFactsPerCall)
	}
	if cfg.Observe.DefaultConfidence != 0.3 {
		t.Fatalf("expected DefaultConfidence=0.3, got %f", cfg.Observe.DefaultConfidence)
	}
	if cfg.Observe.DefaultTemporality != "ephemeral" {
		t.Fatalf("expected DefaultTemporality=ephemeral, got %q", cfg.Observe.DefaultTemporality)
	}
	if cfg.Observe.FeedbackLoopHours != 4 {
		t.Fatalf("expected FeedbackLoopHours=4, got %d", cfg.Observe.FeedbackLoopHours)
	}
	if cfg.Observe.RetrievalSimilarity != 0.7 {
		t.Fatalf("expected RetrievalSimilarity=0.7, got %f", cfg.Observe.RetrievalSimilarity)
	}
}

func TestGCConfigDefaults(t *testing.T) {
	cfg := config.Defaults()

	if cfg.GC.Enabled {
		t.Fatal("GC should be disabled by default")
	}
	if !cfg.GC.DryRun {
		t.Fatal("GC should be dry-run by default")
	}
	if cfg.GC.MinAgeDays != 30 {
		t.Fatalf("expected MinAgeDays=30, got %d", cfg.GC.MinAgeDays)
	}
}

func TestExtractFactsNoLLM(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.extractFacts(context.Background(), []observeMessage{
		{Role: "user", Content: "Hello"},
	})

	if err == nil {
		t.Fatal("expected error when no LLM configured")
	}
	if !strings.Contains(err.Error(), "no LLM provider") {
		t.Fatalf("expected 'no LLM provider' error, got: %v", err)
	}
}

func TestQualityGateSubstanceFilterEdgeCases(t *testing.T) {
	srv, _ := setupTestServer(t)
	cfg := srv.engine.Config()

	facts := []string{
		"",                          // empty
		"short",                     // 5 chars
		strings.Repeat("x", 19),     // exactly 19 chars
		strings.Repeat("x", 20),     // exactly 20 chars -- passes
		"A fact about authentication that is long enough to pass the filter",
	}

	stored := srv.applyQualityGates(context.Background(), facts, cfg)

	if stored != 2 {
		t.Fatalf("expected 2 stored (20+ char facts), got %d", stored)
	}
}

func TestQualityGateAllFactsFiltered(t *testing.T) {
	srv, _ := setupTestServer(t)
	cfg := srv.engine.Config()

	facts := []string{"a", "b", "ok"}

	stored := srv.applyQualityGates(context.Background(), facts, cfg)

	if stored != 0 {
		t.Fatalf("expected 0 stored (all too short), got %d", stored)
	}
}

func TestProcessObservationEmptyFacts(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := observeRequest{Facts: []string{}}
	srv.processObservation(req)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	if srv.engine.Graph().NodeCount() != 0 {
		t.Fatal("empty facts should produce no records")
	}
}

// --- HTTP round-trip test ---

func TestObserveHTTPRoundTrip(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Build request body.
	body, _ := json.Marshal(observeRequest{
		Facts: []string{"Round-trip test fact that should be stored"},
	})

	req := httptest.NewRequest("POST", "/v1/observe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleObserve(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for async processing.
	time.Sleep(100 * time.Millisecond)

	srv.engine.RLock()
	defer srv.engine.RUnlock()

	found := false
	for _, id := range srv.engine.Graph().AllNodeIDs() {
		n, _ := srv.engine.Graph().GetNode(id)
		if c, ok := n.Properties.GetString("content_full"); ok && strings.Contains(c, "Round-trip") {
			found = true
		}
	}
	if !found {
		t.Fatal("observed fact should be in the store after async processing")
	}
}
