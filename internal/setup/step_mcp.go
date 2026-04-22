package setup

import (
	"context"
	"fmt"
)

// stepMCP is Step 3: detect installed MCP clients (Claude Code,
// kiro-cli) and register Gramaton as an MCP server with each.
//
// Flow:
//  1. Call w.mcpBackend.Detect() to find installed clients.
//  2. If none, print a short "add manually later" note and continue
//     (not a failure -- users can always add clients later).
//  3. Show the detected list as a checkbox-style report and ask
//     once for confirmation.
//  4. For each client, call w.mcpBackend.Register(ctx, client).
//     Report per-client status as ✓ success, ✓ already-registered,
//     or ⚠ with the error string for diagnosis.
//  5. Warn at the end that users need to restart their clients for
//     the new MCP config to take effect.
//
// The per-client errors are surfaced but do NOT abort the wizard.
// Partial success (Claude Code registered, kiro-cli failed) is a
// valid end-state; the user can use Gramaton with whichever client
// worked.
func (w *Wizard) stepMCP(ctx context.Context) error {
	w.writer.StepHeader(3, totalSteps, "Connecting to your AI tools")

	clients := w.mcpBackend.Detect()
	if len(clients) == 0 {
		w.writer.Paragraph(
			"No supported MCP clients were found on this computer.",
			"",
			"Gramaton still works via CLI. When you install Claude Code",
			"or kiro-cli, re-run `gramaton init` or register Gramaton",
			"manually:",
			"",
			"  claude mcp add --scope user gramaton gramaton -- mcp",
		)
		return nil
	}

	// Render the detected list. Checkboxes are all [x] because we
	// only list what we actually detected; unsupported clients don't
	// appear. (If we later add a "deselect individual clients"
	// feature, this becomes [x]/[ ] with per-item toggles.)
	w.writer.Paragraph("Looking for AI tools on your computer...", "Found:")
	w.writer.Blank()
	for _, c := range clients {
		w.writer.Raw(fmt.Sprintf("    [x] %-12s  (%s)", c.Name, c.Binary))
	}
	w.writer.Blank()

	// Single confirm covers all detected clients. Rationale:
	// per-client granularity (asking about each) triples the
	// keystrokes for the common case where the user wants to
	// register with everything detected. A user who wants to
	// exclude one client can re-run the wizard or remove via the
	// client's own CLI after the fact.
	w.writer.Paragraph("I'll add Gramaton to these as their MCP backend.")
	w.writer.Prompt("Continue? [Y/n]")
	confirm, err := w.prompter.YesNo(true)
	if err != nil {
		// Invalid input: print the error, re-prompt once, then
		// default to No on a second failure. This is the safer path
		// for a destructive-ish operation (modifying the user's
		// client config).
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt("Continue? [Y/n]")
		confirm, err = w.prompter.YesNo(true)
		if err != nil {
			w.writer.Warn("Couldn't parse answer twice; skipping MCP registration.")
			return nil
		}
	}
	if !confirm {
		w.writer.Warn("Skipping MCP client registration.")
		w.writer.Paragraph(
			"Register manually with any of:",
			"  claude mcp add --scope user gramaton gramaton -- mcp",
			"  (kiro-cli's equivalent -- check `kiro mcp --help`)",
		)
		return nil
	}

	// Register each detected client. Failures are per-client; we
	// continue past them to give other clients a chance.
	registered := 0
	for _, c := range clients {
		already, regErr := w.mcpBackend.Register(ctx, c)
		if regErr != nil {
			w.writer.Warn(fmt.Sprintf("%s: %v", c.Name, regErr))
			continue
		}
		if already {
			w.writer.Check(fmt.Sprintf("Gramaton already registered with %s (no change)", c.Name))
		} else {
			w.writer.Check(fmt.Sprintf("Added Gramaton to %s", c.Name))
		}
		registered++
	}

	if registered > 0 {
		w.writer.Blank()
		w.writer.Warn("Restart your AI client(s) so the new MCP config takes effect.")
	}

	return nil
}
