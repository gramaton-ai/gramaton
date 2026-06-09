package setup

import (
	"context"
	"fmt"
	"path/filepath"
)

// stepHooks is Step 4: install automatic-capture hooks for detected
// MCP clients. This wires Gramaton's session lifecycle (start / stop
// / pre-compact / post-compact) to the client so session-prepare and
// session-commit run automatically without the user having to
// remember.
//
// Flow:
//  1. Depend on Step 3's detection results: if no clients were
//     detected, skip entirely with a short message.
//  2. Ask one Yes/No "install auto-capture hooks for the detected
//     clients? [Y/n]".
//  3. On confirm, for each detected client:
//     - Materialize embedded scripts to
//       <configDir>/hooks/<client>/.
//     - For Claude Code: auto-patch ~/.claude/settings.json to route
//       the event hooks at our scripts. Preserves all other
//       settings and user hooks.
//     - For kiro-cli: scripts are materialized but settings auto-
//       patching is skipped because kiro-cli's hook-config schema
//       isn't documented in our corpus. Print the script paths and
//       tell the user to wire them in via kiro-cli's config.
//  4. Warn at the end that users need to restart their clients for
//     the new hooks to take effect.
//
// Dependencies: this step re-runs Detect on the MCP backend rather
// than threading detected clients through from Step 3. Both calls
// go to the same backend; the cost is a couple of exec.LookPath
// calls, negligible. Decouples the steps so Step 4 can run
// independently if we later add a "re-install hooks only" code path.
func (w *Wizard) stepHooks(ctx context.Context) error {
	w.writer.StepHeader(5, totalSteps, "Automatic knowledge capture (recommended)")

	clients := w.mcpBackend.Detect()
	if len(clients) == 0 {
		w.writer.Paragraph(
			"No MCP clients detected, so there's nothing to install hooks into.",
			"If you install Claude Code or kiro-cli later, re-run `gramaton init`.",
		)
		return nil
	}

	w.writer.Paragraph(
		"Gramaton can automatically save knowledge from your AI",
		"conversations so it builds up a memory across sessions",
		"without you having to do anything.",
		"",
		"Install auto-capture hooks for the detected clients?",
	)
	w.writer.Blank()
	w.writer.Raw("    [Y] Yes, install")
	w.writer.Raw("    [n] Not now")
	w.writer.Blank()
	w.writer.Prompt(">")

	confirm, err := w.prompter.YesNo(true)
	if err != nil {
		// One retry, then default to safe (no install). Same pattern
		// as Step 3's MCP confirm -- destructive-ish operations
		// prefer the conservative default.
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt(">")
		confirm, err = w.prompter.YesNo(true)
		if err != nil {
			w.writer.Warn("Couldn't parse answer twice; skipping hook installation.")
			return nil
		}
	}
	if !confirm {
		w.writer.Warn("Skipping hook installation.")
		w.writer.Paragraph(
			"",
			"You can install hooks later by re-running `gramaton init`, or",
			"manually by inspecting ~/.gramaton/hooks/ and wiring the",
			"scripts into your client's hook config.",
		)
		return nil
	}

	installed := 0
	for _, c := range clients {
		// The registry maps the user-facing name to the embed-tree
		// directory name. Detect uses "Claude Code"/"kiro-cli" for
		// display; the hook backend uses "claude-code"/"kiro" for
		// embed paths (matching the on-disk hooks/ layout at repo
		// root).
		h := harnessByName(c.Name)
		if h == nil || h.HookEmbedDir == "" {
			w.writer.Warn(fmt.Sprintf("No hooks bundled for %s; skipping.", c.Name))
			continue
		}

		paths, err := w.hookBackend.Materialize(h.HookEmbedDir, w.configDir)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: materialize failed: %v", c.Name, err))
			continue
		}
		w.writer.Check(fmt.Sprintf("%s: installed %d hook script(s) to %s",
			c.Name, len(paths), filepath.Join(w.configDir, "hooks", h.HookEmbedDir)))

		if h.AutoWireHooks {
			// Today only Claude Code auto-wires (settings.json
			// patching); the backend method and the success copy are
			// still Claude-specific. Generalizing the wiring strategy
			// is the Codex/Cursor harness work.
			unchanged, err := w.hookBackend.RegisterClaudeHooks(ctx, paths)
			if err != nil {
				w.writer.Warn(fmt.Sprintf("%s: settings.json update failed: %v", c.Name, err))
				continue
			}
			if unchanged {
				w.writer.Check(fmt.Sprintf("%s: hook config already up to date", c.Name))
			} else {
				w.writer.Check(fmt.Sprintf("%s: updated ~/.claude/settings.json", c.Name))
			}
			installed++
		} else {
			// Hook-config schema for this harness isn't verified (or
			// can't be patched programmatically). Print paths and
			// manual-config guidance rather than guess a schema.
			w.writer.Warn(fmt.Sprintf("%s: auto-config not yet supported. Wire these scripts into %s's hooks manually:", c.Name, c.Name))
			for _, p := range paths {
				w.writer.Raw(fmt.Sprintf("        %s", p))
			}
			installed++
		}
	}

	if installed > 0 {
		w.writer.Blank()
		w.writer.Warn("Restart your AI client(s) so the hooks take effect.")
	}
	return nil
}

