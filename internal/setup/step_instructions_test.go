package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/setup/templates"
)

// fencedBlockWith wraps `inner` in the gramaton fence markers the way
// installInstructions writes them, for use in expected-output
// assertions.
func fencedBlockWith(inner string) string {
	return instructionsFenceBegin() + "\n" + strings.TrimSpace(inner) + "\n" + instructionsFenceEnd + "\n"
}

func TestInstallInstructionsFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	action, err := installInstructions(path, "## Gramaton test instructions\nbody.", fencedBlockInSharedFile)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if action != "created" {
		t.Errorf("action = %q, want created", action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := fencedBlockWith("## Gramaton test instructions\nbody.")
	if string(got) != want {
		t.Errorf("fresh file content:\n  got:  %q\n  want: %q", string(got), want)
	}
}

func TestInstallInstructionsAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// Existing content (user's pre-written sections).
	existing := "## My custom section\nmy notes\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := installInstructions(path, "## Gramaton\nhi", fencedBlockInSharedFile)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if action != "appended" {
		t.Errorf("action = %q, want appended", action)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// User content must still be there.
	if !strings.Contains(string(got), "My custom section") {
		t.Errorf("user content lost:\n%s", string(got))
	}
	// Gramaton block appended.
	if !strings.Contains(string(got), "## Gramaton\nhi") {
		t.Errorf("gramaton block missing:\n%s", string(got))
	}
	if !strings.Contains(string(got), instructionsFenceBegin()) || !strings.Contains(string(got), instructionsFenceEnd) {
		t.Errorf("fence markers missing:\n%s", string(got))
	}
}

func TestInstallInstructionsReplacesFencedBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// Existing fenced block with OLD content.
	existing := "## User prelude\nprelude\n\n" + fencedBlockWith("## Old gramaton\nold body") + "\n## User epilogue\nepilogue\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := installInstructions(path, "## New gramaton\nnew body", fencedBlockInSharedFile)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if action != "updated" {
		t.Errorf("action = %q, want updated", action)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotStr := string(got)
	// User prelude and epilogue preserved.
	if !strings.Contains(gotStr, "User prelude") || !strings.Contains(gotStr, "User epilogue") {
		t.Errorf("user content lost:\n%s", gotStr)
	}
	// Old block gone, new block present.
	if strings.Contains(gotStr, "Old gramaton") {
		t.Errorf("old block still present:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "New gramaton") {
		t.Errorf("new block missing:\n%s", gotStr)
	}
}

func TestInstallInstructionsUnchangedWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	template := "## Gramaton\nsame body"

	// First install.
	if _, err := installInstructions(path, template, fencedBlockInSharedFile); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, _ := os.ReadFile(path)

	// Second install with identical template.
	action, err := installInstructions(path, template, fencedBlockInSharedFile)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if action != "unchanged" {
		t.Errorf("action = %q, want unchanged", action)
	}

	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("content shifted on idempotent re-run:\n  before: %q\n  after:  %q", before, after)
	}
}

func TestInstallInstructionsUnbalancedFenceErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// Only BEGIN marker present.
	broken := "## Stuff\n" + instructionsFenceBegin() + "\nhalf-written block\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := installInstructions(path, "## Gramaton", fencedBlockInSharedFile)
	if err == nil {
		t.Error("expected error on unbalanced fence markers")
	}
}

func TestInstructionsPathForClient(t *testing.T) {
	cases := []struct {
		name, client string
		wantSuffix   string
		wantLayout   instructionsLayout
		wantErr      bool
	}{
		{"claude code", "Claude Code", filepath.Join(".claude", "CLAUDE.md"), fencedBlockInSharedFile, false},
		{"kiro-cli", "kiro-cli", filepath.Join(".kiro", "steering", "gramaton.md"), wholeFileOwned, false},
		{"unknown", "SomeUnknownClient", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, layout, err := instructionsPathForClient(tc.client)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("path = %q, want suffix %q", got, tc.wantSuffix)
			}
			if layout != tc.wantLayout {
				t.Errorf("layout = %v, want %v", layout, tc.wantLayout)
			}
		})
	}
}

