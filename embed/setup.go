package embed

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed/bert"
	"github.com/gramaton-ai/gramaton/embed/ollama"
)

// SetupResult describes what the setup process found and configured.
type SetupResult struct {
	Configured bool
	Provider   string
	Model      string
	Messages   []string
}

// SetupEmbedding detects and configures an embedding provider. Modifies
// cfg in place. Returns a result with status messages for display.
//
// Priority: if provider is already set (e.g., from config), use it.
// Otherwise try built-in BERT first (always available, just needs model
// download), then fall back to Ollama detection.
func SetupEmbedding(ctx context.Context, cfg *config.Config) SetupResult {
	// If provider is explicitly configured and not "bert", use it as-is.
	if cfg.Embedding.Provider != "" && cfg.Embedding.Provider != "bert" {
		return setupOllama(ctx, cfg)
	}

	// Default: built-in BERT provider.
	return setupBERT(ctx, cfg)
}

func setupBERT(ctx context.Context, cfg *config.Config) SetupResult {
	result := SetupResult{}
	model := cfg.Embedding.Model
	if model == "" {
		model = bert.DefaultModel
	}

	repo := bert.DefaultModelRepo
	if model != bert.DefaultModel {
		repo = model
	}

	result.Messages = append(result.Messages, fmt.Sprintf("Setting up built-in BERT embedder (%s)...", model))

	if err := bert.EnsureModel(ctx, repo, model, func(msg string) {
		result.Messages = append(result.Messages, msg)
	}); err != nil {
		result.Messages = append(result.Messages, fmt.Sprintf("BERT setup failed: %s", err))
		result.Messages = append(result.Messages, "Falling back to Ollama...")
		return setupOllama(ctx, cfg)
	}

	result.Messages = append(result.Messages, fmt.Sprintf("Model %s ready", model))
	cfg.Embedding.Provider = "bert"
	cfg.Embedding.Model = model
	result.Configured = true
	result.Provider = "bert"
	result.Model = model
	return result
}

func setupOllama(ctx context.Context, cfg *config.Config) SetupResult {
	result := SetupResult{}
	endpoint := cfg.Embedding.Endpoint
	model := cfg.Embedding.Model

	// Check if Ollama binary exists.
	bin := ollama.FindBinary()
	if bin == "" {
		result.Messages = append(result.Messages, "Ollama not found")
		return result
	}
	result.Messages = append(result.Messages, fmt.Sprintf("Ollama binary: %s", bin))

	// Ensure Ollama is running.
	if !ollama.IsReachable(endpoint) {
		result.Messages = append(result.Messages, "Starting Ollama...")
		if err := ollama.EnsureRunning(endpoint); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("Could not start Ollama: %s", err))
			return result
		}
	}
	result.Messages = append(result.Messages, fmt.Sprintf("Ollama responding at %s", endpoint))

	// Check for embedding model.
	if !ollama.HasModel(endpoint, model) {
		result.Messages = append(result.Messages, fmt.Sprintf("Pulling %s...", model))
		err := ollama.PullModel(ctx, endpoint, model, func(msg string) {
			result.Messages = append(result.Messages, msg)
		})
		if err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("Could not pull model: %s", err))
			return result
		}
	}
	result.Messages = append(result.Messages, fmt.Sprintf("Model %s available", model))

	cfg.Embedding.Provider = "ollama"
	result.Configured = true
	result.Provider = "ollama"
	result.Model = model
	return result
}
