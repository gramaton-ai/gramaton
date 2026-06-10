package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/testutil"
)

// newVerifyFixture constructs a minimal Wizard targeting a
// configured-as-skip-everything default state, ready for stepVerify
// to be called directly. The data directory is created at 0700 so
// the writability probe succeeds. Tests tweak wiz.cfg before calling
// stepVerify to exercise different branches.
func newVerifyFixture(t *testing.T) (*Wizard, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	// Hermeticity: stepVerify's MCP survey detects harnesses via the
	// REAL PATH and home dir (it deliberately bypasses the wizard's
	// backend seam). On a dev machine with claude/codex installed
	// that means multi-second vendor-CLI shell-outs per test and
	// reads of the developer's actual ~/.cursor/mcp.json. Empty the
	// PATH and point home at the sandbox so detection finds nothing
	// unless a test plants it.
	t.Setenv("PATH", "")
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	// Skip-everything baseline.
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""

	var buf bytes.Buffer
	wiz := New(
		NewScriptedPrompter(), // no prompts in verify
		NewWriter(&buf),
		&cfg,
		filepath.Join(tmpDir, "config.yaml"),
		tmpDir,
	)
	return wiz, &buf, tmpDir
}

// TestStepVerifyMCPSurveyCursor pins the registry-driven MCP survey
// loop deterministically through its only pure-filesystem strategy:
// Cursor. The fixture's sandboxed HOME means planting ~/.cursor/
// makes the harness "detected" with no binaries or shell-outs.
func TestStepVerifyMCPSurveyCursor(t *testing.T) {
	t.Run("registered", func(t *testing.T) {
		wiz, buf, home := newVerifyFixture(t)
		cursorDir := filepath.Join(home, ".cursor")
		if err := os.MkdirAll(cursorDir, 0o700); err != nil {
			t.Fatal(err)
		}
		seed := `{"mcpServers": {"gramaton": {"type": "stdio", "command": "gramaton", "args": ["mcp"]}}}`
		if err := os.WriteFile(filepath.Join(cursorDir, "mcp.json"), []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}

		wiz.stepVerify(context.Background())
		if out := buf.String(); !strings.Contains(out, "MCP: gramaton entry present in Cursor's config") {
			t.Errorf("missing registered survey line:\n%s", out)
		}
	})

	t.Run("not registered", func(t *testing.T) {
		wiz, buf, home := newVerifyFixture(t)
		// Config dir exists (detected) but no mcp.json at all.
		if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
			t.Fatal(err)
		}

		wiz.stepVerify(context.Background())
		out := buf.String()
		if !strings.Contains(out, "MCP: gramaton not found in Cursor's config") {
			t.Errorf("missing not-registered warn:\n%s", out)
		}
		// The warn must carry the harness's manual hint.
		if !strings.Contains(out, "mcp.json") {
			t.Errorf("not-registered warn missing manual hint:\n%s", out)
		}
	})

	t.Run("not detected means no line", func(t *testing.T) {
		wiz, buf, _ := newVerifyFixture(t)
		wiz.stepVerify(context.Background())
		// The data-dir line prints a TempDir path that embeds this
		// test's name (which contains "Cursor"), so assert on the
		// survey line shapes, not the bare harness name.
		out := buf.String()
		if strings.Contains(out, "Cursor's config") || strings.Contains(out, "MCP (Cursor)") {
			t.Errorf("undetected harness should produce no survey line:\n%s", out)
		}
	})
}

// TestStepVerifySkipEverything confirms the baseline verify pass:
// config saves, perms are reported, data dir writable, embedding
// and LLM both surface "disabled"/"not configured" warnings.
func TestStepVerifySkipEverything(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)

	wiz.stepVerify(context.Background())
	out := buf.String()

	// On Windows, the perm-bit check is skipped (NTFS ACL model);
	// production emits a "skipped on Windows" line instead of the
	// POSIX "0600" line.
	permLine := "Config file permissions: 0600"
	if runtime.GOOS == "windows" {
		permLine = "Config file permissions: skipped on Windows"
	}
	mustContain := []string{
		"Verification",
		"Config saved:",
		permLine,
		"Data directory writable:",
		"Embedding: disabled",
		"LLM: not configured",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in verify output:\n%s", want, out)
		}
	}
	// Saved config must actually exist on disk.
	if _, err := os.Stat(wiz.cfgPath); err != nil {
		t.Errorf("config.yaml not written: %v", err)
	}
}

// TestStepVerifyConfigPermsAre0600 asserts the saved config.yaml has
// 0600 perms (key file paths live in it; world-readable would leak
// secrets even when keys are in separate files).
func TestStepVerifyConfigPermsAre0600(t *testing.T) {
	wiz, _, _ := newVerifyFixture(t)
	wiz.stepVerify(context.Background())

	testutil.AssertFileMode(t, wiz.cfgPath, 0o600)
}

