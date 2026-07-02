package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	// nonInteractive forces the legacy non-interactive bootstrap even
	// when stdin is a TTY. Useful for scripts that explicitly want the
	// old behavior, or for debugging the non-interactive code path.
	nonInteractive bool

	// force bypasses the "already initialized" guard so users can
	// re-run the wizard to pick up new proxy-script templates, new
	// MCP registration logic, or other wizard-touched state without
	// manually deleting config.yaml.
	force bool

	// author is the git-style identity records created in the store
	// are attributed to ("Ada Lovelace <ada@example.com>", or a bare
	// name). When set, the wizard skips its identity prompts and the
	// non-interactive path skips its OS-account fallback.
	author string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Gramaton configuration",
	Long: `Creates the configuration directory, default config file, and data
directory, and sets up embedding + LLM providers.

When stdin is a terminal, init runs an interactive wizard that walks
through provider choice, API key entry, and MCP client registration.
With --non-interactive (or when stdin is piped), init bootstraps with
defaults only and prints instructions for completing setup manually.`,
	RunE: runInit,
}

func init() {
	initCmd.Flags().BoolVar(&nonInteractive, "non-interactive", false,
		"skip the interactive wizard and bootstrap with defaults only")
	initCmd.Flags().BoolVar(&force, "force", false,
		"re-run the wizard even when config.yaml already exists (re-materializes hooks, re-registers MCP)")
	initCmd.Flags().StringVar(&author, "author", "",
		"author identity for records created in the store, git-style: \"Ada Lovelace <ada@example.com>\"")
	rootCmd.AddCommand(initCmd)
}

// runInit is the entry point for `gramaton init`. It decides between
// the interactive wizard (TTY + no --non-interactive flag) and the
// legacy non-interactive bootstrap, then dispatches.
//
// The decision tree:
//
//	--non-interactive OR stdin is not a TTY  ->  runInitNonInteractive
//	otherwise                                 ->  runInitInteractive
//
// This is the only place where TTY detection happens; everything
// downstream of this receives an explicit interactive/non-interactive
// signal. Keeping the check here (rather than per-step) avoids a
// class of bugs where one step guesses TTY state differently from
// another.
func runInit(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Guard against re-init unless --force. The wizard has an
	// "existing install" menu planned (reconfigure MCP, reset
	// everything, abort), but it's not implemented in the first
	// pass — --force is the escape hatch that lets users re-run
	// the full wizard to pick up bug fixes, new proxy-script
	// templates, or updated MCP registration logic.
	if _, err := os.Stat(cfgPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "Already initialized: %s\n", cfgPath)
		fmt.Fprintln(os.Stderr, "Edit the config file directly, delete it and re-run init, or pass --force to re-run without deleting.")
		return nil
	}

	// Honor explicit opt-out of interactivity, and fall through when
	// stdin isn't a terminal (piped/redirected). term.IsTerminal must
	// be checked against the real os.Stdin fd; we don't let callers
	// override it because masking a non-TTY as interactive would hang
	// the wizard waiting on input that will never come.
	interactive := !nonInteractive && term.IsTerminal(int(os.Stdin.Fd()))

	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")

	// --author applies to both paths: the wizard sees a populated
	// cfg.Author and skips its identity prompts; the non-interactive
	// path uses it instead of the OS-account fallback.
	if author != "" {
		parsed, err := parseAuthor(author)
		if err != nil {
			return err
		}
		cfg.Author = parsed
	}

	if interactive {
		return runInitInteractive(cmd.Context(), &cfg, cfgPath, dir)
	}
	return runInitNonInteractive(cmd.Context(), cfg, cfgPath, dir)
}

// runInitInteractive drives the setup package's wizard. The cfg passed
// in is defaults + DataDir resolved; the wizard mutates it through the
// steps and persists at the verification step.
func runInitInteractive(ctx context.Context, cfg *config.Config, cfgPath, dir string) error {
	wiz := setup.New(
		setup.NewTerminalPrompter(),
		setup.NewTerminalWriter(),
		cfg,
		cfgPath,
		dir,
	)
	return wiz.Run(ctx)
}

