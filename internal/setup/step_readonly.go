package setup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/setup/templates"
	"github.com/gramaton-ai/gramaton/store"
)

// runReadOnlyAttach is the Step 0 [3] branch: attach a read-only
// store someone shared, as this machine's ONLY gramaton. Its own
// unnumbered flow (precedent: askSetupRoute, stepIdentity,
// stepVerify), deliberately outside the numbered steps -- a
// read-only consumer never stamps authorship (identity skipped),
// never runs curation (LLM skipped), and never captures sessions
// (hooks skipped).
//
// The attach mechanics (source validation, copy, freeze-the-copy,
// minimal per-store config) live in store.ResolveAttachSource /
// store.Attach, shared with `gramaton store attach` -- the
// non-wizard path for users who ALSO run their own writable store
// and want a shared one alongside it. This route only adds the
// interactive framing, the freeze-the-original offer, MCP
// registration, and the read-only guidance install.
//
// The GLOBAL config is deliberately not written: a later `gramaton
// init` can still run the full wizard for the user's own store
// (the already-initialized guard keys on the global config.yaml),
// and every config loader tolerates a missing global file. Nothing
// on this route prompts for an LLM, ever.
//
// Failure handling mirrors runImport: user-input problems are
// soft-aborts (ErrorLine + return nil) so a mistyped path exits the
// wizard cleanly with guidance instead of a stack trace.
func (w *Wizard) runReadOnlyAttach(ctx context.Context) error {
	w.writer.Section("Attaching a shared read-only store")
	w.writer.Paragraph(
		"You're attaching a store someone shared with you. To be plain:",
		"this sets up a READ-ONLY-ONLY integration. Everything about the",
		"store is read-only, and your agent on this machine gets no",
		"write capability at all -- no personal store, no session",
		"capture, no memory saves, no curation, no edits. Nothing is",
		"ever saved on this machine.",
		"",
		"The store stays fully usable for reading: search, inspect, and",
		"graph exploration all work normally, over the entire store.",
		"",
		"If you meant to have your own writable Gramaton too, press",
		"Ctrl+C now, re-run `gramaton init`, pick a writable route, and",
		"add shared stores alongside it at any time with:",
		"gramaton store attach <path>",
	)

	srcData, ok, err := w.promptSharedStorePath()
	if err != nil || !ok {
		return err
	}

	manifest, ok, err := w.inspectSharedManifest(srcData)
	if err != nil || !ok {
		return err
	}

	name, ok, err := w.promptStoreName(srcData)
	if err != nil || !ok {
		return err
	}

	result, ok := w.attachStoreCopy(srcData, name, manifest)
	if !ok {
		return nil
	}

	// MCP entries + guidance for the detected harnesses. Both are
	// best-effort: per-client failures warn and continue, same as
	// Steps 3-4 on the writable routes.
	clients := w.mcpBackend.Detect()
	mcpClients := w.registerStoreMCP(ctx, clients, name)
	guidanceClients := w.installReadOnlyGuidance(clients, name)

	w.readOnlySummary(result, mcpClients, guidanceClients)
	return nil
}

// promptSharedStorePath asks for the received directory and resolves
// it to a validated data dir via store.ResolveAttachSource. ok=false
// means a reported soft-abort; err is only user abort (Ctrl+C/EOF).
func (w *Wizard) promptSharedStorePath() (dataDir string, ok bool, err error) {
	w.writer.Blank()
	w.writer.Paragraph(
		"Where is the store you received? Enter the directory you were",
		"sent -- either the store directory (it contains data/) or the",
		"data directory itself.",
	)
	w.writer.Blank()
	w.writer.Prompt(">")

	raw, err := w.prompter.Text("")
	if err != nil {
		return "", false, err
	}
	if raw == "" {
		w.writer.Warn("No path entered; aborting. Re-run `gramaton init` to try again.")
		return "", false, nil
	}
	abs, err := expandUserPath(raw)
	if err != nil {
		w.writer.ErrorLine(fmt.Sprintf("Can't resolve path %q: %v", raw, err))
		return "", false, nil
	}

	dataDir, err = store.ResolveAttachSource(abs)
	if err != nil {
		w.writer.ErrorLine(err.Error())
		return "", false, nil
	}
	return dataDir, true, nil
}

