package embed

import (
	"context"
	"fmt"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/embed/ollama"
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
func SetupEmbedding(ctx context.Context, cfg *config.Config) SetupResult {
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