// runInitNonInteractive preserves the original `gramaton init` behavior
// for scripts and piped stdin: create the dirs, auto-detect an
// embedding provider via embed.SetupEmbedding, save the config, print
// terse status. No prompts, no LLM setup, no MCP injection.
//
// This is the backward-compatibility path. The wizard (interactive
// path) is the recommended flow for users; this path exists so
// existing CI scripts and Docker-image bootstraps keep working.
func runInitNonInteractive(ctx context.Context, cfg config.Config, cfgPath, dir string) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	fmt.Printf("Initialized Gramaton at %s\n", dir)
	fmt.Printf("  Config: %s\n", cfgPath)
	fmt.Printf("  Data:   %s\n", cfg.DataDir)
	fmt.Println()

	configured := setupEmbeddingNonInteractive(ctx, &cfg, cfgPath)

	// Author fallback: --author (parsed into cfg.Author by runInit)
	// wins; otherwise default the name from the OS account so records
	// carry a usable identity even in scripted setups. Email has no
	// safe guess, so it stays blank.
	defaultAuthor(&cfg)

	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if configured {
		fmt.Println()
		fmt.Println("Gramaton is set up but an LLM provider was not configured.")
		fmt.Println("For autonomous curation (strongly recommended), run:")
		fmt.Println("  gramaton init  (interactive, in a terminal)")
		fmt.Println("or edit ~/.gramaton/config.yaml manually. See docs/providers.md.")
	}

	return nil
}

// parseAuthor parses the --author flag's git-style identity string
// into its config fields. Accepted forms:
//
//	"Ada Lovelace <ada@example.com>"  ->  Name + Email
//	"Ada Lovelace"                    ->  Name only
//	"<ada@example.com>"               ->  Email only
//
// The split is at the LAST '<' so a name containing '<' still
// parses; the remainder must end with '>' or the value is rejected
// as malformed (a stray '<' is far more likely a quoting mistake
// than an intentional name character).
func parseAuthor(s string) (config.AuthorConfig, error) {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, "<")
	if i < 0 {
		return config.AuthorConfig{Name: s}, nil
	}
	rest := strings.TrimSpace(s[i+1:])
	if !strings.HasSuffix(rest, ">") {
		return config.AuthorConfig{}, fmt.Errorf("invalid --author %q: '<' without a closing '>'; expected \"Name <email>\"", s)
	}
	return config.AuthorConfig{
		Name:  strings.TrimSpace(s[:i]),
		Email: strings.TrimSpace(strings.TrimSuffix(rest, ">")),
	}, nil
}

// defaultAuthor fills a blank cfg.Author with the OS account name.
// Non-interactive path only: the wizard covers the interactive
// equivalent with a prompt (same preference order -- full name,
// then username, then empty). A cfg.Author populated from --author
// is left untouched.
func defaultAuthor(cfg *config.Config) {
	if cfg.Author == (config.AuthorConfig{}) {
		cfg.Author.Name = setup.OSAccountName()
	}
}

// setupEmbeddingNonInteractive is the same flow the old gramaton init
// used: detect + auto-configure an embedding provider, report status.
// Factored into a named helper so the legacy path stays readable.
func setupEmbeddingNonInteractive(ctx context.Context, cfg *config.Config, cfgPath string) bool {
	fmt.Println("Checking for embedding providers...")
	fmt.Println()

	result := embed.SetupEmbedding(ctx, cfg)
	for _, msg := range result.Messages {
		fmt.Printf("  %s\n", msg)
	}

	if !result.Configured {
		printEmbeddingSetupFailed()
		return false
	}

	fmt.Println()
	fmt.Printf("  Embedding configured: %s with %s\n", result.Provider, result.Model)
	return true
}

// printEmbeddingSetupFailed prints the recovery guidance when the
// non-interactive flow can't configure an embedding provider. Kept
// verbatim from the pre-wizard code; still accurate.
func printEmbeddingSetupFailed() {
	fmt.Println("  Embedding setup failed.")
	fmt.Println()
	fmt.Println("  Gramaton's default embedding provider is a pure-Go BERT embedder")
	fmt.Println("  that downloads the model (~130MB) from HuggingFace on first run.")
	fmt.Println("  Setup probably failed because of a network issue.")
	fmt.Println()
	fmt.Println("  Options:")
	fmt.Println("    1. Check your internet connection and re-run: gramaton init")
	fmt.Println("    2. Use Ollama as an alternative local embedding provider:")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("         Download from https://ollama.com/download/mac")
		fmt.Println("         Or: brew install ollama")
	case "linux":
		fmt.Println("         curl -fsSL https://ollama.com/install.sh | sh")
	default:
		fmt.Println("         Download from https://ollama.com")
	}
	fmt.Println("       Then re-run: gramaton init")
	fmt.Println("    3. Configure OpenAI or AWS Bedrock manually. See docs/providers.md.")
	fmt.Println()
	fmt.Println("  Gramaton also works without embeddings (keyword and graph search")
	fmt.Println("  still work), but semantic similarity search requires them.")
}
