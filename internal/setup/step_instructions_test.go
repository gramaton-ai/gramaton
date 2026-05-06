package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fencedBlockWith wraps `inner` in the gramaton fence markers the way
// installInstructions writes them, for use in expected-output
// assertions.
func fencedBlockWith(inner string) string {
	return instructionsFenceBegin + "\n" + strings.TrimSpace(inner) + "\n" + instructionsFenceEnd + "\n"
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
	if !strings.Contains(string(got), instructionsFenceBegin) || !strings.Contains(string(got), instructionsFenceEnd) {
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
	broken := "## Stuff\n" + instructionsFenceBegin + "\nhalf-written block\n"
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
	if strings.Contains(string(got), instructionsFenceBegin) {
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

func TestTemplateBaseEmbedded(t *testing.T) {
	// The embed directives pull templates/*.md into the binary.
	// Verify the base template is non-empty + contains the expected
	// structural heading so we catch empty-embed bugs.
	if len(templateBase) == 0 {
		t.Fatal("templateBase is empty — //go:embed directive broken")
	}
	if !strings.Contains(templateBase, "## Knowledge Store (Gramaton)") {
		t.Error("templateBase missing canonical heading")
	}
	if !strings.Contains(templateBase, "gramaton_search") {
		t.Error("templateBase missing retrieval guidance")
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
	if strings.Contains(got, clientAddendumMarker) {
		t.Error("Kiro template still carries the unfilled CLIENT_ADDENDUM marker")
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
	if strings.Contains(got, clientAddendumMarker) {
		t.Error("unknown client template still carries the CLIENT_ADDENDUM marker")
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
