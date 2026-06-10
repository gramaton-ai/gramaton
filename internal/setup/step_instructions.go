package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gramaton-ai/gramaton/internal/setup/templates"
)

// templateForClient returns the agent-usage guidance body to install
// for the named MCP client: the shared base, with the harness's
// addendum and interpolation variables (both from the registry)
// applied by templates.Render. Unknown clients get the base alone
// with generic variables (graceful default for forward
// compatibility).
//
// Adding a new client is dropping in a new addendum file under
// templates/guidance/ and referencing it from the harness registry;
// install logic stays untouched.
func templateForClient(clientName string) string {
	h := harnessByName(clientName)
	if h == nil {
		return templates.Render("", templates.Vars{
			ClientName:    clientName,
			ReconnectHint: "re-establish the MCP connection",
		})
	}
	return templates.Render(h.Addendum, templates.Vars{
		ClientName:    h.Name,
		ReconnectHint: h.ReconnectHint,
	})
}

// Fence markers bound the Gramaton-managed block inside the user's
// CLAUDE.md. Content outside the fence is preserved verbatim across
// runs; content inside is replaced on every `gramaton init` (with or
// without --force) so fixes ship cleanly.
//
// Users who want to customize agent behavior should add their own
// sections outside the fence. The wizard never touches content
// outside the fenced region.
//
// The BEGIN line carries a guidance-version stamp (v=X.Y.Z, GH issue
// #80) that changes across releases, so fence DETECTION matches only
// the stable instructionsFenceBeginPrefix — otherwise a file written
// by one gramaton version would stop being recognized by the next
// and the block would be appended a second time.
const (
	instructionsFenceBeginPrefix = "<!-- BEGIN gramaton-managed"
	instructionsFenceEnd         = "<!-- END gramaton-managed -->"
)

// instructionsFenceBegin returns the full versioned BEGIN marker
// written on install. The stamp lets a later `gramaton init` (or the
// server's drift check, GH issue #80) see how far behind an
// installed guidance block is without hashing content.
func instructionsFenceBegin() string {
	return fmt.Sprintf("%s v=%s (don't edit by hand — re-run `gramaton init --force` to update) -->",
		instructionsFenceBeginPrefix, templates.GuidanceVersion)
}

// stepInstructions is Step 4: offer to install Gramaton's agent-usage
// guidance into each detected MCP client's instruction file.
//
// Without this, MCP tools are registered and hooks fire, but the
// agent has no built-in guidance on when to search Gramaton, when
// to capture, or how the Session flow works — so it defaults to "I
// don't have memory of that" until the user prompts it explicitly.
//
// Flow:
//  1. Depend on Step 3's detected clients.
//  2. Ask "install Gramaton agent-usage instructions? [Y/n]".
//  3. On confirm, for each detected client: merge the embedded
//     template into the client's CLAUDE.md (or equivalent),
//     fence-marker-bounded so user content is preserved.
//  4. Warn that users must restart their client.
func (w *Wizard) stepInstructions(_ context.Context) error {
	w.writer.StepHeader(4, totalSteps, "Agent usage instructions (recommended)")

	clients := w.mcpBackend.Detect()
	if len(clients) == 0 {
		w.writer.Paragraph(
			"No MCP clients detected, so there's no CLAUDE.md (or equivalent)",
			fmt.Sprintf("to install instructions into. If you install %s", harnessNamesForProse()),
			"later, re-run `gramaton init --force`.",
		)
		return nil
	}

	w.writer.Paragraph(
		"Gramaton gives your agent the right tools, but your agent",
		"won't use them unless its instruction file tells it when.",
		"",
		"This step updates each detected client's user-scope",
		"instruction file. Claude Code's ~/.claude/CLAUDE.md and",
		"Codex's ~/.codex/AGENTS.md get a managed",
		"`<!-- gramaton-managed -->` block (content outside it is",
		"preserved); Kiro's ~/.kiro/steering/gramaton.md is a",
		"dedicated file alongside your other steering topics.",
		"",
		"You'll be asked once per detected client so you can install",
		"for some and skip others.",
	)

	installed := 0
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

		template := templateForClient(c.Name)
		action, err := installInstructions(path, template, layout)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: write failed: %v", c.Name, err))
			continue
		}
		w.writer.Check(fmt.Sprintf("%s: %s %s", c.Name, action, path))
		installed++

		if c.Name == "Claude Code" {
			home, herr := os.UserHomeDir()
			if herr == nil && claudeAutoMemoryPresent(home) {
				w.writer.Blank()
				w.writer.Paragraph(
					"Detected existing Claude Code auto-memory at",
					"~/.claude/projects/*/memory/. The routing rule in the",
					"installed instructions tells the agent to prefer Gramaton",
					"for new saves; existing auto-memory entries are",
					"unchanged.",
				)
			}
		}
	}

	if installed > 0 {
		w.writer.Blank()
		w.writer.Warn("Restart your AI client(s) so the new instructions take effect.")
	}
	return nil
}

