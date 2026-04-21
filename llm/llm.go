package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
	"github.com/gramaton-ai/gramaton/llm/bedrock"
	"github.com/gramaton-ai/gramaton/llm/claudecli"
	"github.com/gramaton-ai/gramaton/llm/kirocli"
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

	// ProviderName returns a short identifier for the provider
	// ("anthropic", "openai", "bedrock", "claude-cli", "kiro-cli").
	// Used for per-provider accounting so CallMetrics can attribute
	// usage to the actual backend instead of the Metered wrapper.
	ProviderName() string
}

// SystemPromptSetter is an optional interface that providers can
// implement to support a persistent system prompt with caching.
// Curation sets this once before a classification batch so that
// the taxonomy is cached across all calls.
type SystemPromptSetter interface {
	SetSystemPrompt(text string)
}

// defaultCLIRateInterval is the minimum time between CLI provider
// calls to avoid hitting subscription rate limits.
const defaultCLIRateInterval = 2 * time.Second

// isCLIProvider returns true for providers backed by CLI tools.
func isCLIProvider(provider string) bool {
	return provider == "kiro-cli" || provider == "claude-cli"
}

// New creates an LLM provider from the config. Returns nil if no
// provider is configured (LLM is optional). CLI providers are
// automatically wrapped with rate limiting.
func New(cfg config.LLMConfig) (Provider, error) {
	var p Provider
	var err error

	switch cfg.Provider {
	case "anthropic":
		return anthropic.New(cfg)
	case "bedrock":
		return bedrock.New(cfg)
	case "openai":
		return openai.New(cfg)
	case "kiro-cli":
		p, err = kirocli.New(cfg.Model)
	case "claude-cli":
		p, err = claudecli.New(cfg.Model)
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
	if err != nil {
		return nil, err
	}

	// Wrap CLI providers with rate limiting.
	if isCLIProvider(cfg.Provider) {
		interval := defaultCLIRateInterval
		if cfg.RateLimitInterval > 0 {
			interval = cfg.RateLimitInterval
		}
		p = NewRateLimited(p, interval)
	}

	return p, nil
}
