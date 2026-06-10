package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !claude.AutoWireHooks {
		t.Error("Claude Code should auto-wire hooks (settings.json patching)")
	}
	if claude.WindowsCmdProxy {
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
	if !kiro.WindowsCmdProxy {
		t.Error("kiro-cli should use .cmd proxies on Windows")
	}
	if kiro.AutoWireHooks {
		t.Error("kiro-cli must not auto-wire hooks (schema unverified)")
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
