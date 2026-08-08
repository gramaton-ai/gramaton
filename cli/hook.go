package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/gramaton-ai/gramaton/hooks"
)

// hookCmd is the internal dispatch point for Claude Code / Kiro
// lifecycle hooks. The proxy scripts installed by `gramaton init`
// at ~/.gramaton/hooks/**/*.{sh,cmd} execute one of the event names
// below; all real logic lives in the hooks/ Go package.
//
// Hidden from --help because users don't invoke this directly —
// Claude Code / Kiro do, via their respective settings.json entries.
var hookCmd = &cobra.Command{
	Use:    "hook <event>",
	Short:  "Dispatch agent lifecycle hook (internal)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runHook,
}

// hookEvents maps the event-name positional arg to the matching
// handler in hooks/. Adding a new event: wire it here + in the
// init-time proxy-generation table in internal/setup/hooks.go.
var hookEvents = map[string]func(stdin *os.File, stdout *os.File){
	// Claude Code
	"session-start":      func(in, out *os.File) { hooks.ClaudeCodeSessionStart(in, out) },
	"stop":               func(in, out *os.File) { hooks.ClaudeCodeStop(in, out) },
	"pre-compact":        func(in, out *os.File) { hooks.ClaudeCodePreCompact(in, out) },
	"post-compact":       func(in, out *os.File) { hooks.ClaudeCodePostCompact(in, out) },
	"user-prompt-submit": func(in, out *os.File) { hooks.ClaudeCodeUserPromptSubmit(in, out) },
	// Kiro
	"kiro-agent-spawn":        func(in, out *os.File) { hooks.KiroAgentSpawn(in, out) },
	"kiro-user-prompt-submit": func(in, out *os.File) { hooks.KiroUserPromptSubmit(in, out) },
	"kiro-stop":               func(in, out *os.File) { hooks.KiroStop(in, out) },
	// Cursor (stdin adapted: conversation_id → session id,
	// workspace_roots[0] → cwd)
	"cursor-session-start": func(in, out *os.File) { hooks.CursorSessionStart(in, out) },
	"cursor-stop":          func(in, out *os.File) { hooks.CursorStop(in, out) },
	"cursor-pre-compact":   func(in, out *os.File) { hooks.CursorPreCompact(in, out) },
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

func runHook(cmd *cobra.Command, args []string) error {
	event := args[0]
	handler, ok := hookEvents[event]
	if !ok {
		// Unknown events log to stderr but exit 0. An unknown event
		// usually means version skew, not a programmer error: a
		// settings.json entry for a newly-added event outlives
		// whichever older binary a proxy script resolves (init from
		// a build tree, a rollback, a second install on PATH), and
		// a non-zero exit would surface as a harness hook error on
		// every firing -- for user-prompt-submit, every prompt. The
		// build-time programmer-error case is already covered by
		// cli/hook_test.go's dispatchability test. Stderr keeps the
		// diagnostic visible in hook debug output.
		fmt.Fprintf(os.Stderr, "gramaton hook: unknown event %q (this binary predates it; re-run gramaton init or upgrade)\n", event)
		return nil
	}
	// Handlers are fail-open — they log errors to
	// ~/.gramaton/hooks.log and return without raising. We
	// always exit 0 so Claude Code / Kiro are never blocked by
	// a Gramaton bug.
	handler(os.Stdin, os.Stdout)
	return nil
}
