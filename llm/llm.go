package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
	"github.com/gramaton-ai/gramaton/llm/bedrock"
	"github.com/gramaton-ai/gramaton/llm/claudecli"
	"github.com/gramaton-ai/gramaton/llm/kirocli"
	"github.com/gramaton-ai/gramaton/llm/openai"
)

// Structured-output capability is advertised via
// SupportsStructuredOutput on the Provider interface. Callers MUST
// check that before invoking CompleteStructured — providers that
// can't enforce a schema at the wire layer (claude-cli, kiro-cli,
// unimplemented providers) return a generic error from
// CompleteStructured and the caller is expected to have already
// routed around them via the capability check.

// Provider generates text completions from prompts.
type Provider interface {
	// Complete sends a prompt and returns the completion text.
	Complete(ctx context.Context, prompt string) (string, error)

	// CompleteWithModel sends a prompt using a specific model override.
	// If model is empty or unsupported, falls back to the default model.
	//
	// Per-provider semantics vary: anthropic honours the override per
	// call; openai and bedrock ignore the model arg entirely (the model
	// is fixed at client construction time); claude-cli and kiro-cli
	// route the override through their CLI subprocess. Callers that
	// need cross-provider consistency should rely on the model being
	// configured up-front rather than overridden per call.
	CompleteWithModel(ctx context.Context, model, prompt string) (string, error)

	// ModelID returns the identifier of the default model.
	ModelID() string

	// ProviderName returns a short identifier for the provider
	// ("anthropic", "openai", "bedrock", "claude-cli", "kiro-cli").
	// Used for per-provider accounting so CallMetrics can attribute
	// usage to the actual backend instead of the Metered wrapper.
	ProviderName() string

	// SupportsStructuredOutput reports whether the provider can
	// enforce a JSON Schema on its response at the wire layer. When
	// true, CompleteStructured should be preferred for outputs that
	// need to unmarshal reliably — the provider refuses to emit
	// output that doesn't conform to the schema, eliminating the
	// "chatty preamble around JSON" class of parser failures.
	//
	// CLI providers (claude-cli, kiro-cli) return false — they
	// exchange free text with a subprocess and can't enforce schema.
	// Callers that want uniform behavior across all providers should
	// route claude-cli / kiro-cli through Complete + a text parser
	// (e.g. internal/sanitize) as a fallback.
	SupportsStructuredOutput() bool

	// CompleteStructured sends a prompt asking the provider to
	// produce JSON conforming to the supplied JSON Schema. Returns
	// the raw JSON response as-is so the caller can Unmarshal into
	// its typed struct.
	//
	// Providers that return false from SupportsStructuredOutput
	// return a plain error here; callers must check the capability
	// first and fall back to Complete for providers that lack it.
	CompleteStructured(ctx context.Context, schema map[string]any, prompt string) (json.RawMessage, error)
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
		p, err = kirocli.New(cfg.Models.Medium)
	case "claude-cli":
		p, err = claudecli.New(cfg.Models.Medium)
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
		if cfg.CostLimits.RateLimitInterval > 0 {
			interval = cfg.CostLimits.RateLimitInterval
		}
		p = NewRateLimited(p, interval)
	}

	return p, nil
}