// TestInstructionsPathForCodex covers the ConfigRootEnv resolution
// branch: ~/.codex/AGENTS.md by default, $CODEX_HOME/AGENTS.md when
// the vendor relocation variable is set.
func TestInstructionsPathForCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")

	got, layout, err := instructionsPathForClient("Codex")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".codex", "AGENTS.md"); got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
	if layout != fencedBlockInSharedFile {
		t.Errorf("layout = %v, want fencedBlockInSharedFile", layout)
	}

	relocated := filepath.Join(home, "elsewhere")
	t.Setenv("CODEX_HOME", relocated)
	got, _, err = instructionsPathForClient("Codex")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(relocated, "AGENTS.md"); got != want {
		t.Errorf("relocated path = %q, want %q", got, want)
	}
}

func TestInstallInstructionsWholeFileCreatedWithoutFenceMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gramaton.md")

	body := "## Gramaton\nwhole file"
	action, err := installInstructions(path, body, wholeFileOwned)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if action != "created" {
		t.Errorf("action = %q, want created", action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Kiro layout: no fence markers — file is ours end-to-end.
	if strings.Contains(string(got), instructionsFenceBeginPrefix) {
		t.Errorf("whole-file layout should NOT contain fence markers:\n%s", string(got))
	}
	if !strings.Contains(string(got), "Gramaton") {
		t.Errorf("whole-file content missing:\n%s", string(got))
	}
}

func TestInstallInstructionsWholeFileOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gramaton.md")
	if err := os.WriteFile(path, []byte("## Old gramaton\nold body"), 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := installInstructions(path, "## New gramaton\nnew body", wholeFileOwned)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if action != "updated" {
		t.Errorf("action = %q, want updated", action)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "Old gramaton") {
		t.Errorf("old content still present:\n%s", string(got))
	}
	if !strings.Contains(string(got), "New gramaton") {
		t.Errorf("new content missing:\n%s", string(got))
	}
}

func TestInstallInstructionsWholeFileUnchangedWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gramaton.md")

	body := "## Gramaton\nsame body"
	if _, err := installInstructions(path, body, wholeFileOwned); err != nil {
		t.Fatal(err)
	}
	action, err := installInstructions(path, body, wholeFileOwned)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Errorf("action = %q, want unchanged", action)
	}
}

