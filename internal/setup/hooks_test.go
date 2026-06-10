package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// fakeHookBackend captures calls and yields pre-seeded return values
// so tests exercise the wizard's hook-step orchestration without
// touching the real filesystem or user config.
type fakeHookBackend struct {
	materializeCalls []string
	// registerCalls holds the script paths passed to RegisterHooks
	// so tests can assert the wizard threads the materialized paths
	// through correctly; registerClients holds the client names of
	// those calls, index-aligned.
	registerCalls   [][]string
	registerClients []string

	// materializeFn lets tests control what Materialize returns per
	// call. Default: returns a fixed fake path list per client.
	materializeFn func(client, configDir string) ([]string, error)

	// registerUnchanged + registerErr control RegisterHooks
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

func (f *fakeHookBackend) RegisterHooks(_ context.Context, client string, paths []string) (bool, error) {
	f.registerCalls = append(f.registerCalls, paths)
	f.registerClients = append(f.registerClients, client)
	return f.registerUnchanged, f.registerErr
}

// newWizardForHooksTest mirrors newWizardForMCPTest but reaches Step
// 5 (hooks) by scripting through Steps 0-4 quickly. Detects clients
// via the injected MCP backend so Step 5's branch on Detect() can
// exercise the two cases (clients present vs empty).
//
// Script answers in order (prompter):
//
//	[0] Step 0 fresh-vs-import:      "1" (fresh)
//	[1] Step 1 embedding menu:       "5" (skip)
//	[2] Step 2 LLM menu:             "5" (skip)
//	[3] Step 3 MCP confirm:          "y" or "n" (caller picks)
//	[4] Step 4 instructions confirm: always "n" (skip install; tests
//	                                 for step 4 are in
//	                                 step_instructions_test.go and
//	                                 don't need full-wizard driving)
//	[5] Step 5 hooks confirm:        "y" or "n" (caller picks)
//
// HOME is pointed at the tmpDir so the skipped instructions step
// (and any other step that might touch $HOME) doesn't scribble on
// the real user's home directory.
func newWizardForHooksTest(t *testing.T, mcpBackend MCPBackend, hookBackend HookBackend, mcpConfirm, hookConfirm string) (*Wizard, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriter(&buf)
	prompter := NewScriptedPrompter("1", "5", "5", mcpConfirm, "n", hookConfirm)

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir) // Windows: os.UserHomeDir reads %USERPROFILE%, not $HOME
	// Hermeticity: stepVerify's MCP survey probes the real PATH
	// (bypassing the injected backends); empty it so dev machines
	// with claude/codex installed don't shell out mid-test.
	t.Setenv("PATH", "")
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
	// RegisterHooks should fire exactly once, for the claude-code
	// embed dir, with the path the fake Materialize returned.
	if len(hook.registerCalls) != 1 {
		t.Fatalf("want 1 RegisterHooks call, got %d", len(hook.registerCalls))
	}
	if hook.registerClients[0] != "claude-code" {
		t.Errorf("RegisterHooks client = %q, want claude-code", hook.registerClients[0])
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
	// kiro-cli has no WireHooks strategy, so RegisterHooks must not
	// fire.
	if len(hook.registerCalls) != 0 {
		t.Errorf("RegisterHooks should not fire for kiro-cli: %v", hook.registerCalls)
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
		t.Errorf("RegisterHooks should not fire after materialize failure: %v", hook.registerCalls)
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
	t.Setenv("USERPROFILE", tmp) // Windows: os.UserHomeDir reads %USERPROFILE%, not $HOME

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Seed an existing settings.json with:
	//   - a UTF-8 BOM (Windows Notepad prepends one; must parse)
	//   - top-level "permissions" key (unrelated to hooks)
	//   - user's own Stop hook (must be preserved)
	//   - a matcher block with NO hooks array (not our shape; must
	//     be preserved verbatim, not dropped)
	//   - an old-style gramaton SessionStart hook (flat layout,
	//     must be stripped and replaced)
	initial := "\xEF\xBB\xBF" + `{
  "permissions": {"allow": ["thing"]},
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]},
      {"matcher": "user-block-without-hooks-array"}
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
	// Goes through the backend's RegisterHooks dispatch so the
	// registry wiring (embed dir -> strategy func) is exercised too.
	unchanged, err := backend.RegisterHooks(context.Background(), "claude-code", scripts)
	if err != nil {
		t.Fatalf("first RegisterHooks: %v", err)
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
	// Matcher block without a hooks array preserved verbatim.
	if !strings.Contains(content, "user-block-without-hooks-array") {
		t.Errorf("hooks-less matcher block was dropped:\n%s", content)
	}
	// New-style gramaton paths present. Production normalizes
	// backslashes to forward slashes before writing settings.json
	// (Claude Code's bundled Git Bash on Windows interprets backslashes
	// as escapes; see the wanted-map comment in registerClaudeHooks
	// for the rationale). So an
	// assertion against the raw filepath.Join output -- which is
	// backslash-formed on Windows -- would miss. Normalize the way
	// production does, then assert.
	for _, path := range scripts {
		want := strings.ReplaceAll(path, `\`, "/")
		if !strings.Contains(content, want) {
			t.Errorf("expected gramaton path %q in settings.json:\n%s", want, content)
		}
	}
	// Old-style gramaton SessionStart hook should be gone.
	if strings.Contains(content, "~/.gramaton/hooks/session-start.sh") {
		t.Error("old-style flat-layout gramaton hook was not stripped")
	}

	// Second call with identical inputs: must report unchanged and
	// not rewrite the file (we can't easily check mtime without
	// timing, but we can check the content is byte-identical).
	unchanged, err = backend.RegisterHooks(context.Background(), "claude-code", scripts)
	if err != nil {
		t.Fatalf("second RegisterHooks: %v", err)
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

// TestPathNormalizationForClaudeBash documents the backslash-to-
// forward-slash transformation applied to hook script paths before
// they land in settings.json. Without this, Claude Code's bundled
// Git Bash sees `C:\Users\op\.gramaton\...` and its backslash-as-
// escape-char processing turns it into `C:Usersop.gramaton...` —
// file not found. Regression-test for a real bug discovered on a
// Windows install, 2026-04-24.
//
// This test does NOT go through registerClaudeHooks because
// filepath.Base on macOS doesn't treat `\` as a separator (Unix
// filesystems permit backslashes in filenames), so the
// hookEventForConfig lookup inside registerClaudeHooks fails to
// parse a faux-Windows path on macOS. The transformation itself
// is what we're testing; the call-through chain is a separate
// concern covered by TestRegisterClaudeHooksIdempotentAndPreserving.
func TestPathNormalizationForClaudeBash(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "windows path gets forward-slashed",
			in:   `C:\Users\op\.gramaton\hooks\claude-code\session-start.sh`,
			want: "C:/Users/op/.gramaton/hooks/claude-code/session-start.sh",
		},
		{
			name: "unix path unchanged",
			in:   "/Users/op/.gramaton/hooks/claude-code/session-start.sh",
			want: "/Users/op/.gramaton/hooks/claude-code/session-start.sh",
		},
		{
			name: "already forward-slashed windows path unchanged",
			in:   "C:/Users/op/.gramaton/hooks/claude-code/session-start.sh",
			want: "C:/Users/op/.gramaton/hooks/claude-code/session-start.sh",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ReplaceAll(tc.in, `\`, "/")
			if got != tc.want {
				t.Errorf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
		// Windows has no POSIX exec bit; os.Stat reports 0o666 or
		// 0o444 regardless of the actual permission, so the
		// assertion is meaningless on Windows. The shebang check
		// below still covers script integrity cross-platform.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
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

func TestStepHooksCodexSuccess(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Codex", Binary: "/fake/bin/codex"},
		},
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if len(hook.materializeCalls) != 1 || hook.materializeCalls[0] != "codex" {
		t.Errorf("unexpected materialize calls: %v", hook.materializeCalls)
	}
	if len(hook.registerCalls) != 1 || hook.registerClients[0] != "codex" {
		t.Fatalf("want 1 RegisterHooks call for codex, got clients %v", hook.registerClients)
	}
	if !strings.Contains(out, "updated ~/.codex/hooks.json") {
		t.Errorf("missing hooks.json update line:\n%s", out)
	}
}

// TestMaterializeCodexDualVariant pins the proxyDualVariant
// behavior: BOTH .sh and .cmd scripts for every Codex event, on any
// host OS, with per-interpreter line endings. The .cmd files exist
// even on a macOS/Linux install because the hooks.json entry carries
// command + commandWindows and Codex picks at runtime.
func TestMaterializeCodexDualVariant(t *testing.T) {
	tmp := t.TempDir()
	paths, err := DefaultHookBackend{}.Materialize("codex", tmp)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if want := len(codexEvents) * 2; len(paths) != want {
		t.Fatalf("got %d scripts, want %d (both variants per event):\n%v", len(paths), want, paths)
	}
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		body := string(content)
		switch {
		case strings.HasSuffix(p, ".sh"):
			if !strings.HasPrefix(body, "#!/bin/bash\n") {
				t.Errorf("%s: bad shebang: %q", p, body)
			}
			if strings.Contains(body, "\r") {
				t.Errorf("%s: .sh proxy must be LF-only (CRLF breaks the shebang): %q", p, body)
			}
		case strings.HasSuffix(p, ".cmd"):
			if !strings.HasPrefix(body, "@gramaton hook ") {
				t.Errorf("%s: bad .cmd body: %q", p, body)
			}
			if !strings.HasSuffix(body, "\r\n") {
				t.Errorf("%s: .cmd proxy must use CRLF line endings: %q", p, body)
			}
		default:
			t.Errorf("unexpected extension: %s", p)
		}
	}
}

func TestCodexConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	t.Setenv("CODEX_HOME", "")
	got, err := codexConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".codex"); got != want {
		t.Errorf("default codexConfigDir = %q, want %q", got, want)
	}

	override := filepath.Join(home, "relocated-codex")
	t.Setenv("CODEX_HOME", override)
	got, err = codexConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != override {
		t.Errorf("CODEX_HOME codexConfigDir = %q, want %q", got, override)
	}

	// A relative CODEX_HOME must error, not scatter config under the
	// wizard's cwd.
	t.Setenv("CODEX_HOME", "relative/codex")
	if _, err := codexConfigDir(); err == nil {
		t.Error("relative CODEX_HOME should be rejected")
	}
}

// codexTestScripts returns the dual-variant script paths for the
// four Codex events, as Materialize would produce them under tmp.
func codexTestScripts(tmp string) []string {
	var scripts []string
	for _, ev := range codexEvents {
		scripts = append(scripts,
			filepath.Join(tmp, ".gramaton", "hooks", "codex", ev.fileBase+".sh"),
			filepath.Join(tmp, ".gramaton", "hooks", "codex", ev.fileBase+".cmd"),
		)
	}
	return scripts
}

// TestRegisterCodexHooksFreshCreate covers the fresh-install path:
// no hooks.json (and possibly no ~/.codex/) exists yet. The file is
// created with one entry per event carrying both command (.sh,
// forward-slashed) and commandWindows (.cmd).
func TestRegisterCodexHooksFreshCreate(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, "codex-home") // does not exist yet
	t.Setenv("CODEX_HOME", codexHome)

	unchanged, err := DefaultHookBackend{}.RegisterHooks(context.Background(), "codex", codexTestScripts(tmp))
	if err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	if unchanged {
		t.Error("fresh create should report changed")
	}

	raw, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil {
		t.Fatalf("hooks.json not created: %v", err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type           string `json:"type"`
				Command        string `json:"command"`
				CommandWindows string `json:"commandWindows"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	for _, ev := range codexEvents {
		blocks := parsed.Hooks[ev.configEvent]
		if len(blocks) != 1 || len(blocks[0].Hooks) != 1 {
			t.Fatalf("%s: want exactly 1 block with 1 entry, got %+v", ev.configEvent, blocks)
		}
		entry := blocks[0].Hooks[0]
		if entry.Type != "command" {
			t.Errorf("%s: type = %q, want command", ev.configEvent, entry.Type)
		}
		if !strings.HasSuffix(entry.Command, ev.fileBase+".sh") || strings.Contains(entry.Command, `\`) {
			t.Errorf("%s: command = %q, want forward-slashed .sh path", ev.configEvent, entry.Command)
		}
		if !strings.HasSuffix(entry.CommandWindows, ev.fileBase+".cmd") {
			t.Errorf("%s: commandWindows = %q, want .cmd path", ev.configEvent, entry.CommandWindows)
		}
	}
}

// TestRegisterCodexHooksIdempotentAndPreserving mirrors the Claude
// settings.json test: user entries under the same event survive, a
// legacy gramaton entry is replaced (matched via either command
// field), unrelated top-level keys survive, and a second identical
// call reports unchanged without rewriting the file. Also seeds a
// UTF-8 BOM to pin the Windows-editor tolerance.
func TestRegisterCodexHooksIdempotentAndPreserving(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(codexHome, "hooks.json")

	initial := "\xEF\xBB\xBF" + `{
  "unrelated": {"keep": true},
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "commandWindows": "C:\\Users\\x\\.gramaton\\hooks\\codex\\session-start.cmd"}]}
    ]
  }
}`
	if err := os.WriteFile(hooksPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	scripts := codexTestScripts(tmp)
	unchanged, err := DefaultHookBackend{}.RegisterHooks(context.Background(), "codex", scripts)
	if err != nil {
		t.Fatalf("first RegisterHooks: %v", err)
	}
	if unchanged {
		t.Error("first call should have reported changed")
	}

	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, `"unrelated"`) {
		t.Error("unrelated top-level key was lost")
	}
	if !strings.Contains(content, "/user/custom/stop.sh") {
		t.Errorf("user's custom Stop hook was removed:\n%s", content)
	}
	// The legacy gramaton entry (recognizable only by its
	// commandWindows path) must be stripped.
	if strings.Contains(content, `C:\\Users\\x\\.gramaton`) {
		t.Errorf("legacy gramaton entry not replaced:\n%s", content)
	}

	unchanged, err = DefaultHookBackend{}.RegisterHooks(context.Background(), "codex", scripts)
	if err != nil {
		t.Fatalf("second RegisterHooks: %v", err)
	}
	if !unchanged {
		t.Error("second call should have reported unchanged")
	}
	raw2, _ := os.ReadFile(hooksPath)
	if string(raw) != string(raw2) {
		t.Error("hooks.json changed on second idempotent call")
	}
}

// TestRegisterCodexHooksMalformedJSON pins the won't-touch-it
// behavior: a hooks.json we can't parse is an error, not a clobber.
func TestRegisterCodexHooksMalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(codexHome, "hooks.json")
	garbage := "{not json"
	if err := os.WriteFile(hooksPath, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := DefaultHookBackend{}.RegisterHooks(context.Background(), "codex", codexTestScripts(tmp))
	if err == nil {
		t.Fatal("expected parse error on malformed hooks.json")
	}
	raw, _ := os.ReadFile(hooksPath)
	if string(raw) != garbage {
		t.Error("malformed hooks.json was modified; must be left untouched")
	}
}

func TestStepHooksCursorSuccess(t *testing.T) {
	mcp := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Cursor"}, // dir-detected: no binary
		},
		registers: []fakeRegisterResult{{false, nil}},
	}
	hook := &fakeHookBackend{}
	wiz, buf := newWizardForHooksTest(t, mcp, hook, "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if len(hook.materializeCalls) != 1 || hook.materializeCalls[0] != "cursor" {
		t.Errorf("unexpected materialize calls: %v", hook.materializeCalls)
	}
	if len(hook.registerCalls) != 1 || hook.registerClients[0] != "cursor" {
		t.Fatalf("want 1 RegisterHooks call for cursor, got clients %v", hook.registerClients)
	}
	if !strings.Contains(out, "updated ~/.cursor/hooks.json") {
		t.Errorf("missing hooks.json update line:\n%s", out)
	}
}

// TestMaterializeCursorNativePerOS pins the proxyNativePerOS
// behavior for Cursor: exactly one variant per event, chosen from
// the host OS (Cursor's hooks.json has no commandWindows field, so
// the config can only point at one script).
func TestMaterializeCursorNativePerOS(t *testing.T) {
	tmp := t.TempDir()
	paths, err := DefaultHookBackend{}.Materialize("cursor", tmp)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(paths) != len(cursorEvents) {
		t.Fatalf("got %d scripts, want %d (one variant per event):\n%v", len(paths), len(cursorEvents), paths)
	}
	wantExt := ".sh"
	if runtime.GOOS == "windows" {
		wantExt = ".cmd"
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, wantExt) {
			t.Errorf("%s: want %s variant on %s", p, wantExt, runtime.GOOS)
		}
	}
}

// cursorTestScripts returns the native-variant script paths for the
// three Cursor events, as Materialize would produce them under tmp
// on a POSIX host.
func cursorTestScripts(tmp string) []string {
	var scripts []string
	for _, ev := range cursorEvents {
		scripts = append(scripts,
			filepath.Join(tmp, ".gramaton", "hooks", "cursor", ev.fileBase+".sh"))
	}
	return scripts
}

// TestRegisterCursorHooksFreshCreate covers the fresh-install path:
// ~/.cursor/hooks.json does not exist (verified: Cursor doesn't
// auto-create it). The file is created with version 1 and one flat
// entry per event -- Cursor's schema has no nested matcher blocks.
func TestRegisterCursorHooksFreshCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	unchanged, err := DefaultHookBackend{}.RegisterHooks(context.Background(), "cursor", cursorTestScripts(home))
	if err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	if unchanged {
		t.Error("fresh create should report changed")
	}

	raw, err := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatalf("hooks.json not created: %v", err)
	}
	var parsed struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
			Hooks   []any  `json:"hooks"` // must stay empty: flat schema
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if parsed.Version != 1 {
		t.Errorf("version = %d, want 1 (required by Cursor's schema)", parsed.Version)
	}
	for _, ev := range cursorEvents {
		entries := parsed.Hooks[ev.configEvent]
		if len(entries) != 1 {
			t.Fatalf("%s: want exactly 1 entry, got %+v", ev.configEvent, entries)
		}
		if !strings.HasSuffix(entries[0].Command, ev.fileBase+".sh") {
			t.Errorf("%s: command = %q, want path ending %s.sh", ev.configEvent, entries[0].Command, ev.fileBase)
		}
		if len(entries[0].Hooks) != 0 {
			t.Errorf("%s: entry has a nested hooks array -- Cursor's schema is flat", ev.configEvent)
		}
	}
}

// TestRegisterCursorHooksPreservesAndIdempotent seeds hooks.json
// with an existing version, a user entry, a legacy gramaton entry,
// and a UTF-8 BOM. User content survives, the legacy entry is
// replaced, the existing version value is untouched, and the second
// run is a byte-identical no-op.
func TestRegisterCursorHooksPreservesAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(cursorDir, "hooks.json")

	// version: 2 (not the 1 production writes when absent) so the
	// preserved-verbatim assertion below is discriminating.
	initial := "\xEF\xBB\xBF" + `{
  "version": 2,
  "hooks": {
    "stop": [
      {"command": "/user/custom/stop.sh", "failClosed": true}
    ],
    "sessionStart": [
      {"command": "/Users/old/.gramaton/hooks/cursor/session-start.sh"}
    ]
  }
}`
	if err := os.WriteFile(hooksPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	scripts := cursorTestScripts(home)
	unchanged, err := DefaultHookBackend{}.RegisterHooks(context.Background(), "cursor", scripts)
	if err != nil {
		t.Fatalf("first RegisterHooks: %v", err)
	}
	if unchanged {
		t.Error("first call should have reported changed")
	}

	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "/user/custom/stop.sh") {
		t.Errorf("user's custom stop hook was removed:\n%s", content)
	}
	if !strings.Contains(content, `"version": 2`) {
		t.Errorf("existing version value was not preserved:\n%s", content)
	}
	if !strings.Contains(content, `"failClosed": true`) {
		t.Errorf("user entry's fields were not preserved verbatim:\n%s", content)
	}
	if strings.Contains(content, "/Users/old/.gramaton/") {
		t.Errorf("legacy gramaton entry not replaced:\n%s", content)
	}

	unchanged, err = DefaultHookBackend{}.RegisterHooks(context.Background(), "cursor", scripts)
	if err != nil {
		t.Fatalf("second RegisterHooks: %v", err)
	}
	if !unchanged {
		t.Error("second call should have reported unchanged")
	}
	raw2, _ := os.ReadFile(hooksPath)
	if string(raw) != string(raw2) {
		t.Error("hooks.json changed on second idempotent call")
	}
}

// TestRegisterCursorHooksMalformedJSON pins the won't-touch behavior
// for the cursor patcher (the codex twin is separate code with its
// own test).
func TestRegisterCursorHooksMalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	garbage := "{not json"
	if err := os.WriteFile(hooksPath, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := (DefaultHookBackend{}).RegisterHooks(context.Background(), "cursor", cursorTestScripts(home)); err == nil {
		t.Fatal("expected parse error on malformed hooks.json")
	}
	raw, _ := os.ReadFile(hooksPath)
	if string(raw) != garbage {
		t.Error("malformed hooks.json was modified; must be left untouched")
	}
}

// TestRegisterCursorHooksEnvelopeTypeMismatch pins the won't-touch
// behavior for parseable-but-wrong-shape files: a "hooks" value that
// isn't an object is an error, not a silent replace.
func TestRegisterCursorHooksEnvelopeTypeMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	seed := `{"version": 1, "hooks": "i am not an object"}`
	if err := os.WriteFile(hooksPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := (DefaultHookBackend{}).RegisterHooks(context.Background(), "cursor", cursorTestScripts(home)); err == nil {
		t.Fatal("expected error on non-object hooks value")
	}
	raw, _ := os.ReadFile(hooksPath)
	if string(raw) != seed {
		t.Error("wrong-shape hooks.json was modified; must be left untouched")
	}
}

// TestRegisterHooksUnknownClient pins the dispatch error path.
func TestRegisterHooksUnknownClient(t *testing.T) {
	if _, err := (DefaultHookBackend{}).RegisterHooks(context.Background(), "no-such-client", nil); err == nil {
		t.Error("expected error for unknown client")
	}
	// kiro exists but has no WireHooks strategy.
	if _, err := (DefaultHookBackend{}).RegisterHooks(context.Background(), "kiro", nil); err == nil {
		t.Error("expected error for client without a wiring strategy")
	}
}
