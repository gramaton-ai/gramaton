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
		path, err := instructionsPathForClient(c.Name)
		if err != nil {
			w.writer.Warn(fmt.Sprintf("%s: %v", c.Name, err))
			continue
		}
		action, err := installInstructions(path, instructionsTemplate)
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

// instructionsPathForClient returns the path to a client's user-scope
// instruction file. Returns an error for unknown clients rather than
// guessing — the wizard prefers to surface gaps so we can add support
// deliberately.
func instructionsPathForClient(clientName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	switch clientName {
	case "Claude Code":
		return filepath.Join(home, ".claude", "CLAUDE.md"), nil
	case "kiro-cli":
		// kiro-cli's user-scope instruction file location is not
		// verified in Gramaton's corpus. Surface as unsupported so
		// the user sees a specific skip message rather than a
		// guess-written file in the wrong place.
		return "", errors.New("kiro-cli instruction-file location not yet supported; install manually if needed")
	}
	return "", fmt.Errorf("no instruction-file path defined for client %q", clientName)
}

// installInstructions writes the gramaton-managed block into the
// client's instruction file. Returns a short human-readable action
// word ("created", "updated", "unchanged") that the wizard prints so
// the user sees what actually happened.
//
// Semantics:
//   - File doesn't exist → create it containing just the fenced block.
//   - File exists but has no fenced block → append the fenced block
//     after a blank line, preserving all existing content.
//   - File exists and has a fenced block → replace only the fenced
//     region; preserve every byte outside.
//   - Fenced content already matches the template → return "unchanged"
//     without rewriting the file (idempotent re-run).
//
// Always uses tmp + rename for durability. The backup file (written
// before replacement) lives alongside with a `.bak-<timestamp>`
// suffix so users can recover if we mis-identify the fenced region.
func installInstructions(path, template string) (string, error) {
	fenced := instructionsFenceBegin + "\n" + strings.TrimSpace(template) + "\n" + instructionsFenceEnd + "\n"

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	if err != nil {
		// File doesn't exist; create it with just the fenced block.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := writeAtomic(path, []byte(fenced), 0o600); err != nil {
			return "", err
		}
		return "created", nil
	}

	// File exists; replace the existing fenced block or append a new one.
	newContent, action, err := replaceOrAppendFence(existing, fenced)
	if err != nil {
		return "", err
	}
	if action == "unchanged" {
		return action, nil
	}

	// Back up the existing file before rewriting.
	backupPath := fmt.Sprintf("%s.bak", path)
	if err := os.WriteFile(backupPath, existing, 0o600); err != nil {
		// Non-fatal: if backup fails the update still proceeds.
		// Users can recover from their own shell history or a
		// prior init run.
	}
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
