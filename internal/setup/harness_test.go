package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/setup/templates"
)

// TestHarnessRegistryInvariants pins the structural rules every
// registry entry must satisfy. These are the assumptions the wizard
// steps lean on when they iterate the registry instead of switching
// on client names.
func TestHarnessRegistryInvariants(t *testing.T) {
	if len(harnesses) == 0 {
		t.Fatal("harness registry is empty")
	}

	seenNames := map[string]bool{}
	seenEmbedDirs := map[string]bool{}
	for _, h := range harnesses {
		if h.Name == "" {
			t.Error("registry entry with empty Name")
		}
		if seenNames[h.Name] {
			t.Errorf("duplicate harness name %q", h.Name)
		}
		seenNames[h.Name] = true

		if h.DetectBinary == "" && h.DetectDir == "" {
			t.Errorf("%s: no detection method (need DetectBinary or DetectDir)", h.Name)
		}

		if h.HookEmbedDir != "" {
			if seenEmbedDirs[h.HookEmbedDir] {
				t.Errorf("duplicate HookEmbedDir %q", h.HookEmbedDir)
			}
			seenEmbedDirs[h.HookEmbedDir] = true
			if len(h.HookEvents) == 0 {
				t.Errorf("%s: HookEmbedDir set but no HookEvents", h.Name)
			}
		} else if len(h.HookEvents) > 0 {
			t.Errorf("%s: HookEvents set but no HookEmbedDir to install them under", h.Name)
		}

		// A harness that Step 3 can register must also tell users how
		// to do it by hand (the skip/no-clients copy prints the hint).
		if h.RegisterMCP != nil && h.ManualMCPHint == "" {
			t.Errorf("%s: RegisterMCP set but ManualMCPHint empty", h.Name)
		}

		// A harness Step 4 can install guidance for must fill every
		// interpolation variable: an empty ReconnectHint (or any
		// other gap) would ship literal {{...}} into the user's
		// instruction file.
		if len(h.InstructionsRelPath) > 0 {
			if h.ReconnectHint == "" {
				t.Errorf("%s: InstructionsRelPath set but ReconnectHint empty", h.Name)
			}
			if got := templateForClient(h.Name); strings.Contains(got, "{{") {
				t.Errorf("%s: rendered guidance has unfilled interpolation vars", h.Name)
			}
		}

		// ConfigRootEnv swaps the env value in for the FIRST path
		// element, so there must be at least a (root, file) pair.
		if h.ConfigRootEnv != "" && len(h.InstructionsRelPath) < 2 {
			t.Errorf("%s: ConfigRootEnv set but InstructionsRelPath %v has no root element to replace", h.Name, h.InstructionsRelPath)
		}

		// A wiring strategy needs the human-readable location for
		// the wizard's success line.
		if h.WireHooks != nil && h.HookConfigPathHint == "" {
			t.Errorf("%s: WireHooks set but HookConfigPathHint empty", h.Name)
		}
	}
}

// TestHarnessRegistryMigratedEntries pins the two migrated entries
// to their pre-registry behavior. If one of these fails, the
// behavior-identical migration contract broke.
func TestHarnessRegistryMigratedEntries(t *testing.T) {
	claude := harnessByName("Claude Code")
	if claude == nil {
		t.Fatal("Claude Code missing from registry")
	}
	if claude.DetectBinary != "claude" {
		t.Errorf("Claude Code DetectBinary = %q, want claude", claude.DetectBinary)
	}
	if claude.InstructionsLayout != fencedBlockInSharedFile {
		t.Error("Claude Code should use the fenced-block layout")
	}
	if !strings.Contains(claude.Addendum, "auto-memory") {
		t.Error("Claude Code addendum should carry the auto-memory routing rule")
	}
	if !strings.Contains(claude.ReconnectHint, "/mcp") {
		t.Error("Claude Code reconnect hint should mention /mcp")
	}
	if claude.HookEmbedDir != "claude-code" {
		t.Errorf("Claude Code HookEmbedDir = %q, want claude-code", claude.HookEmbedDir)
	}
	if claude.WireHooks == nil {
		t.Error("Claude Code should auto-wire hooks (settings.json patching)")
	}
	if claude.ProxyStyle != proxyPosixOnly {
		t.Error("Claude Code should keep .sh proxies on Windows (bundles Git Bash)")
	}

	kiro := harnessByName("kiro-cli")
	if kiro == nil {
		t.Fatal("kiro-cli missing from registry")
	}
	// Deliberately the legacy (broken) probe; see the registry
	// comment. If this fails
	// because someone fixed the binary name, delete this assertion.
	if kiro.DetectBinary != "kiro" {
		t.Errorf("kiro-cli DetectBinary = %q, want kiro (bug-for-bug migration)", kiro.DetectBinary)
	}
	if kiro.InstructionsLayout != wholeFileOwned {
		t.Error("kiro-cli should use the whole-file layout")
	}
	if kiro.ProxyStyle != proxyNativePerOS {
		t.Error("kiro-cli should use .cmd proxies on Windows")
	}
	if kiro.WireHooks != nil {
		t.Error("kiro-cli must not auto-wire hooks (per-agent schema; no default agent to patch)")
	}
}

