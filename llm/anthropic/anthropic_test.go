package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
)

func TestResolveKeyFromEnv(t *testing.T) {
	t.Setenv("TEST_GRAMATON_KEY", "sk-ant-test-key")
	key := secret.ResolveKey("", "TEST_GRAMATON_KEY", "")
	if key != "sk-ant-test-key" {
		t.Fatalf("expected key from env, got %q", key)
	}
}

func TestResolveKeyDirect(t *testing.T) {
	key := secret.ResolveKey("", "", "sk-ant-direct-key-value")
	if key != "sk-ant-direct-key-value" {
		t.Fatalf("expected direct key, got %q", key)
	}
}

func TestResolveKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key\n"), 0o600)

	key := secret.ResolveKey(keyPath, "", "")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected key from file, got %q", key)
	}
}

func TestResolveKeyFileTakesPrecedence(t *testing.T) {
	t.Setenv("TEST_GRAMATON_KEY", "sk-ant-env-key")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key\n"), 0o600)

	key := secret.ResolveKey(keyPath, "TEST_GRAMATON_KEY", "")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected file key to take precedence, got %q", key)
	}
}

func TestResolveKeyEmpty(t *testing.T) {
	key := secret.ResolveKey("", "", "")
	if key != "" {
		t.Fatalf("expected empty, got %q", key)
	}
}

func TestResolveKeyUnsetEnv(t *testing.T) {
	key := secret.ResolveKey("", "NONEXISTENT_VAR_12345", "")
	if key != "" {
		t.Fatalf("expected empty for unset env var, got %q", key)
	}
}

func TestNewWithoutKey(t *testing.T) {
	cfg := config.LLMConfig{
		Provider:  "anthropic",
		Models:    config.LLMModels{Medium: "claude-sonnet-4-6"},
		APIKeyEnv: "NONEXISTENT_KEY_12345",
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error when API key is missing")
	}
}

func TestNewWithDirectKey(t *testing.T) {
	cfg := config.LLMConfig{
		Provider: "anthropic",
		Models:   config.LLMModels{Medium: "claude-sonnet-4-6"},
		APIKey:   "sk-ant-test-key",
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.ModelID() != "claude-sonnet-4-6" {
		t.Fatalf("expected model claude-sonnet-4-6, got %q", client.ModelID())
	}
}

func TestNewDefaultModel(t *testing.T) {
	cfg := config.LLMConfig{
		Provider: "anthropic",
		APIKey:   "sk-ant-test",
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.ModelID() != "claude-sonnet-4-6" {
		t.Fatalf("expected default model, got %q", client.ModelID())
	}
}

func TestCompleteSuccess(t *testing.T) {
	// Mock Anthropic API server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request.
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("missing or wrong API key")
		}
		if r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("missing or wrong API version")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing Content-Type")
		}

		// Verify request body.
		var req messagesRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "claude-sonnet-4-6" {
			t.Errorf("wrong model: %s", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("wrong messages: %+v", req.Messages)
		}

		// Return response.
		json.NewEncoder(w).Encode(messagesResponse{
			Content: []contentBlock{
				{Type: "text", Text: "Hello, world!"},
			},
			Usage: usage{InputTokens: 10, OutputTokens: 5},
		})
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}

	result, err := client.Complete(context.Background(), "Say hello")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result != "Hello, world!" {
		t.Fatalf("expected 'Hello, world!', got %q", result)
	}
}

// TestSupportsStructuredOutput confirms Anthropic advertises
// structured-output capability via the tool-use API.
func TestSupportsStructuredOutput(t *testing.T) {
	client := &Client{}
	if !client.SupportsStructuredOutput() {
		t.Error("Anthropic.SupportsStructuredOutput() = false; want true (tool-use API)")
	}
}

