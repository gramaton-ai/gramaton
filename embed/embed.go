package embed

import (
	"context"
	"fmt"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/embed/bedrock"
	"github.com/brandonlattin/gramaton/embed/ollama"
	"github.com/brandonlattin/gramaton/embed/openai"
)

// Provider generates vector embeddings from text. This is the shared
// interface with multiple implementations -- the correct Go reason to
// define an interface at the provider (D29).
type Provider interface {
	// Embed generates embeddings for one or more texts. Returns one
	// vector per input text, in the same order. Implementations should
	// support batching where the underlying provider allows it.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// ModelID returns the identifier of the model being used, for
	// tracking in the embedding_model property on nodes.
	ModelID() string
}

// New creates an embedding provider from the config. Returns nil if
// no provider is configured (embedding is optional).
func New(cfg config.EmbeddingConfig) (Provider, error) {
	switch cfg.Provider {
	case "ollama":
		return ollama.New(cfg.Endpoint, cfg.Model), nil
	case "bedrock":
		return bedrock.New(cfg)
	case "openai":
		return openai.New(cfg)
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("embed: unknown provider %q", cfg.Provider)
	}
}