// instructionsLayout describes how the client's instruction file is
// structured — whether we share it with user-written content (Claude
// Code) or own it entirely (Kiro). The install logic branches on
// this to pick between fence-marker-bounded merges and
// atomic-overwrite.
type instructionsLayout int

const (
	// fencedBlockInSharedFile: the target file is shared with user
	// content; we fence our managed region with BEGIN/END markers
	// and leave everything outside them alone. Claude Code's
	// ~/.claude/CLAUDE.md works this way.
	fencedBlockInSharedFile instructionsLayout = iota

	// wholeFileOwned: the target file is a dedicated file in a
	// multi-file directory where each file is typically one topic;
	// we own the full content. Kiro's ~/.kiro/steering/gramaton.md
	// works this way — users add their own steering topics as
	// sibling .md files rather than mixing them into ours.
	wholeFileOwned
)

// instructionsPathForClient returns the path + layout for a client's
// user-scope instruction file, resolved from the harness registry.
// Returns an error for unknown clients (or registry entries without
// an instructions path) rather than guessing — the wizard prefers
// to surface gaps so we can add support deliberately.
//
// Harnesses with a ConfigRootEnv (Codex's CODEX_HOME) get their
// config root swapped in for the home-relative directory when the
// variable is set: [".codex", "AGENTS.md"] resolves to
// $CODEX_HOME/AGENTS.md instead of ~/.codex/AGENTS.md.
func instructionsPathForClient(clientName string) (string, instructionsLayout, error) {
	h := harnessByName(clientName)
	if h == nil || len(h.InstructionsRelPath) == 0 {
		return "", 0, fmt.Errorf("no instruction-file path defined for client %q", clientName)
	}
	if h.ConfigRootEnv != "" {
		if root := os.Getenv(h.ConfigRootEnv); root != "" {
			return filepath.Join(append([]string{root}, h.InstructionsRelPath[1:]...)...), h.InstructionsLayout, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(append([]string{home}, h.InstructionsRelPath...)...), h.InstructionsLayout, nil
}

// installInstructions writes the gramaton-managed content into the
// client's instruction file. Returns a short human-readable action
// word ("created", "updated", "unchanged", "appended") that the
// wizard prints so the user sees what actually happened.
//
// Semantics depend on layout:
//
//   fencedBlockInSharedFile (Claude Code's ~/.claude/CLAUDE.md):
//     - File doesn't exist → create with just the fenced block.
//     - Exists, no fenced block → append the fenced block after a
//       blank-line separator; existing content preserved.
//     - Exists with fenced block → replace only the fenced region.
//     - Fenced content matches → "unchanged"; no rewrite.
//
//   wholeFileOwned (Kiro's ~/.kiro/steering/gramaton.md):
//     - File doesn't exist → create with the full template.
//     - File exists, content matches → "unchanged"; no rewrite.
//     - File exists, content differs → overwrite.
//     No merging; we own the full file.
//
// Always uses tmp + rename for durability. For the shared-file path
// a sibling `.bak` file is written before replacement in case the
// fence markers were misidentified and the user needs to roll back.
func installInstructions(path, template string, layout instructionsLayout) (string, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	if layout == wholeFileOwned {
		return installWholeFile(path, existing, err == nil, strings.TrimSpace(template)+"\n")
	}
	return installFencedBlock(path, existing, err == nil, template)
}

// installWholeFile writes the template verbatim. No merging, no
// fencing — the file is ours end-to-end.
func installWholeFile(path string, existing []byte, exists bool, body string) (string, error) {
	if exists && string(existing) == body {
		return "unchanged", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := writeAtomic(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	if exists {
		return "updated", nil
	}
	return "created", nil
}

// installFencedBlock handles the Claude Code layout: user-owned
// content outside the BEGIN/END markers, gramaton-managed content
// inside.
func installFencedBlock(path string, existing []byte, exists bool, template string) (string, error) {
	fenced := instructionsFenceBegin() + "\n" + strings.TrimSpace(template) + "\n" + instructionsFenceEnd + "\n"

	if !exists {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := writeAtomic(path, []byte(fenced), 0o600); err != nil {
			return "", err
		}
		return "created", nil
	}

	newContent, action, err := replaceOrAppendFence(existing, fenced)
	if err != nil {
		return "", err
	}
	if action == "unchanged" {
		return action, nil
	}

	// Back up the existing file before rewriting.
	backupPath := fmt.Sprintf("%s.bak", path)
	_ = os.WriteFile(backupPath, existing, 0o600)
	if err := writeAtomic(path, newContent, 0o600); err != nil {
		return "", err
	}
	return action, nil
}

// replaceOrAppendFence returns the new file content and the action
// performed. `action` is one of "updated", "appended", or "unchanged".
// Errors only when the begin/end fence markers are unbalanced — which
// means the file was corrupted mid-write by some external actor; bail
// rather than guess where the managed region ends.
//
// Detection keys off instructionsFenceBeginPrefix (not the full
// versioned BEGIN line), so blocks written by any prior gramaton
// version — including pre-stamp installs — are found and replaced
// in place, stamp and all.
func replaceOrAppendFence(existing []byte, fenced string) ([]byte, string, error) {
	beginIdx := bytes.Index(existing, []byte(instructionsFenceBeginPrefix))
	endIdx := bytes.Index(existing, []byte(instructionsFenceEnd))

	if beginIdx == -1 && endIdx == -1 {
		// No fenced block; append after a blank-line separator
		// (unless the file is empty).
		var buf bytes.Buffer
		if len(existing) > 0 {
			buf.Write(existing)
			if !bytes.HasSuffix(existing, []byte("\n\n")) {
				if bytes.HasSuffix(existing, []byte("\n")) {
					buf.WriteByte('\n')
				} else {
					buf.WriteString("\n\n")
				}
			}
		}
		buf.WriteString(fenced)
		return buf.Bytes(), "appended", nil
	}

	if beginIdx == -1 || endIdx == -1 {
		return nil, "", errors.New("instruction file has unbalanced fence markers; won't touch it — fix by hand")
	}
	if endIdx < beginIdx {
		return nil, "", errors.New("instruction file has END before BEGIN; won't touch it — fix by hand")
	}

	// Replace from beginIdx through endIdx + len(endMarker). Include
	// a trailing newline for cleanliness.
	endMarkerEnd := endIdx + len(instructionsFenceEnd)
	// Swallow the newline after the end marker too so we don't grow
	// the file by one line on every idempotent re-run.
	if endMarkerEnd < len(existing) && existing[endMarkerEnd] == '\n' {
		endMarkerEnd++
	}

	var buf bytes.Buffer
	buf.Write(existing[:beginIdx])
	buf.WriteString(fenced)
	buf.Write(existing[endMarkerEnd:])
	newContent := buf.Bytes()

	if bytes.Equal(newContent, existing) {
		return existing, "unchanged", nil
	}
	return newContent, "updated", nil
}

// claudeAutoMemoryPresent reports whether the given home dir holds at
// least one Claude Code auto-memory file
// (~/.claude/projects/*/memory/MEMORY.md). Used to print a one-line
// notice during init so users aren't confused about which store
// captures land in -- separate stores, separate access patterns.
//
// Existence is sufficient signal; we don't try to count entries or
// peek at content. The notice is informational, not authoritative.
func claudeAutoMemoryPresent(home string) bool {
	if home == "" {
		return false
	}
	pattern := filepath.Join(home, ".claude", "projects", "*", "memory", "MEMORY.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return false
	}
	return len(matches) > 0
}

// writeAtomic writes data to path via tmp + rename for durability.
// Mirrors core.AtomicWriteFile's behavior without taking a
// dependency from setup/ on core/ for one helper.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