// inspectSharedManifest reads the source STORE manifest, reports
// publication provenance for a frozen artifact, and -- for a
// writable or manifest-less one -- explains that this install is
// read-only regardless and offers (default no) to freeze the
// original on disk too.
func (w *Wizard) inspectSharedManifest(srcData string) (core.StoreManifest, bool, error) {
	m, err := core.ReadStoreManifest(srcData)
	if err != nil {
		// Fail loud, same rationale as the engine: a corrupted
		// manifest on a store that might be frozen must not be
		// silently treated as anything.
		w.writer.ErrorLine(fmt.Sprintf("Can't read the store's STORE manifest: %v", err))
		return core.StoreManifest{}, false, nil
	}

	if m.ReadOnly {
		w.writer.Check("Frozen store: " + publicationProvenance(m))
		return m, true, nil
	}

	w.writer.Warn("This store isn't frozen on disk -- whoever shared it didn't run `gramaton store freeze`.")
	w.writer.Paragraph(
		"That changes nothing here: this install treats the store as",
		"read-only regardless. Your local copy's manifest will be",
		"frozen, so every gramaton surface -- server, MCP tools, CLI --",
		"rejects writes to it.",
		"",
		"I can also freeze the original directory you received, so",
		"anything else that opens it sees it as read-only too. The",
		"default leaves the original exactly as it arrived.",
	)
	w.writer.Prompt("Freeze the original too? [y/N]")
	freeze, err := w.prompter.YesNo(false)
	if err != nil {
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt("Freeze the original too? [y/N]")
		freeze, err = w.prompter.YesNo(false)
		if err != nil {
			w.writer.Warn("Couldn't parse answer twice; leaving the original untouched.")
			freeze = false
		}
	}
	if freeze {
		// Owner deliberately empty: this machine configures no author
		// identity on the read-only route, and stamping a guessed one
		// onto someone else's artifact would be wrong.
		if err := core.FreezeStore(srcData, ""); err != nil {
			w.writer.Warn(fmt.Sprintf("Couldn't freeze the original (continuing; the local copy is still frozen): %v", err))
		} else {
			w.writer.Check(fmt.Sprintf("Original frozen: %s", srcData))
		}
	}
	return m, true, nil
}

// promptStoreName asks for the local store name with a default
// derived from the source directory name. One retry on an invalid or
// colliding name, then soft-abort -- the wizard's standard retry
// policy.
func (w *Wizard) promptStoreName(srcData string) (string, bool, error) {
	def := store.DefaultAttachName(srcData, w.configDir)

	w.writer.Blank()
	w.writer.Paragraph(
		"What should this store be called on this machine? The name",
		"selects it in commands (gramaton --store <name>) and in the",
		"MCP entry your AI tools use.",
	)
	w.writer.Prompt(fmt.Sprintf("Store name (default %s):", def))

	for attempt := 0; ; attempt++ {
		name, err := w.prompter.Text(def)
		if err != nil {
			return "", false, err
		}
		vErr := store.ValidateName(name)
		if vErr == nil && store.Exists(w.configDir, name) {
			vErr = fmt.Errorf("store %q already exists", name)
		}
		if vErr == nil {
			return name, true, nil
		}
		if attempt > 0 {
			w.writer.Warn("Couldn't get a usable store name twice; aborting. Re-run `gramaton init` to try again.")
			return "", false, nil
		}
		w.writer.ErrorLine(vErr.Error())
		w.writer.Prompt(fmt.Sprintf("Store name (default %s):", def))
	}
}

// attachStoreCopy runs the shared attach primitive (store.Attach)
// and narrates the result: copy destination, original untouched,
// local manifest frozen, minimal config written.
func (w *Wizard) attachStoreCopy(srcData, name string, src core.StoreManifest) (store.AttachResult, bool) {
	// Interrupt cleanup: a Ctrl+C mid-copy must not leave a
	// half-attached store. store.Attach removes the store home
	// itself on its own failure paths. Armed only when this attach
	// will be the CREATOR of the store home: Attach refuses a
	// pre-existing home, and an interrupt arriving after that
	// refusal must not delete a store the user already had.
	storeHome := store.Resolve(w.configDir, name)
	if _, statErr := os.Stat(storeHome); os.IsNotExist(statErr) {
		w.addCleanup(func() { _ = os.RemoveAll(storeHome) })
	}

	result, err := store.Attach(w.configDir, name, srcData)
	if err != nil {
		w.writer.ErrorLine(fmt.Sprintf("Attach failed (nothing was registered): %v", err))
		return store.AttachResult{}, false
	}

	w.writer.Check(fmt.Sprintf("Copied store data to %s", result.DataDir))
	w.writer.Paragraph(
		fmt.Sprintf("The original at %s", srcData),
		"is untouched -- this install works from its own copy.",
	)
	if src.ReadOnly {
		w.writer.Check("Local copy frozen: the publisher's STORE manifest (provenance preserved) travels with the copy")
	} else {
		w.writer.Check("Local copy frozen: its STORE manifest now marks it read-only for every gramaton surface")
	}
	w.writer.Check(fmt.Sprintf("Per-store config written: %s (minimal; no llm, no author)", result.ConfigPath))
	return result, true
}