// TestHarnessRegistryCursorEntry pins the Cursor entry to the facts
// established by the 2026-05-24 vendor-docs research plus the
// 2026-06-09 vendor-shipped-skill verifications.
func TestHarnessRegistryCursorEntry(t *testing.T) {
	cursor := harnessByName("Cursor")
	if cursor == nil {
		t.Fatal("Cursor missing from registry")
	}
	if cursor.DetectBinary != "" {
		t.Errorf("Cursor DetectBinary = %q, want empty (the IDE has no PATH binary)", cursor.DetectBinary)
	}
	if cursor.DetectDir != ".cursor" {
		t.Errorf("Cursor DetectDir = %q, want .cursor", cursor.DetectDir)
	}
	if cursor.RegisterMCP == nil {
		t.Error("Cursor should register via direct mcp.json write")
	}
	wantPath := []string{".cursor", "skills", "gramaton", "SKILL.md"}
	if len(cursor.InstructionsRelPath) != len(wantPath) {
		t.Fatalf("Cursor InstructionsRelPath = %v, want %v", cursor.InstructionsRelPath, wantPath)
	}
	for i := range wantPath {
		if cursor.InstructionsRelPath[i] != wantPath[i] {
			t.Errorf("Cursor InstructionsRelPath[%d] = %q, want %q", i, cursor.InstructionsRelPath[i], wantPath[i])
		}
	}
	if cursor.InstructionsLayout != wholeFileOwned {
		t.Error("Cursor should own SKILL.md end to end")
	}
	if cursor.InstructionsHeader == nil {
		t.Fatal("Cursor needs an InstructionsHeader (SKILL.md frontmatter)")
	}
	if cursor.ProxyStyle != proxyNativePerOS {
		t.Error("Cursor should use native per-OS proxies (no commandWindows in its hooks schema)")
	}
	if cursor.WireHooks == nil {
		t.Error("Cursor should auto-wire hooks (hooks.json patching)")
	}
	if len(cursor.HookEvents) != 3 {
		t.Errorf("Cursor should wire 3 lifecycle events (no postCompact), got %d", len(cursor.HookEvents))
	}
	for _, ev := range cursor.HookEvents {
		if !strings.HasPrefix(ev.cliEvent, "cursor-") {
			t.Errorf("Cursor cliEvent %q must be cursor-prefixed (stdin needs the adapter)", ev.cliEvent)
		}
	}
}

// TestCursorSkillHeader pins the SKILL.md preamble to Cursor's skill
// contract: frontmatter as the very first bytes, lowercase name
// within 64 chars, description within 1024 chars, and the version
// stamp after (never before) the frontmatter.
func TestCursorSkillHeader(t *testing.T) {
	h := cursorSkillHeader()
	if !strings.HasPrefix(h, "---\nname: gramaton\n") {
		t.Errorf("frontmatter must open the file:\n%s", h)
	}
	if len(cursorSkillDescription) > 1024 {
		t.Errorf("skill description is %d chars, Cursor caps at 1024", len(cursorSkillDescription))
	}
	if strings.Contains(cursorSkillDescription, `"`) {
		t.Error("skill description must not contain double quotes (it is emitted inside a quoted YAML scalar)")
	}
	if strings.Contains(h, "disable-model-invocation") {
		t.Error("disable-model-invocation must be omitted so Cursor auto-invokes the skill")
	}
	stamp := "<!-- gramaton-managed v=" + templates.GuidanceVersion
	stampIdx := strings.Index(h, stamp)
	closeIdx := strings.Index(h, "\n---\n")
	if stampIdx == -1 {
		t.Fatalf("header missing version stamp %q", stamp)
	}
	if closeIdx == -1 || stampIdx < closeIdx {
		t.Error("version stamp must come AFTER the closing frontmatter delimiter")
	}
}

