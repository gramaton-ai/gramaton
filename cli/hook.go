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
	"session-start": func(in, out *os.File) { hooks.ClaudeCodeSessionStart(in, out) },
	"stop":          func(in, out *os.File) { hooks.ClaudeCodeStop(in, out) },
	"pre-compact":   func(in, out *os.File) { hooks.ClaudeCodePreCompact(in, out) },
	"post-compact":  func(in, out *os.File) { hooks.ClaudeCodePostCompact(in, out) },
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
		// Unknown events ARE surfaced (vs. hook handler errors
		// which are always swallowed). A proxy script calling a
		// non-existent event is a programmer error and should be
		// visible to whoever's installing / debugging the hook.
		return fmt.Errorf("unknown hook event %q (valid: session-start, stop, pre-compact, post-compact, kiro-agent-spawn, kiro-user-prompt-submit, kiro-stop, cursor-session-start, cursor-stop, cursor-pre-compact)", event)
	}
	// Handlers are fail-open — they log errors to
	// ~/.gramaton/hooks.log and return without raising. We
	// always exit 0 so Claude Code / Kiro are never blocked by
	// a Gramaton bug.
	handler(os.Stdin, os.Stdout)
	return nil
}
