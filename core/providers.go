package core

import (
	"log/slog"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/llm"
)

// providers groups the external dependency clients held by the
// engine. Both fields may be nil: embedder is required for any vector
// work but the engine can technically run without one (search degrades);
// llm is always optional and curation/rerank silently skip when absent.
type providers struct {
	embedder embed.Provider
	llm      llm.Provider
}

// newProviders constructs the embedding and LLM providers from cfg.
// An llm misconfiguration is logged but not fatal -- the rest of the
// engine runs without curation. An embedder construction error is
// fatal, mirroring the historical LoadEngine behavior.
func newProviders(cfg config.Config) (*providers, error) {
	emb, err := embed.New(cfg.Embedding)
	if err != nil {
		return nil, err
	}
	llmProv, err := llm.New(cfg.LLM)
	if err != nil {
		if cfg.LLM.Provider != "" {
			slog.Error("llm provider configured but failed to initialise",
				"component", "engine",
				"provider", cfg.LLM.Provider,
				"err", err)
		}
		llmProv = nil
	}
	return &providers{embedder: emb, llm: llmProv}, nil
}