// registerStoreMCP registers the per-store MCP entry with each
// detected client, mirroring stepMCP's confirm-once-then-register
// shape. Returns the names of clients that ended up registered.
func (w *Wizard) registerStoreMCP(ctx context.Context, clients []DetectedClient, name string) []string {
	entry := storeMCPEntryName(name)

	if len(clients) == 0 {
		lines := []string{
			"No supported MCP clients were found on this computer.",
			"",
			"The store still works via CLI. When you install " + harnessNamesForProse() + ",",
			"re-run `gramaton init` or register the entry manually:",
			"",
		}
		lines = append(lines, storeManualMCPHints(name)...)
		w.writer.Blank()
		w.writer.Paragraph(lines...)
		return nil
	}

	w.writer.Blank()
	w.writer.Paragraph("Looking for AI tools on your computer...", "Found:")
	w.writer.Blank()
	for _, c := range clients {
		w.writer.Raw(fmt.Sprintf("    [x] %-12s  (%s)", c.Name, c.Binary))
	}
	w.writer.Blank()
	w.writer.Paragraph(
		fmt.Sprintf("I'll add this store to these tools as MCP entry %q,", entry),
		fmt.Sprintf("running `gramaton --store %s mcp`. Because the store is", name),
		"frozen, only the read tools (search, inspect, explore, ...) are",
		"registered -- your agent never even sees a write tool for it.",
	)
	w.writer.Prompt("Continue? [Y/n]")
	confirm, err := w.prompter.YesNo(true)
	if err != nil {
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
		lines := []string{"Register manually with any of:"}
		lines = append(lines, storeManualMCPHints(name)...)
		w.writer.Paragraph(lines...)
		return nil
	}

	// Delegate the per-client registration to the shared primitive so
	// this route and the non-interactive CLI store commands run one
	// loop; the wizard only owns the interactive framing (prompt +
	// per-client narration) around it.
	rep := syncClients(ctx, w.mcpBackend, clients, name, EntryPresent)
	for _, c := range rep.Clients {
		switch c.Action {
		case SyncFailed:
			w.writer.Warn(fmt.Sprintf("%s: %v", c.Client, c.Err))
		case SyncAlreadyRegistered:
			w.writer.Check(fmt.Sprintf("%s already registered with %s (no change)", entry, c.Client))
		case SyncRegistered:
			w.writer.Check(fmt.Sprintf("Added %s to %s", entry, c.Client))
		}
	}
	return rep.Registered()
}

// installReadOnlyGuidance offers the read-only agent-guidance
// variant per detected client, reusing Step 4's install machinery
// (paths, layouts, fenced merges) with the readonly.md body. Returns
// the names of clients whose guidance was installed.
func (w *Wizard) installReadOnlyGuidance(clients []DetectedClient, storeName string) []string {
	if len(clients) == 0 {
		return nil
	}

	w.writer.Blank()
	w.writer.Paragraph(
		"Last step: agent guidance. This installs a short instruction",
		"block telling your agent the store is read-only, what works",
		"(search, inspect, explore, ...), and that no write tools exist",
		"for it. It replaces any Gramaton guidance block already",
		"installed for these clients.",
		"",
		"You'll be asked once per detected client.",
	)

	var installed []string
	for _, c := range clients {
		path, layout, err := instructionsPathForClient(c.Name)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: %v", c.Name, err))
			continue
		}

		w.writer.Blank()
		w.writer.Raw(fmt.Sprintf("    %s: %s", c.Name, path))
		w.writer.Raw("    [Y] Yes, install")
		w.writer.Raw("    [n] Skip for this client")
		w.writer.Prompt(">")

		confirm, err := w.prompter.YesNo(true)
		if err != nil {
			w.writer.ErrorLine(err.Error())
			w.writer.Prompt(">")
			confirm, err = w.prompter.YesNo(true)
			if err != nil {
				w.writer.Warn(fmt.Sprintf("%s: couldn't parse answer twice; skipping.", c.Name))
				continue
			}
		}
		if !confirm {
			w.writer.Warn(fmt.Sprintf("%s: skipped.", c.Name))
			continue
		}

		action, err := installInstructions(path, readOnlyBodyForClient(c.Name, storeName), layout)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: write failed: %v", c.Name, err))
			continue
		}
		w.writer.Check(fmt.Sprintf("%s: %s %s", c.Name, action, path))
		installed = append(installed, c.Name)
	}
	return installed
}

