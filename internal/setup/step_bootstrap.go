package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
)

// stepBootstrap is Step 1: create ~/.gramaton/, set up the embedding
// provider, and (if BERT) download the model. This is the only step
// where we can't easily "skip" -- without an embedding provider,
// Gramaton falls back to BM25-only search, which is a materially
// worse first experience than the product deserves.
//
// Decision: offer 5 embedding options (BERT default, Ollama, OpenAI,
// Bedrock, Skip). BERT is called out as recommended; Skip is labelled
// "not recommended" and explains the downside. Most users should take
// the default by pressing Enter.
//
// Why not auto-detect Ollama and quietly prefer it (as the pre-wizard
// cli/init.go flow did): auto-detection is invisible to users who
// don't know they have Ollama installed, or who want to explicitly
// pick BERT for no-external-dep reasons. The wizard's job is to make
// choices visible and informed; auto-detection without disclosure
// would quietly undermine that.
func (w *Wizard) stepBootstrap(ctx context.Context) error {
	w.writer.StepHeader(1, totalSteps, "Knowledge store")

	w.writer.Paragraph(
		"Pick an embedding provider (powers semantic search):",
	)
	w.writer.Blank()
	w.writer.Raw("    [1] BERT  (recommended)")
	w.writer.Raw("        Fastest. Runs locally. No external cost.")
	w.writer.Raw("        One-time ~130 MB model download.")
	w.writer.Blank()
	w.writer.Raw("    [2] Ollama")
	w.writer.Raw("        Runs locally via Ollama. Use if you already run")
	w.writer.Raw("        Ollama or need a specific model (multilingual, etc.).")
	w.writer.Raw("        Requires Ollama installed separately.")
	w.writer.Blank()
	w.writer.Raw("    [3] OpenAI-compatible API")
	w.writer.Raw("        Cloud-hosted. Slightly higher retrieval quality on")
	w.writer.Raw("        some benchmarks. Tiny cost (~$0.02 per 1M tokens).")
	w.writer.Blank()
	w.writer.Raw("    [4] AWS Bedrock")
	w.writer.Raw("        For AWS accounts. Uses Amazon Titan or Cohere embeddings.")
	w.writer.Raw("        Requires AWS credentials (profile or default chain).")
	w.writer.Blank()
	w.writer.Raw("    [5] Skip  (not recommended)")
	w.writer.Raw("        Keyword search only. Semantic similarity unavailable.")
	w.writer.Raw("        Search quality degrades on conceptual queries.")
	w.writer.Blank()
	w.writer.Prompt(">")

	idx, err := w.prompter.Choice(5, 0) // default [1] BERT on Enter
	if err != nil {
		// Single retry on invalid input. If still bad, fall through
		// to BERT (the safe default) rather than abort the whole
		// wizard -- low-stakes decision, don't block the user.
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt(">")
		idx, err = w.prompter.Choice(5, 0)
		if err != nil {
			w.writer.Warn("Invalid input twice; defaulting to BERT.")
			idx = 0
		}
	}

	// Create the data directory regardless of embedding choice.
	// Perms: 0700 matches the rest of ~/.gramaton/ (owner-only; may
	// hold API keys, user data). Even though DataDir itself isn't
	// secret, keeping the permissions uniform simplifies the
	// security story for docs and for future file-sweep audits.
	if err := os.MkdirAll(w.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Created %s", w.configDir))
	w.writer.Check(fmt.Sprintf("Created %s", w.cfg.DataDir))

	switch idx {
	case 0:
		return w.setupEmbeddingBERT(ctx)
	case 1:
		return w.setupEmbeddingOllama(ctx)
	case 2:
		return w.setupEmbeddingOpenAI(ctx)
	case 3:
		return w.setupEmbeddingBedrock(ctx)
	case 4:
		return w.setupEmbeddingSkip(ctx)
	}
	return fmt.Errorf("unreachable: choice idx %d", idx)
}

// setupEmbeddingBERT configures the pure-Go BERT provider and
// downloads the model via embed.SetupEmbedding. This is the default
// and should succeed for any user with a working internet connection.
func (w *Wizard) setupEmbeddingBERT(ctx context.Context) error {
	w.cfg.Embedding.Provider = "bert"
	// Leave Model empty so embed.SetupEmbedding uses bert.DefaultModel
	// (currently bge-small-en-v1.5). Setting it explicitly here would
	// prevent the default from updating when we bump models in the
	// future.
	w.cfg.Embedding.Model = ""
	w.cfg.Embedding.Dimension = 384 // matches bge-small-en-v1.5

	w.writer.ProgressStart("Downloading embedding model (~130 MB, one time)")
	result := embed.SetupEmbedding(ctx, w.cfg)
	w.writer.ProgressEnd()

	if !result.Configured {
		// Network failure is the common case. Report helpfully rather
		// than abort the wizard -- the user can re-run later, and in
		// the meantime they still get BM25 search.
		w.writer.ErrorLine("Embedding model download failed.")
		for _, msg := range result.Messages {
			w.writer.Paragraph(msg)
		}
		w.writer.Paragraph(
			"",
			"Continuing without embeddings. Semantic search will be",
			"unavailable until you re-run: gramaton init --reconfigure-embedding",
		)
		// Zero the provider so curation doesn't assume embeddings work.
		w.cfg.Embedding.Provider = ""
		return nil
	}

	w.writer.Check(fmt.Sprintf("Embedding model ready (%s via %s)", result.Model, result.Provider))
	return nil
}

// setupEmbeddingOllama configures the Ollama provider and asks
// embed.SetupEmbedding to detect/pull as needed. The embed package
// already handles the "Ollama binary exists? is it running? is the
// model pulled?" chain; we just route to it.
func (w *Wizard) setupEmbeddingOllama(ctx context.Context) error {
	w.cfg.Embedding.Provider = "ollama"
	// Leave Model empty so embed.SetupEmbedding picks a sensible
	// default (currently mxbai-embed-large). Advanced users edit
	// config.yaml afterwards.

	result := embed.SetupEmbedding(ctx, w.cfg)
	if !result.Configured {
		w.writer.ErrorLine("Ollama setup failed.")
		for _, msg := range result.Messages {
			w.writer.Paragraph(msg)
		}
		w.writer.Paragraph(
			"",
			"To use Ollama, install it from https://ollama.com and re-run",
			"the wizard. For now, continuing without embeddings.",
		)
		w.cfg.Embedding.Provider = ""
		return nil
	}
	w.writer.Check(fmt.Sprintf("Embedding configured (%s via %s)", result.Model, result.Provider))
	return nil
}

// setupEmbeddingOpenAI asks for an API key and configures the
// OpenAI-compatible provider. Uses the same api_key_file pattern as
// the LLM step -- we never put keys directly in config.yaml, and the
// file sits at 0600 perms in the config directory.
//
// We do NOT validate the OpenAI key with a test call here (unlike
// the LLM step). Reason: embedding providers are called synchronously
// during every capture; a misconfigured embedding provider will fail
// loud on the first capture and the user will know. Saving an API
// round-trip during init keeps the wizard snappy.
func (w *Wizard) setupEmbeddingOpenAI(ctx context.Context) error {
	w.writer.Paragraph("Enter your OpenAI API key (hidden):")
	w.writer.Prompt(">")
	key, err := w.prompter.Secret()
	if err != nil {
		return err
	}
	if key == "" {
		w.writer.Warn("No key entered; falling back to BERT.")
		return w.setupEmbeddingBERT(ctx)
	}
	keyPath := filepath.Join(w.configDir, "openai.key")
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write openai key: %w", err)
	}

	w.cfg.Embedding.Provider = "openai"
	w.cfg.Embedding.Model = "text-embedding-3-small"
	w.cfg.Embedding.Dimension = 1536
	w.cfg.Embedding.APIKeyFile = keyPath

	w.writer.Check(fmt.Sprintf("API key saved to %s (0600 perms)", keyPath))
	w.writer.Check("Embedding configured (text-embedding-3-small via OpenAI)")
	return nil
}