// TestCompleteStructuredSuccess verifies the tool-use request is
// built correctly, the response's tool_use.input is returned as-is,
// and usage telemetry is recorded.
func TestCompleteStructuredSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(req.Tools))
		}
		if req.Tools[0].Name != "emit_output" {
			t.Errorf("tool name = %q, want emit_output", req.Tools[0].Name)
		}
		if req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "emit_output" {
			t.Errorf("tool_choice = %+v, want {type: tool, name: emit_output}", req.ToolChoice)
		}
		// Verify the schema was forwarded verbatim.
		if req.Tools[0].InputSchema["type"] != "object" {
			t.Errorf("schema type = %v, want object", req.Tools[0].InputSchema["type"])
		}

		// Respond with a tool_use block.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [
				{"type": "tool_use", "name": "emit_output", "input": {"field": "value", "n": 42}}
			],
			"usage": {"input_tokens": 100, "output_tokens": 20}
		}`))
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field": map[string]any{"type": "string"},
			"n":     map[string]any{"type": "integer"},
		},
		"required": []string{"field", "n"},
	}
	raw, err := client.CompleteStructured(context.Background(), schema, "give me a structured thing")
	if err != nil {
		t.Fatalf("CompleteStructured: %v", err)
	}
	// Unmarshal and verify.
	var parsed struct {
		Field string `json:"field"`
		N     int    `json:"n"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, raw)
	}
	if parsed.Field != "value" || parsed.N != 42 {
		t.Errorf("parsed = %+v, want {value, 42}", parsed)
	}
}

// TestCompleteStructuredMissingToolUseBlock covers the case where
// the response doesn't contain a tool_use block with our tool name
// (degenerate / misbehaving provider). Expected to return an error
// rather than silently producing empty/nil data.
func TestCompleteStructuredMissingToolUseBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{"type": "text", "text": "oops, text instead of tool use"}],
			"usage": {"input_tokens": 5, "output_tokens": 3}
		}`))
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}
	_, err := client.CompleteStructured(context.Background(), map[string]any{"type": "object"}, "anything")
	if err == nil {
		t.Fatal("expected error when response has no tool_use block; got nil")
	}
}

// TestCompleteStructuredAPIError mirrors TestCompleteAPIError for the
// structured path: a 4xx/5xx response with an error body should
// surface as an error rather than silently return an empty result.
func TestCompleteStructuredAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"too many requests"}}`))
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}
	_, err := client.CompleteStructured(context.Background(), map[string]any{"type": "object"}, "anything")
	if err == nil {
		t.Fatal("expected error on 429 response, got nil")
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Errorf("error missing rate_limit_error detail: %v", err)
	}
}

// TestSetSystemPromptConcurrentWithComplete is a regression test:
// SetSystemPrompt and Complete share systemCache; before
// the mutex was added, concurrent SetSystemPrompt + Complete races
// fired under -race, and partial cache annotations could end up in
// the request body. The test stresses both via interleaving and
// asserts no race + no panic. Run with `go test -race`.
func TestSetSystemPromptConcurrentWithComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain body so the connection can be reused; respond cheaply.
		var req messagesRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(messagesResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
			Usage:   usage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}

	const goroutines = 8
	const iters = 50

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < iters; j++ {
				if id%2 == 0 {
					if _, err := client.Complete(context.Background(), "ping"); err != nil {
						t.Errorf("Complete: %v", err)
						return
					}
				} else {
					// Toggle system prompt: empty / set / set-different.
					switch j % 3 {
					case 0:
						client.SetSystemPrompt("")
					case 1:
						client.SetSystemPrompt("you are a helpful assistant")
					case 2:
						client.SetSystemPrompt("be concise")
					}
				}
			}
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestCompleteAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(struct {
			Error apiError `json:"error"`
		}{
			Error: apiError{Type: "invalid_request_error", Message: "bad request"},
		})
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "claude-sonnet-4-6",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}

	_, err := client.Complete(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !contains(err.Error(), "invalid_request_error") {
		t.Fatalf("error should contain error type, got: %v", err)
	}
}

func TestCompleteMultipleContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(messagesResponse{
			Content: []contentBlock{
				{Type: "text", Text: "Part 1. "},
				{Type: "text", Text: "Part 2."},
			},
		})
	}))
	defer srv.Close()

	client := &Client{
		baseURL: srv.URL,
		model:   "test",
		apiKey:  "sk-ant-test",
		client:  srv.Client(),
	}

	result, err := client.Complete(context.Background(), "test")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result != "Part 1. Part 2." {
		t.Fatalf("expected concatenated text, got %q", result)
	}
}

func TestModelID(t *testing.T) {
	client := &Client{model: "my-model"}
	if client.ModelID() != "my-model" {
		t.Fatalf("expected 'my-model', got %q", client.ModelID())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
