package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestNewMissingModel(t *testing.T) {
	cfg := config.EmbeddingConfig{Provider: "openai"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New() with empty model should fail")
	}
}

func TestModelID(t *testing.T) {
	c := &Client{model: "text-embedding-3-small"}
	if got := c.ModelID(); got != "text-embedding-3-small" {
		t.Errorf("ModelID() = %q", got)
	}
}

func TestEmbedEmpty(t *testing.T) {
	c := &Client{model: "text-embedding-3-small"}
	got, err := c.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil) = %v", err)
	}
	if got != nil {
		t.Errorf("Embed(nil) = %v, want nil", got)
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}

		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := embeddingResponse{
			Data: make([]embeddingData, len(req.Input)),
		}
		for i := range req.Input {
			resp.Data[i] = embeddingData{
				Index:     i,
				Embedding: []float32{0.1, 0.2, 0.3},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("TEST_OPENAI_KEY", "test-key")
	cfg := config.EmbeddingConfig{
		Provider:  "openai",
		Model:     "text-embedding-3-small",
		BaseURL:   srv.URL,
		APIKeyEnv: "TEST_OPENAI_KEY",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	got, err := c.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d embeddings, want 2", len(got))
	}
	if len(got[0]) != 3 {
		t.Errorf("embedding dim = %d, want 3", len(got[0]))
	}
}

func TestEmbedOutOfOrder(t *testing.T) {
	// OpenAI API may return embeddings out of input order.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Data: []embeddingData{
				{Index: 1, Embedding: []float32{0.4, 0.5}},
				{Index: 0, Embedding: []float32{0.1, 0.2}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "test", client: srv.Client()}
	got, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	// Index 0 should have [0.1, 0.2], index 1 should have [0.4, 0.5].
	if got[0][0] != 0.1 {
		t.Errorf("got[0][0] = %v, want 0.1", got[0][0])
	}
	if got[1][0] != 0.4 {
		t.Errorf("got[1][0] = %v, want 0.4", got[1][0])
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Data: []embeddingData{
				{Index: 0, Embedding: []float32{0.1}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "test", client: srv.Client()}
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on count mismatch")
	}
}

func TestEmbedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "invalid api key",
				"type":    "authentication_error",
			},
		})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "test", client: srv.Client()}
	_, err := c.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestEmbedNoKey(t *testing.T) {
	// Some local servers don't require auth.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("should not send Authorization header when no key")
		}
		resp := embeddingResponse{
			Data: []embeddingData{{Index: 0, Embedding: []float32{0.1}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider: "openai",
		Model:    "local-model",
		BaseURL:  srv.URL,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	got, err := c.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d embeddings, want 1", len(got))
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
			if got := resolveKey(tt.val); got != tt.want {
				t.Errorf("resolveKey(%q) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}