// setupEmbeddingBedrock prompts for AWS profile + region. Uses the
// default credential chain if profile is blank. Model ID is hardcoded
// to Titan v2 as the safest default (widely available, cheapest) --
// users can edit config.yaml if they want Cohere or a different Titan
// variant.
func (w *Wizard) setupEmbeddingBedrock(ctx context.Context) error {
	w.writer.Paragraph(
		"Which AWS profile should Gramaton use?",
		"(Leave blank to use the default credential chain: env vars, IMDS, SSO, etc.)",
	)
	w.writer.Prompt(">")
	profile, err := w.prompter.Text("")
	if err != nil {
		return err
	}

	w.writer.Paragraph(
		"Which AWS region? (Titan embeddings are available in us-east-1,",
		"us-west-2, eu-central-1, ap-northeast-1, ap-south-1 at time of writing.)",
	)
	w.writer.Prompt(">")
	region, err := w.prompter.Text("us-west-2")
	if err != nil {
		return err
	}

	w.cfg.Embedding.Provider = "bedrock"
	w.cfg.Embedding.Model = "amazon.titan-embed-text-v2:0"
	w.cfg.Embedding.Dimension = 1024
	w.cfg.Embedding.Region = region
	w.cfg.Embedding.AWSProfile = profile

	// Do not validate Bedrock access here: AWS SDK surfaces
	// authentication errors with cryptic messages (STS-specific,
	// profile-specific, etc.) that are hard to translate into
	// actionable wizard feedback. Let the first capture surface any
	// real misconfiguration.
	w.writer.Check("Bedrock embedding configured (amazon.titan-embed-text-v2:0)")
	if profile != "" {
		w.writer.Check(fmt.Sprintf("  AWS profile: %s", profile))
	}
	w.writer.Check(fmt.Sprintf("  Region: %s", region))
	return nil
}

// setupEmbeddingSkip leaves provider empty. Search will use BM25
// only. The wizard already printed the downside in the menu; here we
// just confirm and move on.
func (w *Wizard) setupEmbeddingSkip(_ context.Context) error {
	w.cfg.Embedding.Provider = ""
	w.writer.Warn("Semantic search disabled. Keyword search still works.")
	w.writer.Paragraph(
		"",
		"Re-enable later by editing ~/.gramaton/config.yaml and setting",
		"embedding.provider, or re-run `gramaton init --reconfigure-embedding`.",
	)
	return nil
}

// (Reference: the inline unused-import underscore workaround is not
// needed here -- config is imported directly for the struct fields
// we mutate, even though the alias looks unused to the quick scanner.)
var _ = config.Config{}
