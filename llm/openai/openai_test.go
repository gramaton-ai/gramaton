package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

func TestNewMissingModel(t *testing.T) {
	cfg := config.LLMConfig{Provider: "openai"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New() with empty model should fail")
	}
}

func TestModelID(t *testing.T) {
	c := &Client{model: "gpt-4o"}
	if got := c.ModelID(); got != "gpt-4o" {
		t.Errorf("ModelID() = %q", got)
	}
}

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}

		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Model != "gpt-4o" {
			t.Errorf("model = %q, want gpt-4o", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}

		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "Hello!"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("TEST_OPENAI_KEY", "test-key")
	cfg := config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-4o",
		BaseURL:   srv.URL,
		APIKeyEnv: "TEST_OPENAI_KEY",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	got, err := c.Complete(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	if got != "Hello!" {
		t.Errorf("Complete() = %q, want %q", got, "Hello!")
	}
}

// TestSupportsStructuredOutput confirms OpenAI advertises structured
// output via response_format: json_schema + strict=true.
func TestSupportsStructuredOutput(t *testing.T) {
	c := &Client{model: "gpt-4o"}
	if !c.SupportsStructuredOutput() {
		t.Error("OpenAI.SupportsStructuredOutput() = false; want true")
	}
}

// TestCompleteStructuredSuccess verifies the response_format payload
// carries type=json_schema + strict=true + the supplied schema,
// and the response content is returned as raw JSON.
func TestCompleteStructuredSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.ResponseFormat == nil {
			t.Fatal("request missing response_format")
		}
		if req.ResponseFormat.Type != "json_schema" {
			t.Errorf("response_format.type = %q, want json_schema", req.ResponseFormat.Type)
		}
		if req.ResponseFormat.JSONSchema == nil || !req.ResponseFormat.JSONSchema.Strict {
			t.Error("response_format.json_schema.strict must be true for wire-layer enforcement")
		}
		if req.ResponseFormat.JSONSchema.Schema["type"] != "object" {
			t.Errorf("schema not forwarded verbatim: %+v", req.ResponseFormat.JSONSchema.Schema)
		}

		// Respond with the schema-valid JSON embedded in content.
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: `{"field":"value","n":42}`}},
			},
			Usage: chatUsage{PromptTokens: 80, CompletionTokens: 12},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "gpt-4o", apiKey: "sk-test", client: srv.Client()}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field": map[string]any{"type": "string"},
			"n":     map[string]any{"type": "integer"},
		},
	}
	raw, err := c.CompleteStructured(context.Background(), schema, "give me structured")
	if err != nil {
		t.Fatalf("CompleteStructured: %v", err)
	}
	var parsed struct {
		Field string `json:"field"`
		N     int    `json:"n"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal raw=%q: %v", raw, err)
	}
	if parsed.Field != "value" || parsed.N != 42 {
		t.Errorf("parsed = %+v, want {value, 42}", parsed)
	}
}

// TestCompleteStructuredAPIError surfaces a 4xx error from OpenAI
// rather than silently returning empty. Mirrors TestCompleteAPIError
// for the structured path.
func TestCompleteStructuredAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"schema rejected"}}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "gpt-4o", apiKey: "sk-test", client: srv.Client()}
	_, err := c.CompleteStructured(context.Background(), map[string]any{"type": "object"}, "prompt")
	if err == nil {
		t.Fatal("expected error on 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("error missing type: %v", err)
	}
}

// TestCompleteStructuredNoChoices mirrors TestCompleteNoChoices for
// the structured path: an empty choices array must surface as an
// error, not a silent nil RawMessage.
func TestCompleteStructuredNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "gpt-4o", client: srv.Client()}
	_, err := c.CompleteStructured(context.Background(), map[string]any{"type": "object"}, "hello")
	if err == nil {
		t.Fatal("expected error on empty choices")
	}
}

func TestCompleteNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "test", client: srv.Client()}
	_, err := c.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on empty choices")
	}
}

func TestCompleteAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "rate limit exceeded",
				"type":    "rate_limit_error",
			},
		})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "test", client: srv.Client()}
	_, err := c.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 429")
	}
}

func TestCompleteNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("should not send Authorization header when no key")
		}
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "ok"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := config.LLMConfig{
		Provider: "openai",
		Model:    "local-model",
		BaseURL:  srv.URL,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	got, err := c.Complete(context.Background(), "test")
	if err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	if got != "ok" {
		t.Errorf("Complete() = %q, want %q", got, "ok")
	}
}

// TestCompleteRecordsUsage proves Complete reports token counts to a
// UsageRecorder attached to ctx. Mirrors the anthropic/bedrock telemetry
// contract so per-provider accounting in Metered works uniformly.
func TestCompleteRecordsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "ok"}}],
			"usage": {
				"prompt_tokens": 42,
				"completion_tokens": 7,
				"prompt_tokens_details": {"cached_tokens": 10}
			}
		}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "gpt-4o", client: srv.Client()}
	recorder := &telemetry.UsageRecorder{}
	ctx := telemetry.WithUsageRecorder(context.Background(), recorder)
	ctx = telemetry.WithTask(ctx, "classify")

	if _, err := c.Complete(ctx, "hello"); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	got := recorder.Total()
	want := telemetry.CallUsage{InputTokens: 42, OutputTokens: 7, CacheReadTokens: 10}
	if got != want {
		t.Errorf("recorded usage = %+v, want %+v", got, want)
	}
	if byTask := recorder.ByTask(); byTask["classify"] != want {
		t.Errorf("per-task usage = %+v, want %+v", byTask["classify"], want)
	}
}

func TestResolveKey(t *testing.T) {
	tests := []struct {
		name string
		val  string
		env  map[string]string
		want string
	}{
		{"empty", "", nil, ""},
		{"env_var", "MY_KEY", map[string]string{"MY_KEY": "resolved"}, "resolved"},
		{"direct_key", "sk-abc123", nil, "sk-abc123"},
		{"unset_env", "MISSING_VAR", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			// Pass tt.val as both env-name AND direct, mirroring
			// pre-Wave-2 ergonomics where APIKeyEnv could double
			// as a literal key. The deprecation warning fires
			// once for the sk- direct path; harmless in tests.
			if got := secret.ResolveKey("", tt.val, ""); got != tt.want {
				t.Errorf("ResolveKey(%q) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}