// TestHarnessRegistryCodexEntry pins the Codex entry to the facts
// established by the 2026-05-24 vendor-docs research plus the
// 2026-06-09 empirical verification.
func TestHarnessRegistryCodexEntry(t *testing.T) {
	codex := harnessByName("Codex")
	if codex == nil {
		t.Fatal("Codex missing from registry")
	}
	if codex.DetectBinary != "codex" {
		t.Errorf("Codex DetectBinary = %q, want codex", codex.DetectBinary)
	}
	if codex.RegisterMCP == nil {
		t.Error("Codex should register via the codex CLI")
	}
	if codex.ConfigRootEnv != "CODEX_HOME" {
		t.Errorf("Codex ConfigRootEnv = %q, want CODEX_HOME", codex.ConfigRootEnv)
	}
	if codex.InstructionsLayout != fencedBlockInSharedFile {
		t.Error("Codex should use the fenced-block layout (AGENTS.md is shared with user content)")
	}
	if !strings.Contains(codex.Addendum, "~/.codex/memories/") {
		t.Error("Codex addendum should carry the native-memories routing rule")
	}
	if codex.ProxyStyle != proxyDualVariant {
		t.Error("Codex should materialize both proxy variants (hooks.json command/commandWindows)")
	}
	if codex.WireHooks == nil {
		t.Error("Codex should auto-wire hooks (hooks.json patching)")
	}
	if codex.HookEmbedDir != "codex" {
		t.Errorf("Codex HookEmbedDir = %q, want codex", codex.HookEmbedDir)
	}
	if len(codex.HookEvents) != 4 {
		t.Errorf("Codex should wire 4 lifecycle events, got %d", len(codex.HookEvents))
	}
}

// TestHarnessLookups covers the two lookup helpers' hit and miss
// paths.
func TestHarnessLookups(t *testing.T) {
	if h := harnessByName("Claude Code"); h == nil || h.Name != "Claude Code" {
		t.Error("harnessByName(Claude Code) failed")
	}
	if h := harnessByName("definitely-not-a-harness"); h != nil {
		t.Errorf("harnessByName(unknown) = %v, want nil", h)
	}
	if h := harnessByEmbedDir("kiro"); h == nil || h.Name != "kiro-cli" {
		t.Error("harnessByEmbedDir(kiro) failed")
	}
	if h := harnessByEmbedDir(""); h != nil {
		t.Error("harnessByEmbedDir(empty) must return nil, not a harness with no embed dir")
	}
	if h := harnessByEmbedDir("nope"); h != nil {
		t.Errorf("harnessByEmbedDir(unknown) = %v, want nil", h)
	}
}

// TestDetectHarnessDir exercises the directory-presence detection
// branch (used by harnesses with no PATH binary, e.g. GUI IDEs).
// The branch has no registry consumer until the Cursor entry lands;
// this test keeps it honest in the meantime.
func TestDetectHarnessDir(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows;
	// set both so the test passes on every CI leg.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	h := Harness{Name: "Dir Harness", DetectDir: ".dirharness"}

	if _, ok := detectHarness(h); ok {
		t.Fatal("detected harness before its config dir exists")
	}

	if err := os.MkdirAll(filepath.Join(home, ".dirharness"), 0o700); err != nil {
		t.Fatal(err)
	}
	c, ok := detectHarness(h)
	if !ok {
		t.Fatal("failed to detect harness via config dir")
	}
	if c.Name != "Dir Harness" {
		t.Errorf("detected name = %q", c.Name)
	}
	if c.Binary != "" {
		t.Errorf("dir-detected harness must have empty Binary, got %q", c.Binary)
	}

	// A plain file at the path must NOT count as a config dir.
	filePath := filepath.Join(home, ".dirharness-file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectHarness(Harness{Name: "File", DetectDir: ".dirharness-file"}); ok {
		t.Error("a regular file should not satisfy directory detection")
	}
}

// TestDetectHarnessBinaryPrecedence verifies DetectBinary wins over
// DetectDir when both are set: a missing binary means not-detected
// even if the directory exists.
func TestDetectHarnessBinaryPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".bothharness"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := Harness{
		Name:         "Both",
		DetectBinary: "definitely-not-on-path-xyzzy",
		DetectDir:    ".bothharness",
	}
	if _, ok := detectHarness(h); ok {
		t.Error("binary probe is authoritative when set; dir presence must not override a missing binary")
	}
}
