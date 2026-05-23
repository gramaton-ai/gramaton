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

// templateBase is the shared agent-usage guide installed into every
// MCP client's user-scope instructions file. Per-client divergence is
// expressed via addendum files (see templateForClient). Content lives
// in sibling .md files so editors syntax-highlight it and diffs read
// as prose rather than escaped Go strings.
//
//go:embed templates/base.md
var templateBase string

// templateClaudeAddendum is appended to the base template when
// installing for Claude Code. Currently carries the auto-memory
// routing rule that disambiguates Claude Code's harness-level
// MEMORY.md store from Gramaton.
//
//go:embed templates/claude_addendum.md
var templateClaudeAddendum string

// templateKiroAddendum is appended to the base template when
// installing for Kiro. Intentionally empty for now: Kiro has no
// auto-memory analogue, and the planned multi-file install split
// (per-topic steering files) is tracked as a separate follow-up.
// Reserved here so future Kiro-specific guidance has a clear home.
//
//go:embed templates/kiro_addendum.md
var templateKiroAddendum string

// clientAddendumMarker is the placeholder in base.md where each
// client's addendum slots in. Keeping it just after the introductory
// framing (and before the deeper "### Retrieval" / "### Save"
// sections) lets the routing rule register before the agent dives
// into specific tool guidance.
const clientAddendumMarker = "<!-- CLIENT_ADDENDUM -->"

// templateForClient returns the agent-usage template body to install
// for the named MCP client: the shared base, with the client's
// addendum substituted at the CLIENT_ADDENDUM marker. Unknown
// clients get the base template alone (graceful default for forward
// compatibility).
//
// Adding a new client is dropping in a new addendum file and a case
// here; install logic stays untouched.
func templateForClient(clientName string) string {
	// Normalize embedded templates to LF. The .md files live in the
	// repo and may be checked out with CRLF on Windows (git's
	// autocrlf=true is the default for Windows installs). The
	// substitution patterns below use literal "\n", and the canonical
	// integration/<client>/*.md snapshots are LF on disk, so writing
	// CRLF here would both break the marker-strip Replace and
	// produce host-dependent output.
	base := strings.ReplaceAll(templateBase, "\r\n", "\n")
	var addendum string
	switch clientName {
	case "Claude Code":
		addendum = strings.TrimSpace(strings.ReplaceAll(templateClaudeAddendum, "\r\n", "\n"))
	case "kiro-cli":
		addendum = strings.TrimSpace(strings.ReplaceAll(templateKiroAddendum, "\r\n", "\n"))
	}

	body := base
	if addendum == "" {
		// Strip the marker line and the surrounding blank lines so
		// the file doesn't carry a dangling HTML comment when no
		// addendum applies.
		body = strings.Replace(body, "\n"+clientAddendumMarker+"\n\n", "\n", 1)
	} else {
		body = strings.Replace(body, clientAddendumMarker, addendum, 1)
	}
	return strings.TrimSpace(body) + "\n"
}

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
		"This step updates each detected client's user-scope",
		"instruction file. Claude Code's ~/.claude/CLAUDE.md gets a",
		"managed `<!-- gramaton-managed -->` block (content outside",
		"it is preserved); Kiro's ~/.kiro/steering/gramaton.md is a",
		"dedicated file alongside your other steering topics.",
		"",
		"You'll be asked once per detected client so you can install",
		"for one and skip the other.",
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
