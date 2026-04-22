package setup

import "context"

// stepHooks is Step 4: install the auto-capture hooks into the
// detected MCP clients' hook directories. Hooks are bash scripts
// shipped under hooks/claude-code/ and hooks/kiro/; the installer
// copies or symlinks them into place with executable bits set.
//
// NOT YET IMPLEMENTED. Stub for the first-pass wizard commit; full
// implementation plan below.
//
// Implementation plan:
//
//  1. Depend on stepMCP's detection results: if no clients were
//     detected in Step 3, skip Step 4 entirely with a short message.
//
//  2. For each detected client:
//     - Locate the client's hooks directory (e.g.,
//       ~/.claude/hooks/ for Claude Code). Create it if missing
//       with 0700 perms.
//     - For each hook script in the corresponding hooks/<client>/
//       directory in this repo, place it into the client's hooks
//       directory.
//     - Placement mechanism: symlink preferred so upgrades
//       (re-running the wizard after a gramaton upgrade)
//       propagate automatically. Fall back to copy-with-executable-
//       bit on filesystems that don't support symlinks (rare but
//       possible on some Windows FS).
//     - Idempotency: detect existing symlinks pointing at our
//       shipped hooks; update rather than duplicate. Detect
//       existing user-custom hooks (not our symlinks) and prompt
//       before overwriting.
//
//  3. The "shipped hooks location" is resolved at runtime from the
//     gramaton binary's install location. For `go install` builds,
//     the hooks/ directory isn't packaged with the binary -- we'd
//     need to either bundle them via embed.FS and materialize to
//     ~/.gramaton/hooks/ at wizard time, or tell the user to clone
//     the repo. Decision point for the follow-up pass: which
//     strategy? embed.FS is the cleanest but pulls the hook
//     scripts into the binary (trivial size impact, ~few KB).
//
//  4. Report ✓ per hook installed. Warn that users need to restart
//     the client for hooks to take effect.
func (w *Wizard) stepHooks(ctx context.Context) error {
	w.writer.StepHeader(4, totalSteps, "Automatic knowledge capture")
	w.writer.Paragraph(
		"(Hook installer is still being built. For now, see",
		"hooks/claude-code/ and hooks/kiro/ for the scripts to copy",
		"into your client's hooks directory manually.)",
	)
	return nil
}
