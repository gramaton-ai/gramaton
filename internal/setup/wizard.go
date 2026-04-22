package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
)

// Wizard orchestrates the interactive first-run setup. A single
// Wizard instance runs once per `gramaton init` invocation; the type
// is not concurrency-safe and is not intended to be.
//
// Construction injects three things:
//   - Prompter: how the wizard reads input (terminal in production,
//     scripted in tests).
//   - Writer: how the wizard prints output (stdout in production,
//     buffer in tests).
//   - cfg / cfgPath / configDir: the target state the wizard mutates.
//     The wizard writes to cfg in memory and persists via config.Save
//     at well-defined checkpoints; callers passing a partially-
//     populated cfg (e.g., pre-loaded from defaults + a partial
//     user-provided override) is supported.
//
// Why inject the Prompter/Writer instead of reading os.Stdin / writing
// os.Stdout directly in the wizard code:
//   - Every step becomes unit-testable.
//   - Steps stay unaware of what "a terminal" is; they just ask the
//     Prompter for a Yes/No or print to the Writer.
//   - A future non-CLI driver (`gramaton doctor --fix`, for example,
//     which might want to re-run Step 3 MCP injection headlessly)
//     can swap the Prompter for a non-interactive stub.
type Wizard struct {
	prompter  Prompter
	writer    Writer
	cfg       *config.Config
	cfgPath   string
	configDir string

	// mcpBackend handles MCP client detection + registration in
	// Step 3. Defaulted in New to DefaultMCPBackend (shells out to
	// `claude mcp add` etc.). Tests override by assigning a fake
	// after construction; the field is unexported so only the
	// setup package itself (including its tests) can swap it.
	mcpBackend MCPBackend

	// hookBackend handles hook script materialization and client
	// hook-config patching in Step 4. Defaulted in New to
	// DefaultHookBackend (embed.FS extraction + settings.json JSON
	// patching). Tests swap it for a fake.
	hookBackend HookBackend
}

// New constructs a Wizard. cfg may be any config.Config (typically
// config.Defaults() with the DataDir already resolved by the caller).
// cfgPath is the path we'll eventually write the final config to.
// configDir is the parent directory of cfgPath and is where the
// wizard drops ancillary files (API key files, model cache, etc.).
func New(prompter Prompter, writer Writer, cfg *config.Config, cfgPath, configDir string) *Wizard {
	return &Wizard{
		prompter:    prompter,
		writer:      writer,
		cfg:         cfg,
		cfgPath:     cfgPath,
		configDir:   configDir,
		mcpBackend:  DefaultMCPBackend{},
		hookBackend: DefaultHookBackend{},
	}
}

// totalSteps is the number shown in "Step N of M" headers. The wizard
// has 5 logical steps but only 4 are user-facing (Step 0 -- branching
// fresh vs import -- is a single prompt, not a numbered step). Kept
// as a constant so a refactor that adds/removes a step updates every
// header consistently.
const totalSteps = 4

