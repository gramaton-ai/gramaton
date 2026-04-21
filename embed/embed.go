package embed

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed/bedrock"
	"github.com/gramaton-ai/gramaton/embed/bert"
	"github.com/gramaton-ai/gramaton/embed/ollama"
	"github.com/gramaton-ai/gramaton/embed/openai"
)

// DefaultContextWindow is the fallback context window (in tokens)
// when auto-detection is unavailable and no config is set.
const DefaultContextWindow = 512

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

	// ContextWindow returns the model's context window in tokens.
	// Used to size chunks before embedding. Implementations should
	// auto-detect where possible (e.g., Ollama /api/show) and fall
	// back to config or DefaultContextWindow.
	ContextWindow() int
}

// QueryEmbedder is implemented by providers that distinguish between
// document-time and query-time embeddings at the underlying API level.
// Cohere on Bedrock accepts an `input_type` field that's
// "search_document" when embedding indexed content and "search_query"
// when embedding a retrieval query; using the document type for queries
// degrades cosine similarity measurably.
//
// Search-time code should type-assert its embedder for QueryEmbedder
// and prefer EmbedQuery when available; non-implementing providers
// (OpenAI, Ollama, Titan) treat queries and documents identically and
// can fall back to Embed. (P1-40.)
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// EmbedForQuery is the canonical helper for embedding a search query.
// Uses the provider's EmbedQuery when implemented; otherwise calls
// Embed with a single-text slice. Returns a single vector (or nil +
// error).
func EmbedForQuery(ctx context.Context, p Provider, text string) ([]float32, error) {
	if qe, ok := p.(QueryEmbedder); ok {
		return qe.EmbedQuery(ctx, text)
	}
	vecs, err := p.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	return vecs[0], nil
}

// New creates an embedding provider from the config. Returns nil if
// no provider is configured (embedding is optional).
func New(cfg config.EmbeddingConfig) (Provider, error) {
	switch cfg.Provider {
	case "bert":
		return bert.New(cfg)
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
