package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"

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

	// cleanups holds undo-actions registered by steps that wrote
	// persistent state before the final Step 5 commit. On success,
	// the list is discarded. On interrupt (Ctrl+C, panic, or
	// explicit early-return), the list is executed LIFO so the
	// user's filesystem ends in a clean state -- no orphan API key
	// files, no half-baked embedded hook trees.
	//
	// Each entry is a nullary cleanup function. Steps are expected
	// to be idempotent in their own cleanups (e.g., os.Remove is
	// fine if the file was already removed by something else).
	cleanups   []func()
	cleanupsMu sync.Mutex

	// committed flips to true once the wizard has reached its
	// successful end state (Step 5 has persisted config.yaml and
	// printed next-steps). After commit, the cleanup stack is
	// discarded -- the user's state is the intended final state,
	// any interrupt from here on should NOT roll back.
	committed bool
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
const totalSteps = 5

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
// addCleanup registers a function to be executed on wizard interrupt.
// Called by steps after they write persistent state (e.g., API key
// files). Safe for concurrent calls, though in practice the wizard
// is single-threaded.
func (w *Wizard) addCleanup(fn func()) {
	w.cleanupsMu.Lock()
	defer w.cleanupsMu.Unlock()
	w.cleanups = append(w.cleanups, fn)
}

// runCleanups executes registered cleanups LIFO and clears the list.
// Called from the SIGINT handler and from Run's deferred rollback on
// error. Idempotent: calling it twice is a no-op the second time.
func (w *Wizard) runCleanups() {
	w.cleanupsMu.Lock()
	cleanups := w.cleanups
	w.cleanups = nil
	w.cleanupsMu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// markCommitted flips the wizard into its post-success state:
// registered cleanups are discarded without running (they would
// destroy the user's intended final state), and subsequent
// interrupts leave the filesystem as-is.
func (w *Wizard) markCommitted() {
	w.cleanupsMu.Lock()
	defer w.cleanupsMu.Unlock()
	w.cleanups = nil
	w.committed = true
}

// installInterruptHandler registers a SIGINT handler that runs
// cleanups before exiting. Returns an unregister function the caller
// must call when the wizard completes (successfully or otherwise) so
// the Go runtime can restore default signal handling.
//
// Why explicit signal handling (vs relying on deferred cleanups):
// Go's default Ctrl+C behavior is os.Exit(130), which does NOT run
// deferred functions. We need an explicit handler to get the
// cleanup stack to fire on interrupt. Once the wizard exits by any
// other path (error return, normal success), the unregister func
// restores default behavior.
func (w *Wizard) installInterruptHandler() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			// Keep this short: printing much from a signal handler
			// interleaves badly with terminal state (especially if
			// we interrupted a hidden-input Secret() read). A single
			// line is the best we can do cleanly.
			w.writer.Blank()
			w.writer.Warn("Interrupted -- rolling back partial state...")
			w.runCleanups()
			// 130 is the conventional exit code for SIGINT (128 + 2).
			os.Exit(130)
		case <-done:
			return
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func (w *Wizard) Run(ctx context.Context) error {
	// Install the interrupt handler first so every downstream
	// file-writing step can trust cleanup-on-Ctrl+C. Unregister on
	// any exit path to restore default signal handling.
	unregister := w.installInterruptHandler()
	defer unregister()

	// If Run returns with an error (any step blew up), roll back
	// before unregistering. Success commits its own state via
	// markCommitted, which zeroes the cleanup list -- the deferred
	// runCleanups becomes a no-op.
	defer w.runCleanups()

	w.welcome()

	importing, err := w.askImportOrFresh()
	if err != nil {
		return err
	}

	if importing {
		// runImport replaces Step 1 bootstrap: restore populates the
		// data directory atomically from the archive. Steps 2-4 still
		// run because keys are stripped from backups and MCP/hooks
		// are per-machine.
		if err := w.runImport(ctx); err != nil {
			return fmt.Errorf("import: %w", err)
		}
	} else {
		if err := w.stepBootstrap(ctx); err != nil {
			return fmt.Errorf("bootstrap step: %w", err)
		}
	}

	if err := w.stepLLM(ctx); err != nil {
		return fmt.Errorf("LLM step: %w", err)
	}
	if err := w.stepMCP(ctx); err != nil {
		return fmt.Errorf("MCP step: %w", err)
	}
	if err := w.stepInstructions(ctx); err != nil {
		return fmt.Errorf("instructions step: %w", err)
	}
	if err := w.stepHooks(ctx); err != nil {
		return fmt.Errorf("hooks step: %w", err)
	}
	w.stepVerify(ctx)
	// Commit before nextSteps: once stepVerify has persisted
	// config.yaml, the user's state IS the intended final state.
	// Any subsequent interrupt should leave everything in place.
	w.markCommitted()
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
	w.writer.Raw("    [1] First time  (or re-running to reconfigure — won't touch your existing data)")
	w.writer.Raw("    [2] Import a backup from another computer  (replaces data with the archive)")
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
