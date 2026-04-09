package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
)

func TestResolveKeyFromEnv(t *testing.T) {
	t.Setenv("TEST_GRAMATON_KEY", "sk-ant-test-key")
	key := secret.ResolveKey("", "TEST_GRAMATON_KEY")
	if key != "sk-ant-test-key" {
		t.Fatalf("expected key from env, got %q", key)
	}
}

func TestResolveKeyDirect(t *testing.T) {
	key := secret.ResolveKey("", "sk-ant-direct-key-value")
	if key != "sk-ant-direct-key-value" {
		t.Fatalf("expected direct key, got %q", key)
	}
}

func TestResolveKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key\n"), 0o600)

	key := secret.ResolveKey(keyPath, "")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected key from file, got %q", key)
	}
}

func TestResolveKeyFileTakesPrecedence(t *testing.T) {
	t.Setenv("TEST_GRAMATON_KEY", "sk-ant-env-key")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key\n"), 0o600)

	key := secret.ResolveKey(keyPath, "TEST_GRAMATON_KEY")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected file key to take precedence, got %q", key)
	}
}

func TestResolveKeyEmpty(t *testing.T) {
	key := secret.ResolveKey("", "")
	if key != "" {
		t.Fatalf("expected empty, got %q", key)
	}
}

func TestResolveKeyUnsetEnv(t *testing.T) {
	key := secret.ResolveKey("", "NONEXISTENT_VAR_12345")
	if key != "" {
		t.Fatalf("expected empty for unset env var, got %q", key)
	}
}

func TestNewWithoutKey(t *testing.T) {
	cfg := config.LLMConfig{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		APIKeyEnv: "NONEXISTENT_KEY_12345",
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error when API key is missing")
	}
}

func TestNewWithDirectKey(t *testing.T) {
	cfg := config.LLMConfig{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		APIKeyEnv: "sk-ant-test-key",
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
		Provider:  "anthropic",
		APIKeyEnv: "sk-ant-test",
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
