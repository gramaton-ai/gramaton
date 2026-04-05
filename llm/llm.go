package llm

import (
	"context"
	"fmt"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/llm/anthropic"
)

// Provider generates text completions from prompts.
type Provider interface {
	// Complete sends a prompt and returns the completion text.
	Complete(ctx context.Context, prompt string) (string, error)

	// ModelID returns the identifier of the model being used.
	ModelID() string
}

// New creates an LLM provider from the config. Returns nil if no
// provider is configured (LLM is optional).
func New(cfg config.LLMConfig) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return anthropic.New(cfg)
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
