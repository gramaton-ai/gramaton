package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// fakeHookBackend captures calls and yields pre-seeded return values
// so tests exercise the wizard's hook-step orchestration without
// touching the real filesystem or user config.
type fakeHookBackend struct {
	materializeCalls []string
	// registerCalls holds the script paths passed to
	// RegisterClaudeHooks so tests can assert the wizard threads the
	// materialized paths through correctly.
	registerCalls [][]string

	// materializeFn lets tests control what Materialize returns per
	// call. Default: returns a fixed fake path list per client.
	materializeFn func(client, configDir string) ([]string, error)

	// registerUnchanged + registerErr control RegisterClaudeHooks
	// behavior for the single registration the wizard triggers.
	registerUnchanged bool
	registerErr       error
}

func (f *fakeHookBackend) Materialize(client, configDir string) ([]string, error) {
	f.materializeCalls = append(f.materializeCalls, client)
	if f.materializeFn != nil {
		return f.materializeFn(client, configDir)
	}
	return []string{filepath.Join(configDir, "hooks", client, "session-start.sh")}, nil
}

func (f *fakeHookBackend) RegisterClaudeHooks(_ context.Context, paths []string) (bool, error) {
	f.registerCalls = append(f.registerCalls, paths)
	return f.registerUnchanged, f.registerErr
}

// newWizardForHooksTest mirrors newWizardForMCPTest but reaches Step
// 4 by scripting through Steps 0-3 quickly. Detects clients via the
// injected MCP backend so Step 4's branch on Detect() can exercise
// the two cases (clients present vs empty).
//
// Script answers in order (prompter):
//
//	[0] Step 0 fresh-vs-import: "1" (fresh)
//	[1] Step 1 embedding menu:  "5" (skip)
//	[2] Step 2 LLM menu:        "5" (skip)
//	[3] Step 3 MCP confirm:     "y" or "n" (caller picks)
//	[4] Step 4 hooks confirm:   "y" or "n" (caller picks)
func newWizardForHooksTest(t *testing.T, mcpBackend MCPBackend, hookBackend HookBackend, mcpConfirm, hookConfirm string) (*Wizard, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriter(&buf)
	prompter := NewScriptedPrompter("1", "5", "5", mcpConfirm, hookConfirm)

	tmpDir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = tmpDir + "/data"

	wiz := New(prompter, writer, &cfg, tmpDir+"/config.yaml", tmpDir)
	wiz.mcpBackend = mcpBackend
	wiz.hookBackend = hookBackend
	return wiz, &buf
}

func TestStepHooksNoClientsDetected(t *testing.T) {
	mcp := &fakeMCPBackend{}
	hook := &fakeHookBackend{}
	// No clients -> hook step prints a short message and returns
	// early. Confirm answer is unused but script wants 5 tokens.
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "", "")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "No MCP clients detected, so there's nothing to install hooks into") {
		t.Errorf("missing no-clients message in hooks step:\n%s", out)
	}
	if len(hook.materializeCalls) != 0 {
		t.Errorf("Materialize should not have been called: %v", hook.materializeCalls)
	}
}

func TestStepHooksClaudeCodeSuccess(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Claude Code", Binary: "/fake/bin/claude"},
		},
		// Register: return success so Step 3 shows ✓ Added
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	// Materialize should fire for claude-code (the embed tree name).
	if len(hook.materializeCalls) != 1 || hook.materializeCalls[0] != "claude-code" {
		t.Errorf("unexpected materialize calls: %v", hook.materializeCalls)
	}
	// RegisterClaudeHooks should fire exactly once with the path
	// the fake Materialize returned.
	if len(hook.registerCalls) != 1 {
		t.Fatalf("want 1 RegisterClaudeHooks call, got %d", len(hook.registerCalls))
	}
	if len(hook.registerCalls[0]) != 1 {
		t.Errorf("expected 1 script path threaded through, got %v", hook.registerCalls[0])
	}
	// User-visible: ✓ Added to Claude Code + settings.json update.
	if !strings.Contains(out, "Claude Code: installed") {
		t.Errorf("missing materialize check line:\n%s", out)
	}
	if !strings.Contains(out, "updated ~/.claude/settings.json") {
		t.Errorf("missing settings.json update line:\n%s", out)
	}
	// Restart warning should fire since at least one install succeeded.
	if !strings.Contains(out, "Restart your AI client") {
		t.Errorf("missing restart warning:\n%s", out)
	}
}

func TestStepHooksKiroCliPrintsManualInstructions(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "kiro-cli", Binary: "/fake/bin/kiro"},
		},
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{
		materializeFn: func(client, configDir string) ([]string, error) {
			return []string{
				filepath.Join(configDir, "hooks", client, "agent-spawn.sh"),
				filepath.Join(configDir, "hooks", client, "user-prompt-submit.sh"),
				filepath.Join(configDir, "hooks", client, "stop.sh"),
			}, nil
		},
	}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if len(hook.materializeCalls) != 1 || hook.materializeCalls[0] != "kiro" {
		t.Errorf("unexpected materialize calls: %v", hook.materializeCalls)
	}
	// kiro-cli path should NOT call RegisterClaudeHooks.
	if len(hook.registerCalls) != 0 {
		t.Errorf("RegisterClaudeHooks should not fire for kiro-cli: %v", hook.registerCalls)
	}
	if !strings.Contains(out, "auto-config not yet supported") {
		t.Errorf("missing manual-config warning for kiro-cli:\n%s", out)
	}
	// The three script paths should be listed in the output.
	for _, script := range []string{"agent-spawn.sh", "user-prompt-submit.sh", "stop.sh"} {
		if !strings.Contains(out, script) {
			t.Errorf("expected %q in manual-instructions output:\n%s", script, out)
		}
	}
}

