package embed

import (
	"context"
	"os"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestNewEmptyProvider(t *testing.T) {
	cfg := config.EmbeddingConfig{Provider: ""}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New empty: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil provider for empty config")
	}
}

func TestNewOllamaProvider(t *testing.T) {
	cfg := config.EmbeddingConfig{
		Provider: "ollama",
		Endpoint: "http://localhost:11434",
		Model:    "nomic-embed-text",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New ollama: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider for ollama")
	}
	if p.ModelID() != "nomic-embed-text" {
		t.Fatalf("expected model 'nomic-embed-text', got %q", p.ModelID())
	}
}

func TestNewUnknownProvider(t *testing.T) {
	cfg := config.EmbeddingConfig{Provider: "unknown"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// mockEmbedder records how each text was embedded (Embed vs
// EmbedQuery). Captures the call in order so tests can assert that
// EmbedForQuery routes correctly.
type mockEmbedder struct {
	embedCalls []string
	queryCalls []string
	dim        int
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	m.embedCalls = append(m.embedCalls, texts...)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, m.dim)
	}
	return out, nil
}

func (m *mockEmbedder) ModelID() string    { return "mock" }
func (m *mockEmbedder) ContextWindow() int { return 512 }

type mockQueryEmbedder struct {
	mockEmbedder
}

func (m *mockQueryEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	m.queryCalls = append(m.queryCalls, text)
	return make([]float32, m.dim), nil
}

// TestEmbedForQueryPrefersQueryPath verifies that when a provider
// implements QueryEmbedder, EmbedForQuery routes through EmbedQuery
// instead of falling back to the document-embedding path.
func TestEmbedForQueryPrefersQueryPath(t *testing.T) {
	m := &mockQueryEmbedder{mockEmbedder: mockEmbedder{dim: 4}}
	vec, err := EmbedForQuery(context.Background(), m, "what is a gramaton")
	if err != nil {
		t.Fatalf("EmbedForQuery: %v", err)
	}
	if len(vec) != 4 {
		t.Fatalf("expected 4-dim vector, got %d", len(vec))
	}
	if len(m.queryCalls) != 1 || m.queryCalls[0] != "what is a gramaton" {
		t.Fatalf("expected EmbedQuery to be called, got queryCalls=%v embedCalls=%v", m.queryCalls, m.embedCalls)
	}
	if len(m.embedCalls) != 0 {
		t.Fatalf("EmbedForQuery should not have called Embed, got %v", m.embedCalls)
	}
}

// TestEmbedForQueryFallsBackToEmbed verifies that providers without
// QueryEmbedder fall through to the Embed path for query text.
func TestEmbedForQueryFallsBackToEmbed(t *testing.T) {
	m := &mockEmbedder{dim: 4}
	vec, err := EmbedForQuery(context.Background(), m, "what is a gramaton")
	if err != nil {
		t.Fatalf("EmbedForQuery: %v", err)
	}
	if len(vec) != 4 {
		t.Fatalf("expected 4-dim vector, got %d", len(vec))
	}
	if len(m.embedCalls) != 1 {
		t.Fatalf("expected Embed to be called, got %v", m.embedCalls)
	}
}

func TestSetupEmbeddingNoOllama(t *testing.T) {
	if os.Getenv("GRAMATON_TEST_OLLAMA") == "" {
		t.Skip("skipping: tries to start Ollama (set GRAMATON_TEST_OLLAMA=1 to run)")
	}
	cfg := config.Defaults()
	// SetupEmbedding tries to find Ollama binary. In test environments
	// where Ollama may or may not be installed, we verify it returns
	// a result without panicking.
	result := SetupEmbedding(context.Background(), &cfg)
	// Result.Configured may be true or false depending on environment.
	// Just verify it doesn't crash and returns messages.
	if len(result.Messages) == 0 {
		t.Fatal("expected at least one message from setup")
	}
}