// TestInstallInstructionsUpgradesUnversionedFence covers the
// migration path for files written before the BEGIN marker carried a
// v= stamp: the legacy marker must still be recognized (detection
// keys off instructionsFenceBeginPrefix) and replaced in place by
// the versioned one — NOT left behind with a second block appended.
func TestInstallInstructionsUpgradesUnversionedFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// The exact BEGIN line shipped by pre-stamp gramaton versions.
	legacyBegin := "<!-- BEGIN gramaton-managed (don't edit by hand — re-run `gramaton init --force` to update) -->"
	existing := "## User prelude\nprelude\n\n" + legacyBegin + "\nold guidance body\n" + instructionsFenceEnd + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := installInstructions(path, "new guidance body", fencedBlockInSharedFile)
	if err != nil {
		t.Fatalf("install over legacy fence: %v", err)
	}
	if action != "updated" {
		t.Errorf("action = %q, want updated (legacy fence must be recognized, not appended after)", action)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotStr := string(got)
	if !strings.Contains(gotStr, "User prelude") {
		t.Errorf("user content lost:\n%s", gotStr)
	}
	if strings.Contains(gotStr, "old guidance body") {
		t.Errorf("legacy block content still present:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "v="+templates.GuidanceVersion) {
		t.Errorf("upgraded fence missing version stamp:\n%s", gotStr)
	}
	if n := strings.Count(gotStr, instructionsFenceBeginPrefix); n != 1 {
		t.Errorf("found %d BEGIN markers, want exactly 1 (duplicate-append bug):\n%s", n, gotStr)
	}
}

func TestTemplateForClientClaudeIncludesRoutingBlock(t *testing.T) {
	got := templateForClient("Claude Code")
	if !strings.Contains(got, "## Knowledge Store (Gramaton)") {
		t.Error("Claude template missing base content")
	}
	if !strings.Contains(got, "Memory routing: Claude Code's auto-memory vs Gramaton") {
		t.Error("Claude template missing routing block heading")
	}
	if !strings.Contains(got, "Decision rule:") {
		t.Error("Claude template missing decision-rule wording")
	}
}

func TestTemplateForClientKiroOmitsRoutingBlock(t *testing.T) {
	got := templateForClient("kiro-cli")
	if !strings.Contains(got, "## Knowledge Store (Gramaton)") {
		t.Error("Kiro template missing base content")
	}
	if strings.Contains(got, "Memory routing: Claude Code") {
		t.Error("Kiro template should NOT carry the Claude-only routing block")
	}
	if strings.Contains(got, templates.AddendumMarker) {
		t.Error("Kiro template still carries the unfilled CLIENT_ADDENDUM marker")
	}
}

// TestTemplateForClientCodexIncludesMemoriesRouting pins Codex's
// addendum substitution: the native-memories routing rule must be
// present, and Claude Code's auto-memory rule must not leak in.
func TestTemplateForClientCodexIncludesMemoriesRouting(t *testing.T) {
	got := templateForClient("Codex")
	if !strings.Contains(got, "Memory routing: Codex's native memories vs Gramaton") {
		t.Error("Codex template missing the memories routing block")
	}
	if strings.Contains(got, "auto-memory") {
		t.Error("Codex template should NOT carry Claude Code's auto-memory rule")
	}
}

// TestTemplateForClientReconnectHints pins the rendered reconnect
// parenthetical per client — the {{mcp_reconnect_hint}} splice is
// the part of interpolation most likely to silently regress into
// ungrammatical output.
func TestTemplateForClientReconnectHints(t *testing.T) {
	cases := []struct{ client, want string }{
		{"Claude Code", "reconnect (for Claude Code: `/mcp` in the prompt, or"},
		{"kiro-cli", "reconnect (for kiro-cli: start a new session, or"},
		{"Codex", "reconnect (for Codex: check `codex mcp list`, then start a new session, or"},
	}
	for _, tc := range cases {
		got := templateForClient(tc.client)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: rendered guidance missing %q", tc.client, tc.want)
		}
		if strings.Contains(got, "{{") {
			t.Errorf("%s: rendered guidance has unfilled interpolation vars", tc.client)
		}
	}
}

func TestTemplateForClientUnknownReturnsBaseAlone(t *testing.T) {
	got := templateForClient("future-codex-cli")
	if !strings.Contains(got, "## Knowledge Store (Gramaton)") {
		t.Error("unknown client should still receive base content")
	}
	if strings.Contains(got, "Memory routing: Claude Code") {
		t.Error("unknown client should not get Claude addendum")
	}
	if strings.Contains(got, templates.AddendumMarker) {
		t.Error("unknown client template still carries the CLIENT_ADDENDUM marker")
	}
	if strings.Contains(got, "{{") {
		t.Error("unknown client template has unfilled interpolation vars")
	}
}

func TestClaudeAutoMemoryPresent(t *testing.T) {
	home := t.TempDir()
	// No auto-memory layout yet.
	if claudeAutoMemoryPresent(home) {
		t.Error("expected false on empty home")
	}

	// Materialize a MEMORY.md file under the expected layout.
	dir := filepath.Join(home, ".claude", "projects", "some-slug", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !claudeAutoMemoryPresent(home) {
		t.Error("expected true once MEMORY.md exists")
	}
}

func TestClaudeAutoMemoryPresentEmptyHomeArg(t *testing.T) {
	if claudeAutoMemoryPresent("") {
		t.Error("empty home arg should return false")
	}
}