// Run drives the full wizard flow. It returns a non-nil error only
// for fatal problems that leave the system in an unusable state;
// recoverable issues (network hiccup, user skipped an optional step)
// are reported via the Writer and Run returns nil.
//
// Step order:
//  0. Welcome banner + fresh-vs-import branch.
//  1. Knowledge-store bootstrap (data dir, embedding, model download).
//  2. LLM provider + API key + cost caps (optional but strongly
//     recommended).
//  3. MCP client auto-detect + config injection (Claude Code, kiro-cli).
//  4. Auto-capture hook installer.
//  5. Verification + concrete next-steps block.
//
// The import branch (Step 0 -> [2]) skips Step 1's bootstrap (the
// restored archive populates the data dir) but still walks through
// Steps 2-4 because API keys, MCP registration, and hooks are all
// per-machine and deliberately not included in backups.
func (w *Wizard) Run(ctx context.Context) error {
	w.welcome()

	importing, err := w.askImportOrFresh()
	if err != nil {
		return err
	}

	if importing {
		// Import branch is TODO: needs the `backup.Restore` wiring
		// and a server-start-for-restore dance. For this first pass
		// we surface the gap clearly rather than silently fall back,
		// so users who picked [2] understand they need to use the
		// manual `gramaton restore` flow for now.
		w.writer.Blank()
		w.writer.Warn("Import flow is not yet implemented in the wizard.")
		w.writer.Paragraph(
			"For now, run `gramaton init` to bootstrap this machine, then",
			"run `gramaton restore <path-to-backup>` to load your data.",
			"You'll also need to re-run `gramaton set-key` afterwards --",
			"API keys are stripped from backups for safety.",
		)
		return nil
	}

	if err := w.stepBootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap step: %w", err)
	}
	if err := w.stepLLM(ctx); err != nil {
		return fmt.Errorf("LLM step: %w", err)
	}
	if err := w.stepMCP(ctx); err != nil {
		return fmt.Errorf("MCP step: %w", err)
	}
	if err := w.stepHooks(ctx); err != nil {
		return fmt.Errorf("hooks step: %w", err)
	}
	w.stepVerify(ctx)
	w.nextSteps()

	return nil
}

// welcome prints the framing banner. Brief on purpose -- a tech-
// capable user reading "Welcome to Gramaton" has already decided to
// install; we don't need to re-sell the product.
func (w *Wizard) welcome() {
	w.writer.Section("Welcome to Gramaton")
	w.writer.Paragraph(
		"A local, versioned knowledge store for AI agents.",
		"",
		"I'll get you set up in about 2-3 minutes. Press Ctrl+C at",
		"any time to exit -- nothing is persisted until the end of",
		"each step.",
	)
}

// askImportOrFresh is Step 0: route between fresh install and import.
// Kept as a named method (not inlined) because the same branching
// logic will be called from a future "reconfigure existing install"
// menu, and factoring it now avoids a later refactor.
//
// Returns (true, nil) for import, (false, nil) for fresh.
func (w *Wizard) askImportOrFresh() (bool, error) {
	w.writer.Blank()
	w.writer.Paragraph(
		"Is this your first time using Gramaton, or are you importing",
		"an existing knowledge store from another computer?",
	)
	w.writer.Blank()
	w.writer.Raw("    [1] First time")
	w.writer.Raw("    [2] Import a backup from another computer")
	w.writer.Blank()
	w.writer.Prompt(">")

	idx, err := w.prompter.Choice(2, 0)
	if err != nil {
		if errors.Is(err, ErrAborted) {
			return false, err
		}
		// Re-prompt on invalid input with a user-facing error. Keeping
		// the re-prompt loop here (not inside Prompter.Choice) lets
		// each caller control its own retry policy -- e.g., the LLM-
		// key step silently retries bad formats, while some future
		// destructive step might abort on anything unexpected.
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt(">")
		idx, err = w.prompter.Choice(2, 0)
		if err != nil {
			return false, err
		}
	}
	return idx == 1, nil
}

// nextSteps prints the end-of-wizard block with concrete actions
// the user should take in the next ~60 seconds. Follows the
// "Setup-wizard language and UX design principles" Memory record:
// every wizard flow ends with a concrete "what's next" block, not
// a generic "done" message.
func (w *Wizard) nextSteps() {
	w.writer.Section("Gramaton is ready.")
	w.writer.Paragraph(
		"Next steps:",
		"",
		"  1. Restart your AI client (Claude Code, kiro-cli) so it",
		"     picks up the new MCP configuration.",
		"",
		"  2. Try it -- in your AI assistant, say:",
		"     \"Remember that my favorite editor is neovim.\"",
		"     Then in a new session, ask:",
		"     \"What's my favorite editor?\"",
		"",
		"  3. Questions or issues?",
		"     https://github.com/gramaton-ai/gramaton/discussions",
	)
	w.writer.Blank()
}
