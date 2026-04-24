package setup

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// instructionsTemplate is the canonical Gramaton agent-usage guide
// installed into each MCP client's user-scope instructions file
// (Claude Code: ~/.claude/CLAUDE.md; Kiro TBD). Content lives in a
// sibling .md file so editors syntax-highlight it and diffs read as
// prose rather than escaped Go strings.
//
//go:embed agent_instructions.md
var instructionsTemplate string

// Fence markers bound the Gramaton-managed block inside the user's
// CLAUDE.md. Content outside the fence is preserved verbatim across
// runs; content inside is replaced on every `gramaton init` (with or
// without --force) so fixes ship cleanly.
//
// Users who want to customize agent behavior should add their own
// sections outside the fence. The wizard never touches content
// outside the fenced region.
const (
	instructionsFenceBegin = "<!-- BEGIN gramaton-managed (don't edit by hand — re-run `gramaton init --force` to update) -->"
	instructionsFenceEnd   = "<!-- END gramaton-managed -->"
)

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
			"to install instructions into. If you install Claude Code or",
			"kiro-cli later, re-run `gramaton init --force`.",
		)
		return nil
	}

	w.writer.Paragraph(
		"Gramaton gives your agent the right tools, but your agent",
		"won't use them unless its instruction file tells it when.",
		"",
		"This step appends a managed `<!-- gramaton-managed -->` block",
		"to each client's user-scope instruction file (Claude Code:",
		"~/.claude/CLAUDE.md). Content outside the block is preserved",
		"on every re-run.",
		"",
		"Install Gramaton agent-usage instructions?",
	)
	w.writer.Blank()
	w.writer.Raw("    [Y] Yes, install")
	w.writer.Raw("    [n] Not now (you can add instructions by hand later)")
	w.writer.Blank()
	w.writer.Prompt(">")

	confirm, err := w.prompter.YesNo(true)
	if err != nil {
		w.writer.ErrorLine(err.Error())
		w.writer.Prompt(">")
		confirm, err = w.prompter.YesNo(true)
		if err != nil {
			w.writer.Warn("Couldn't parse answer twice; skipping instructions install.")
			return nil
		}
	}
	if !confirm {
		w.writer.Warn("Skipping agent-usage instructions.")
		w.writer.Paragraph(
			"",
			"Your agent won't auto-call Gramaton until its instruction",
			"file has the usage guidance. Re-run `gramaton init --force`",
			"when you're ready, or paste the guidance manually from",
			"docs/agent-instructions.md (shipped with the binary).",
		)
		return nil
	}

	installed := 0
	for _, c := range clients {
		path, layout, err := instructionsPathForClient(c.Name)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: %v", c.Name, err))
			continue
		}
		action, err := installInstructions(path, instructionsTemplate, layout)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: write failed: %v", c.Name, err))
			continue
		}
		w.writer.Check(fmt.Sprintf("%s: %s %s", c.Name, action, path))
		installed++
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
// user-scope instruction file. Returns an error for unknown clients
// rather than guessing — the wizard prefers to surface gaps so we
// can add support deliberately.
func instructionsPathForClient(clientName string) (string, instructionsLayout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("resolve home dir: %w", err)
	}
	switch clientName {
	case "Claude Code":
		// Claude Code loads ~/.claude/CLAUDE.md as one merged
		// system-prompt piece; users routinely add their own
		// content alongside. Fence the managed region.
		return filepath.Join(home, ".claude", "CLAUDE.md"), fencedBlockInSharedFile, nil
	case "kiro-cli":
		// Kiro loads every .md in ~/.kiro/steering/ on session
		// start, so single-purpose files are the idiomatic shape.
		// Own gramaton.md entirely; users add siblings for their
		// own topics.
		// Verified: https://kiro.dev/docs/cli/steering/
		return filepath.Join(home, ".kiro", "steering", "gramaton.md"), wholeFileOwned, nil
	}
	return "", 0, fmt.Errorf("no instruction-file path defined for client %q", clientName)
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
	fenced := instructionsFenceBegin + "\n" + strings.TrimSpace(template) + "\n" + instructionsFenceEnd + "\n"

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
func replaceOrAppendFence(existing []byte, fenced string) ([]byte, string, error) {
	beginIdx := bytes.Index(existing, []byte(instructionsFenceBegin))
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
