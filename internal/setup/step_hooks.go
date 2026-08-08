package setup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gramaton-ai/gramaton/server"
)

// stepHooks is Step 5: install automatic-capture hooks for detected
// MCP clients, and offer permission pre-approval where the harness
// supports it. This wires Gramaton's session lifecycle (start / stop
// / pre-compact / post-compact / user-prompt-submit) to the client
// so session prepare and save run automatically without the user
// having to remember, and (separately consented) removes the
// per-call permission prompts that would otherwise interrupt each
// automatic save.
//
// Flow:
//  1. Depend on Step 3's detection results: if no clients were
//     detected, skip entirely with a short message.
//  2. Ask a Yes/No "install auto-capture hooks for the detected
//     clients? [Y/n]".
//  3. On confirm, for each detected client: materialize the proxy
//     scripts to <configDir>/hooks/<client>/, then wire them via
//     the harness registry's WireHooks strategy (settings.json for
//     Claude Code, hooks.json for Codex and Cursor — each patcher
//     preserves all non-gramaton entries). Harnesses without a
//     strategy (kiro-cli: per-agent schema, no default agent to
//     patch) get the script paths printed with manual wiring
//     guidance instead.
//  4. Offer permission pre-approval (offerPermissions) under its own
//     Yes/No, on both the confirm and decline paths — the two
//     consents are independent.
//  5. Warn at the end that users need to restart their clients for
//     the new hooks/permissions to take effect.
//
// Dependencies: this step re-runs Detect on the MCP backend rather
// than threading detected clients through from Step 3. Both calls
// go to the same backend; the cost is a couple of exec.LookPath
// calls, negligible. Decouples the steps so Step 5 can run
// independently if we later add a "re-install hooks only" code path.
func (w *Wizard) stepHooks(ctx context.Context) error {
	w.writer.StepHeader(5, totalSteps, "Automatic knowledge capture (recommended)")

	clients := w.mcpBackend.Detect()
	if len(clients) == 0 {
		w.writer.Paragraph(
			"No MCP clients detected, so there's nothing to install hooks into.",
			fmt.Sprintf("If you install %s later, re-run `gramaton init`.", harnessNamesForProse()),
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
		// Permissions are consent-gated independently: declining
		// hooks shouldn't cost the user the prompt-free tool calls.
		if w.offerPermissions(ctx, clients) {
			w.writer.Blank()
			w.writer.Warn("Restart your AI client(s) so the permissions take effect.")
		}
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

		if h.WireHooks != nil {
			// The backend dispatches to the registry's per-harness
			// wiring strategy (settings.json for Claude Code,
			// hooks.json for Codex); going through the backend keeps
			// the test seam.
			unchanged, err := w.hookBackend.RegisterHooks(ctx, h.HookEmbedDir, paths)
			if err != nil {
				w.writer.Warn(fmt.Sprintf("%s: hook config update failed: %v", c.Name, err))
				continue
			}
			if unchanged {
				w.writer.Check(fmt.Sprintf("%s: hook config already up to date", c.Name))
			} else {
				w.writer.Check(fmt.Sprintf("%s: updated %s", c.Name, h.HookConfigPathHint()))
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

	permsChanged := w.offerPermissions(ctx, clients)

	if installed > 0 || permsChanged {
		w.writer.Blank()
		w.writer.Warn("Restart your AI client(s) so the hooks and permissions take effect.")
	}
	return nil
}

// offerPermissions is the consent-gated permission pre-approval half
// of Step 5, offered to every detected client whose harness has a
// WirePermissions strategy (Claude Code today). Kept separate from
// the hooks consent because the two grants differ in kind: hooks run
// gramaton code on lifecycle events, permissions let the agent call
// gramaton's MCP tools without per-call prompts. A user may
// reasonably want either without the other. Reports whether any
// permission config was actually rewritten, so the caller can widen
// the restart warning. A bare Enter counts as yes, matching the
// hooks consent above it.
func (w *Wizard) offerPermissions(ctx context.Context, clients []DetectedClient) bool {
	// Dedup by harness: two detected clients resolving to the same
	// registry entry (not possible today, cheap to guard) must not
	// write or report twice.
	seen := map[string]bool{}
	var eligible []*Harness
	for _, c := range clients {
		if h := harnessByName(c.Name); h != nil && h.WirePermissions != nil && !seen[h.Name] {
			seen[h.Name] = true
			eligible = append(eligible, h)
		}
	}
	if len(eligible) == 0 {
		return false
	}

	names := make([]string, 0, len(eligible))
	for _, h := range eligible {
		names = append(names, h.Name)
	}
	toolNames := server.MCPAgentSurfaceToolNames()
	w.writer.Blank()
	w.writer.Paragraph(
		fmt.Sprintf("%s asks before every tool call that isn't", strings.Join(names, " / ")),
		"pre-approved, so automatic saves would stop on a permission",
		"prompt each time. Gramaton can pre-approve its full tool",
		fmt.Sprintf("surface -- %d tools, including ones that modify and delete", len(toolNames)),
		"store data (records, collections, branches). Only entries",
		"starting mcp__gramaton__ are touched: your other permission",
		"entries are preserved, deny/ask rules always win, renamed",
		"tools are reconciled on re-run, and `gramaton uninstall`",
		"removes the grants.",
		"",
		"Pre-approve Gramaton's tools?",
	)
	w.writer.Blank()
	w.writer.Raw("    [Y] Yes, pre-approve")
	w.writer.Raw("    [n] Not now")
	w.writer.Blank()
	w.writer.Prompt(">")

	confirm, err := w.prompter.YesNo(true)
	if err != nil {
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt(">")
		confirm, err = w.prompter.YesNo(true)
		if err != nil {
			w.writer.Warn("Couldn't parse answer twice; skipping permission pre-approval.")
			return false
		}
	}
	if !confirm {
		w.writer.Warn("Skipping permission pre-approval.")
		return false
	}

	changed := false
	for _, h := range eligible {
		unchanged, err := w.hookBackend.RegisterPermissions(ctx, h.HookEmbedDir, toolNames)
		if errors.Is(err, errPermissionsBlocked) {
			w.writer.Warn(fmt.Sprintf("%s: your permissions.deny/ask rules block Gramaton's tools; leaving permissions untouched.", h.Name))
			continue
		}
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: permission update failed: %v", h.Name, err))
			continue
		}
		if unchanged {
			w.writer.Check(fmt.Sprintf("%s: permissions already up to date", h.Name))
			continue
		}
		changed = true
		where := "its permission config"
		if h.HookConfigPathHint != nil {
			where = h.HookConfigPathHint()
		}
		w.writer.Check(fmt.Sprintf("%s: pre-approved %d tools in %s", h.Name, len(toolNames), where))
	}
	return changed
}
