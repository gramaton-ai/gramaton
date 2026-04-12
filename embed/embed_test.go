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
