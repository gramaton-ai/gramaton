package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
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
			if got := secret.ResolveKey("", tt.val); got != tt.want {
				t.Errorf("ResolveKey(%q) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}
