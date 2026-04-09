package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			// Context window detection -- return model info.
			json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{
					"bert.context_length": float64(512),
				},
			})
			return
		}
		if r.URL.Path != "/api/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}

		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if req.Model != "nomic-embed-text" {
			t.Fatalf("expected model 'nomic-embed-text', got %q", req.Model)
		}

		resp := embedResponse{
			Model: req.Model,
			Embeddings: make([][]float32, len(req.Input)),
		}
		for i := range req.Input {
			resp.Embeddings[i] = []float32{float32(i) * 0.1, float32(i) * 0.2, float32(i) * 0.3}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "nomic-embed-text")
	embeddings, err := client.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}
	if len(embeddings[0]) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(embeddings[0]))
	}
	// First text: index 0 → [0.0, 0.0, 0.0]
	if embeddings[0][0] != 0.0 {
		t.Fatalf("expected 0.0, got %f", embeddings[0][0])
	}
	// Second text: index 1 → [0.1, 0.2, 0.3]
	if embeddings[1][0] != 0.1 {
		t.Fatalf("expected 0.1, got %f", embeddings[1][0])
	}
}

func TestEmbedEmpty(t *testing.T) {
	client := New("http://unused", "test")
	embeddings, err := client.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed empty: %v", err)
	}
	if embeddings != nil {
		t.Fatal("expected nil for empty input")
	}
}

func TestEmbedSingle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Model:      "test",
			Embeddings: [][]float32{{0.5, 0.6, 0.7}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "test")
	embeddings, err := client.Embed(context.Background(), []string{"single"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embeddings) != 1 {
		t.Fatalf("expected 1, got %d", len(embeddings))
	}
}

func TestEmbedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(errorResponse{Error: "model not found"})
	}))
	defer server.Close()

	client := New(server.URL, "missing-model")
	_, err := client.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestEmbedConnectionRefused(t *testing.T) {
	client := New("http://localhost:1", "test")
	_, err := client.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Model:      "test",
			Embeddings: [][]float32{{0.1}}, // Only 1 embedding for 2 inputs.
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "test")
	_, err := client.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
}

func TestModelID(t *testing.T) {
	client := New("http://unused", "nomic-embed-text")
	if client.ModelID() != "nomic-embed-text" {
		t.Fatalf("expected 'nomic-embed-text', got %q", client.ModelID())
	}
}

func TestContextWindowAutoDetect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{
					"bert.context_length": float64(512),
				},
			})
			return
		}
		// Embed handler.
		resp := embedResponse{
			Model:      "test",
			Embeddings: [][]float32{{0.1, 0.2}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "mxbai-embed-large")
	if client.ContextWindow() != 512 {
		t.Fatalf("ContextWindow = %d, want 512", client.ContextWindow())
	}
}

func TestContextWindowFallback(t *testing.T) {
	// Server that doesn't support /api/show.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := New(server.URL, "unknown-model")
	if client.ContextWindow() != 0 {
		t.Fatalf("ContextWindow = %d, want 0 (fallback)", client.ContextWindow())
	}
}
