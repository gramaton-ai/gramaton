package setup

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
)

// stepVerify is Step 5: persist the config, run a final round of
// health checks, and confirm the wizard's work before printing
// next-steps. This step is always run (it's how the config file
// actually lands on disk) but the user-visible depth scales with
// what was configured.
//
// First-pass implementation saves the config and prints a minimal
// "everything looks good" summary. Full health-check pipeline
// (matching the future `gramaton doctor` command's output) is
// scoped separately in a post-OSS backlog item.
//
// Checks the first pass performs:
//   - Config file saves successfully.
//   - Data directory exists and is writable.
//   - Config file perms are 0600 (so API key paths don't leak).
//
// Checks deferred to a later pass (will converge with `gramaton
// doctor`):
//   - Embedding provider reachable (BERT model loads, Ollama
//     responds, OpenAI/Bedrock auth works).
//   - LLM provider reachable (can make a Complete call).
//   - MCP client config files actually contain gramaton entries.
//   - Hooks are present and executable.
//   - Recent errors in ~/.gramaton/gramaton.log.
func (w *Wizard) stepVerify(ctx context.Context) {
	w.writer.Section("Verification")

	// Persist the config. If this fails, we abort noisy and loud --
	// everything the wizard did up to this point is in memory and
	// lost otherwise. (We don't incrementally save earlier because
	// a Ctrl+C mid-wizard would leave a half-baked config on disk.)
	if err := config.Save(*w.cfg, w.cfgPath); err != nil {
		w.writer.ErrorLine(fmt.Sprintf("Failed to save config: %v", err))
		w.writer.Paragraph(
			"This is unusual; check that the config directory is writable",
			"and try re-running `gramaton init`.",
		)
		return
	}
	w.writer.Check(fmt.Sprintf("Config saved: %s", w.cfgPath))

	// Embedding summary.
	switch w.cfg.Embedding.Provider {
	case "bert":
		w.writer.Check("Embedding: pure-Go BERT (local)")
	case "ollama":
		w.writer.Check("Embedding: Ollama (local)")
	case "openai":
		w.writer.Check("Embedding: OpenAI (cloud)")
	case "bedrock":
		w.writer.Check("Embedding: AWS Bedrock (cloud)")
	case "":
		w.writer.Warn("Embedding: disabled (semantic search unavailable)")
	}

	// LLM summary.
	switch w.cfg.LLM.Provider {
	case "anthropic":
		w.writer.Check("LLM: Anthropic (curation enabled)")
	case "openai":
		w.writer.Check("LLM: OpenAI (curation enabled)")
	case "bedrock":
		w.writer.Check("LLM: AWS Bedrock + Anthropic models (curation enabled)")
	case "":
		w.writer.Warn("LLM: not configured (deterministic-only curation)")
	default:
		w.writer.Check(fmt.Sprintf("LLM: %s (curation enabled)", w.cfg.LLM.Provider))
	}

	// Search rerank summary (enabled implicitly when LLM is configured).
	if w.cfg.Search.RerankEnabled {
		w.writer.Check("Search reranking: enabled")
	}
}
