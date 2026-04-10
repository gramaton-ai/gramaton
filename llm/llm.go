package llm

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
	"github.com/gramaton-ai/gramaton/llm/bedrock"
	"github.com/gramaton-ai/gramaton/llm/openai"
)

// Provider generates text completions from prompts.
type Provider interface {
	// Complete sends a prompt and returns the completion text.
	Complete(ctx context.Context, prompt string) (string, error)

	// CompleteWithModel sends a prompt using a specific model override.
	// If model is empty or unsupported, falls back to the default model.
	CompleteWithModel(ctx context.Context, model, prompt string) (string, error)

	// ModelID returns the identifier of the default model.
	ModelID() string
}

// SystemPromptSetter is an optional interface that providers can
// implement to support a persistent system prompt with caching.
// Curation sets this once before a classification batch so that
// the taxonomy is cached across all calls.
type SystemPromptSetter interface {
	SetSystemPrompt(text string)
}

// New creates an LLM provider from the config. Returns nil if no
// provider is configured (LLM is optional).
func New(cfg config.LLMConfig) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return anthropic.New(cfg)
	case "bedrock":
		return bedrock.New(cfg)
	case "openai":
		return openai.New(cfg)
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