// readOnlyBodyForClient is installBodyForClient's read-only sibling:
// the readonly.md variant rendered with the harness's interpolation
// variables plus the attached store's name (for the CLI-fallback
// line), and the harness's InstructionsHeader when one is defined
// (Cursor's SKILL.md frontmatter).
func readOnlyBodyForClient(clientName, storeName string) string {
	h := harnessByName(clientName)
	if h == nil {
		return templates.RenderReadOnly(templates.Vars{
			ClientName:    clientName,
			ReconnectHint: "re-establish the MCP connection",
			StoreName:     storeName,
		})
	}
	body := templates.RenderReadOnly(templates.Vars{
		ClientName:    h.Name,
		ReconnectHint: h.ReconnectHint,
		StoreName:     storeName,
	})
	if h.InstructionsHeader != nil {
		body = h.InstructionsHeader() + body
	}
	return body
}

// readOnlySummary is the route's verification block + next steps,
// the read-only counterpart of stepVerify + nextSteps. The manifest
// state is re-read from disk so the summary shows what will actually
// be enforced.
func (w *Wizard) readOnlySummary(result store.AttachResult, mcpClients, guidanceClients []string) {
	name := result.Name

	w.writer.Section("Verification")

	w.writer.Check(fmt.Sprintf("Store attached: %s [read-only]", name))
	w.writer.Check(fmt.Sprintf("Data: %s", result.DataDir))

	if m, err := core.ReadStoreManifest(result.DataDir); err != nil {
		w.writer.Warn(fmt.Sprintf("STORE manifest unreadable on the copy: %v", err))
	} else if !m.ReadOnly {
		w.writer.Warn("STORE manifest on the copy is not frozen -- writes would be accepted. Fix: gramaton --store " + name + " store freeze")
	} else {
		w.writer.Check("Read-only enforced: " + publicationProvenance(m))
	}

	if len(mcpClients) > 0 {
		w.writer.Check(fmt.Sprintf("MCP: %s entry present in %s (gramaton --store %s mcp)",
			storeMCPEntryName(name), strings.Join(mcpClients, ", "), name))
	} else {
		w.writer.Warn("MCP: no client entries registered (see the manual hints above)")
	}
	if len(guidanceClients) > 0 {
		w.writer.Check(fmt.Sprintf("Guidance: read-only variant installed for %s", strings.Join(guidanceClients, ", ")))
	} else {
		w.writer.Warn("Guidance: not installed (agents won't be told the store is read-only)")
	}

	w.writer.Section("Read-only store attached.")
	w.writer.Paragraph(
		"Next steps:",
		"",
		"  1. Restart your AI client(s) so they pick up the new MCP",
		"     entry.",
		"",
		fmt.Sprintf("  2. Try it -- ask your agent to search the %s store.", name),
		"     Only read tools are available; nothing can be saved to it.",
		"",
		"  3. Browse from the CLI:",
		fmt.Sprintf("     gramaton --store %s search \"<query>\" --top 5", name),
	)
	w.writer.Blank()
}

// publicationProvenance renders a STORE manifest's owner/published_at
// as a human-readable clause ("published by Ada <ada@example.com> at
// 2026-07-02T15:04:05Z"). Both fields are optional in the manifest;
// absent ones are simply omitted.
func publicationProvenance(m core.StoreManifest) string {
	switch {
	case m.Owner != "" && !m.PublishedAt.IsZero():
		return fmt.Sprintf("published by %s at %s", m.Owner, m.PublishedAt.UTC().Format(time.RFC3339))
	case m.Owner != "":
		return fmt.Sprintf("published by %s", m.Owner)
	case !m.PublishedAt.IsZero():
		return fmt.Sprintf("published at %s", m.PublishedAt.UTC().Format(time.RFC3339))
	default:
		return "publisher unknown (no provenance recorded)"
	}
}

// storeManualMCPHints returns per-harness manual-registration hints
// for an attached store's MCP entry, the per-store analogue of
// harnessManualHints. Spelled out here (not derived from the
// registry's static ManualMCPHint strings) because the entry name
// and argv interpolate the store name.
func storeManualMCPHints(name string) []string {
	entry := storeMCPEntryName(name)
	return []string{
		fmt.Sprintf("  claude mcp add --scope user %s gramaton -- --store %s mcp", entry, name),
		fmt.Sprintf("  codex mcp add %s -- gramaton --store %s mcp", entry, name),
		fmt.Sprintf(`  (Cursor has no CLI -- add to ~/.cursor/mcp.json under mcpServers: %q: {"type": "stdio", "command": "gramaton", "args": ["--store", %q, "mcp"]})`, entry, name),
		"  (kiro-cli's equivalent -- check `kiro mcp --help`)",
	}
}