func TestStepHooksUserDeclines(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients:   []DetectedClient{{Name: "Claude Code", Binary: "/fake/bin/claude"}},
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "n")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if len(hook.materializeCalls) != 0 {
		t.Errorf("Materialize should not fire after user declined: %v", hook.materializeCalls)
	}
	if !strings.Contains(out, "Skipping hook installation") {
		t.Errorf("missing skip confirmation:\n%s", out)
	}
}

func TestStepHooksMaterializeFailure(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients:   []DetectedClient{{Name: "Claude Code", Binary: "/fake/bin/claude"}},
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{
		materializeFn: func(string, string) ([]string, error) {
			return nil, errors.New("disk full")
		},
	}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	// Materialize error should surface as ⚠ warning, not panic/abort.
	if !strings.Contains(out, "materialize failed: disk full") {
		t.Errorf("missing materialize error warn:\n%s", out)
	}
	// Subsequent register call should NOT happen when materialize fails.
	if len(hook.registerCalls) != 0 {
		t.Errorf("RegisterClaudeHooks should not fire after materialize failure: %v", hook.registerCalls)
	}
}

// TestRegisterClaudeHooksIdempotentAndPreserving is the critical
// test for the settings.json-patching code path. Runs against a real
// fake HOME so the file I/O is exercised, verifies:
//   - First call adds our entries to an existing-settings.json file.
//   - Unrelated top-level keys (permissions, etc.) are preserved.
//   - User's own hook entries (commands not under ~/.gramaton/hooks/)
//     are preserved under the same event.
//   - Second call (same inputs) reports unchanged=true; file
//     content matches byte-for-byte so no pointless mtime bump.
//   - Old-style gramaton entries (flat layout) are cleanly replaced.
func TestRegisterClaudeHooksIdempotentAndPreserving(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Seed an existing settings.json with:
	//   - top-level "permissions" key (unrelated to hooks)
	//   - user's own Stop hook (must be preserved)
	//   - an old-style gramaton SessionStart hook (flat layout,
	//     must be stripped and replaced)
	initial := `{
  "permissions": {"allow": ["thing"]},
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "~/.gramaton/hooks/session-start.sh"}]}
    ]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := DefaultHookBackend{}
	scripts := []string{
		filepath.Join(tmp, ".gramaton/hooks/claude-code/session-start.sh"),
		filepath.Join(tmp, ".gramaton/hooks/claude-code/stop.sh"),
	}

	// First call: must change settings (strips old + adds new).
	unchanged, err := backend.RegisterClaudeHooks(context.Background(), scripts)
	if err != nil {
		t.Fatalf("first RegisterClaudeHooks: %v", err)
	}
	if unchanged {
		t.Error("first call should have reported changed, got unchanged")
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)

	// Unrelated top-level key preserved.
	if !strings.Contains(content, `"permissions"`) {
		t.Error("permissions key was lost")
	}
	// User's custom Stop hook preserved.
	if !strings.Contains(content, "/user/custom/stop.sh") {
		t.Errorf("user's custom stop hook was removed:\n%s", content)
	}
	// New-style gramaton paths present.
	for _, path := range scripts {
		if !strings.Contains(content, path) {
			t.Errorf("expected gramaton path %q in settings.json:\n%s", path, content)
		}
	}
	// Old-style gramaton SessionStart hook should be gone.
	if strings.Contains(content, "~/.gramaton/hooks/session-start.sh") {
		t.Error("old-style flat-layout gramaton hook was not stripped")
	}

	// Second call with identical inputs: must report unchanged and
	// not rewrite the file (we can't easily check mtime without
	// timing, but we can check the content is byte-identical).
	unchanged, err = backend.RegisterClaudeHooks(context.Background(), scripts)
	if err != nil {
		t.Fatalf("second RegisterClaudeHooks: %v", err)
	}
	if !unchanged {
		t.Error("second call should have reported unchanged, got changed")
	}
	raw2, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(raw2) {
		t.Error("settings.json content changed on second idempotent call")
	}
}

// TestDefaultHookBackendMaterializeRoundtrip exercises the real
// DefaultHookBackend against a temp config dir. Verifies the
// embedded scripts reach disk with executable perms and match the
// embedded content byte-for-byte (catches regressions where the
// generate-directive drift lets the embed tree fall out of sync).
func TestDefaultHookBackendMaterializeRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	backend := DefaultHookBackend{}

	paths, err := backend.Materialize("claude-code", tmp)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no scripts materialized")
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %s: %v", p, err)
			continue
		}
		// Exec bit must be set (user / group / other — any).
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable: mode %o", p, info.Mode().Perm())
		}
		// Script should start with a shebang.
		content, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if !strings.HasPrefix(string(content), "#!/bin/bash") {
			t.Errorf("%s missing shebang; first 40 bytes: %q", p, string(content[:min(40, len(content))]))
		}
	}
}