// TestStepVerifyBERTEmbedding confirms the BERT check renders the
// expected "pure-Go BERT" line.
func TestStepVerifyBERTEmbedding(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)
	wiz.cfg.Embedding.Provider = "bert"

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Embedding: pure-Go BERT") {
		t.Errorf("missing BERT summary:\n%s", out)
	}
}

// TestStepVerifyOpenAIEmbeddingWithKeyFile exercises the api-key-
// file-present branch: the file must exist, and the verify line
// should confirm "OpenAI (cloud)".
func TestStepVerifyOpenAIEmbeddingWithKeyFile(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)

	keyPath := filepath.Join(tmpDir, "openai.key")
	if err := os.WriteFile(keyPath, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.Embedding.Provider = "openai"
	wiz.cfg.Embedding.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Embedding: OpenAI (cloud)") {
		t.Errorf("missing OpenAI summary:\n%s", out)
	}
}

// TestStepVerifyOpenAIEmbeddingWithMissingKeyFile exercises the
// warning path: provider set but api_key_file path doesn't resolve.
func TestStepVerifyOpenAIEmbeddingWithMissingKeyFile(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)
	wiz.cfg.Embedding.Provider = "openai"
	wiz.cfg.Embedding.APIKeyFile = "/tmp/definitely-not-a-real-key-file-xyz"

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "OpenAI key file missing") {
		t.Errorf("missing key-file warning:\n%s", out)
	}
}

// TestStepVerifyLLMWithGoodKeyFile exercises the LLM-with-api-key-
// file path: key file exists at 0600, provider=anthropic. Expect the
// happy-path ✓ line.
func TestStepVerifyLLMWithGoodKeyFile(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)
	keyPath := filepath.Join(tmpDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.LLM.Provider = "anthropic"
	wiz.cfg.LLM.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	// Windows skips the 0600 perm check (NTFS ACL model); production
	// emits the "skipped on Windows" variant of the ok line instead.
	wantLine := "LLM: anthropic (key file present, 0600 perms)"
	if runtime.GOOS == "windows" {
		wantLine = "LLM: anthropic (key file present; perm check skipped on Windows"
	}
	if !strings.Contains(out, wantLine) {
		t.Errorf("missing LLM ok line:\n%s", out)
	}
}

// TestStepVerifyLLMWithKeyFileWrongPerms catches the 0644 case:
// user hand-edited a key file without setting 0600. Wizard must
// fail closed with a visible chmod hint.
//
// Skipped on Windows: production correctly skips the perm-bit check
// (NTFS ACL model), so there's no "wrong perm" warn to assert on.
// The corresponding production-side warning belongs to a future
// Windows-equivalent ACL audit, not this test.
func TestStepVerifyLLMWithKeyFileWrongPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm-bit check is skipped on Windows (NTFS ACL model); no warning to assert")
	}
	wiz, buf, tmpDir := newVerifyFixture(t)
	keyPath := filepath.Join(tmpDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.LLM.Provider = "anthropic"
	wiz.cfg.LLM.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "perms are 644") {
		t.Errorf("missing wrong-perm warn:\n%s", out)
	}
	if !strings.Contains(out, "chmod 600") {
		t.Errorf("missing chmod remediation:\n%s", out)
	}
}

// TestStepVerifyHooksInstalledAndExecutable exercises the hooks
// survey branch: install a fake hook script with 0755 perms,
// verifyHooks must report ✓.
func TestStepVerifyHooksInstalledAndExecutable(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)

	hookDir := filepath.Join(tmpDir, "hooks", "claude-code")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "session-start.sh"),
		[]byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Hooks (claude-code): 1 script(s) installed and executable") {
		t.Errorf("missing hooks ok line:\n%s", out)
	}
}

// TestStepVerifyHooksNonExecutable catches the chmod-forgot case:
// hook script present but missing +x. Must warn with a fix hint.
//
// Skipped on Windows: production correctly does not check the exec
// bit there (Windows doesn't model an exec bit; Claude Code's bundled
// Git Bash invokes scripts via shebang regardless of mode bits).
func TestStepVerifyHooksNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bit is meaningless on Windows; production correctly skips this check")
	}
	wiz, buf, tmpDir := newVerifyFixture(t)

	hookDir := filepath.Join(tmpDir, "hooks", "claude-code")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "session-start.sh"),
		[]byte("#!/bin/bash\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "not executable") {
		t.Errorf("missing non-executable warn:\n%s", out)
	}
	if !strings.Contains(out, "chmod +x") {
		t.Errorf("missing chmod remediation:\n%s", out)
	}
}
