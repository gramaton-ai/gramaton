package setup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// setUninstallTestEnv points every home/config-root/PATH resolution
// at throwaway locations so no test can touch the real user
// environment or find a real harness binary. Returns the fake home.
func setUninstallTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows: os.UserHomeDir reads %USERPROFILE%, not $HOME
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

// fakeBinaryOnPath drops non-functional executables named after
// harness binaries into a fresh PATH dir, so exec.LookPath finds
// them while the runHarnessCommand seam intercepts every actual
// invocation.
func fakeBinaryOnPath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		fn := n
		if runtime.GOOS == "windows" {
			fn += ".bat"
		}
		if err := os.WriteFile(filepath.Join(dir, fn), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// swapHarnessCommand installs a fake exec seam and restores the real
// one at cleanup.
func swapHarnessCommand(t *testing.T, fake func(ctx context.Context, bin string, args ...string) (string, error)) {
	t.Helper()
	orig := runHarnessCommand
	runHarnessCommand = fake
	t.Cleanup(func() { runHarnessCommand = orig })
}

// snapshotTree captures every file and directory under root:
// relative path -> content hash (files) or "dir". Used to prove
// dry-run and probe passes mutate nothing.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func countBackups(t *testing.T, path string) int {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// --- naming convention + parsers -----------------------------------

func TestIsGramatonMCPEntryName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"gramaton", true},
		{"gramaton-work", true},
		{"gramaton-", true}, // degenerate but convention-prefixed
		{"gramatonx", false},
		{"my-gramaton", false},
		{"GRAMATON", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isGramatonMCPEntryName(tt.name); got != tt.want {
			t.Errorf("isGramatonMCPEntryName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestIsGramatonCommandToken pins the command leg of the ownership
// check: bare "gramaton", any path ending in /gramaton (or a Windows
// .exe variant) is ours; everything else is a foreign binary.
func TestIsGramatonCommandToken(t *testing.T) {
	tests := []struct {
		tok  string
		want bool
	}{
		{"gramaton", true},
		{"/opt/homebrew/bin/gramaton", true},
		{`C:\Users\x\go\bin\gramaton.exe`, true},
		{"gramaton.exe", true},
		{"othercmd", false},
		{"/usr/bin/notgramaton", false},
		{"gramatond", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isGramatonCommandToken(tt.tok); got != tt.want {
			t.Errorf("isGramatonCommandToken(%q) = %v, want %v", tt.tok, got, tt.want)
		}
	}
}

// TestParseColonMCPList pins the `claude mcp list` parser: the entry
// name before the first colon must match the naming convention AND
// the command token after it must run gramaton. gramatonx, unrelated
// servers, and convention-NAMED entries running a foreign command
// must not be selected; failed-health-check status tails parse the
// same as connected ones.
func TestParseColonMCPList(t *testing.T) {
	out := "Checking MCP server health...\n" +
		"\n" +
		"gramaton: gramaton mcp - ✓ Connected\n" +
		"gramaton-work: gramaton --store work mcp - ✗ Failed to connect\n" +
		"gramaton-abs: /opt/homebrew/bin/gramaton mcp - ✓ Connected\n" +
		"gramaton-evil: othercmd mcp - ✓ Connected\n" +
		"gramatonx: something else - ✓ Connected\n" +
		"playwright: npx @playwright/mcp@latest - ✗ Failed to connect\n" +
		"gramaton: gramaton mcp - duplicate line should dedupe\n"
	got := parseColonMCPList(out)
	want := []string{"gramaton", "gramaton-abs", "gramaton-work"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseColonMCPList = %v, want %v", got, want)
	}
}

// TestParseTokenMCPList pins the tabular parser (`codex mcp list`,
// best-effort kiro): first token is the name, second the command,
// and BOTH ownership legs must hold. The "notes" row runs the
// gramaton binary but is not convention-named; "gramaton-evil" is
// convention-named but runs a foreign binary; neither is ours. Under
// strictCommand a row with no visible command column is refused;
// the lenient variant (kiro, format unverified) accepts it on the
// name convention alone.
func TestParseTokenMCPList(t *testing.T) {
	out := "Name          Command   Args              Env\n" +
		"gramaton      gramaton  mcp               -\n" +
		"gramaton-foo  gramaton  --store foo mcp   -\n" +
		"gramaton-evil othercmd  -                 -\n" +
		"gramatonx     other     -                 -\n" +
		"notes         gramaton  mcp               -\n" +
		"gramaton-bare\n"
	strict := parseTokenMCPList(out, true)
	if want := []string{"gramaton", "gramaton-foo"}; !reflect.DeepEqual(strict, want) {
		t.Errorf("strict parseTokenMCPList = %v, want %v", strict, want)
	}
	lenient := parseTokenMCPList(out, false)
	if want := []string{"gramaton", "gramaton-bare", "gramaton-foo"}; !reflect.DeepEqual(lenient, want) {
		t.Errorf("lenient parseTokenMCPList = %v, want %v", lenient, want)
	}
}

// TestRunHarnessCommandSetsNoAutostart pins MAJOR safety plumbing:
// every vendor-CLI invocation must carry GRAMATON_NO_AUTOSTART=1 in
// its environment, because `claude mcp list` health-checks stdio
// entries by spawning them and a spawned `gramaton mcp` must not
// auto-start a server.
func TestRunHarnessCommandSetsNoAutostart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	out, err := runHarnessCommand(context.Background(), "/bin/sh", "-c", `printf '%s' "$GRAMATON_NO_AUTOSTART"`)
	if err != nil {
		t.Fatalf("runHarnessCommand: %v", err)
	}
	if out != "1" {
		t.Errorf("GRAMATON_NO_AUTOSTART in vendor-CLI env = %q, want \"1\"", out)
	}
}

// --- vendor-CLI strategies through the exec seam --------------------

// TestClaudeMCPListAndRemoveArgv proves enumeration parsing and the
// exact removal argv -- including the load-bearing `--scope user`,
// which must match the scope install registered with.
func TestClaudeMCPListAndRemoveArgv(t *testing.T) {
	var calls [][]string
	swapHarnessCommand(t, func(_ context.Context, bin string, args ...string) (string, error) {
		calls = append(calls, append([]string{bin}, args...))
		if len(args) >= 2 && args[0] == "mcp" && args[1] == "list" {
			return "gramaton: gramaton mcp - ✓ Connected\ngramaton-work: gramaton --store work mcp - ✓ Connected\n", nil
		}
		return "", nil
	})

	h := harnessByName(harnessClaudeCode)
	entries, err := h.ListMCPEntries(context.Background(), "/fake/claude")
	if err != nil {
		t.Fatalf("ListMCPEntries: %v", err)
	}
	if want := []string{"gramaton", "gramaton-work"}; !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}

	removed, backup, err := h.RemoveMCPEntries(context.Background(), "/fake/claude", entries)
	if err != nil {
		t.Fatalf("RemoveMCPEntries: %v", err)
	}
	if backup != "" {
		t.Errorf("claude removal should not write a backup, got %q", backup)
	}
	if !reflect.DeepEqual(removed, entries) {
		t.Errorf("removed = %v, want %v", removed, entries)
	}

	wantCalls := [][]string{
		{"/fake/claude", "mcp", "list"},
		{"/fake/claude", "mcp", "remove", "--scope", "user", "gramaton"},
		{"/fake/claude", "mcp", "remove", "--scope", "user", "gramaton-work"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("exec calls = %v, want %v", calls, wantCalls)
	}
}

// TestCodexMCPRemoveArgv pins Codex's removal argv: positional entry
// name, no scope flag (config.toml entries are user-global).
func TestCodexMCPRemoveArgv(t *testing.T) {
	var calls [][]string
	swapHarnessCommand(t, func(_ context.Context, bin string, args ...string) (string, error) {
		calls = append(calls, append([]string{bin}, args...))
		return "", nil
	})

	h := harnessByName(harnessCodex)
	removed, _, err := h.RemoveMCPEntries(context.Background(), "/fake/codex", []string{"gramaton", "gramaton-foo"})
	if err != nil {
		t.Fatalf("RemoveMCPEntries: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed = %v, want both entries", removed)
	}
	wantCalls := [][]string{
		{"/fake/codex", "mcp", "remove", "gramaton"},
		{"/fake/codex", "mcp", "remove", "gramaton-foo"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("exec calls = %v, want %v", calls, wantCalls)
	}
}

// TestClaudeMCPRemoveFailureReportsPartialProgress: a mid-run remove
// failure must return the entries already removed (so the report is
// truthful) plus an error naming the failing entry.
func TestClaudeMCPRemoveFailureReportsPartialProgress(t *testing.T) {
	swapHarnessCommand(t, func(_ context.Context, _ string, args ...string) (string, error) {
		if args[len(args)-1] == "gramaton-bad" {
			return "no such server", fmt.Errorf("exit status 1")
		}
		return "", nil
	})

	h := harnessByName(harnessClaudeCode)
	removed, _, err := h.RemoveMCPEntries(context.Background(), "/fake/claude", []string{"gramaton", "gramaton-bad"})
	if err == nil {
		t.Fatal("expected error for failing entry")
	}
	if !strings.Contains(err.Error(), "gramaton-bad") {
		t.Errorf("error should name the failing entry: %v", err)
	}
	if want := []string{"gramaton"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}

// --- Cursor mcp.json ------------------------------------------------

const cursorMCPFixture = `{
  "mcpServers": {
    "gramaton": {"type": "stdio", "command": "gramaton", "args": ["mcp"]},
    "gramaton-work": {"type": "stdio", "command": "gramaton", "args": ["--store", "work", "mcp"]},
    "gramaton-x": {"type": "stdio", "command": "other-binary", "args": []},
    "playwright": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp@latest"]}
  },
  "unknownTopLevel": {"keep": true}
}`

// TestCursorMCPListAndRemove: convention-named entries whose command
// is "gramaton" are enumerated and removed; gramaton-x (convention
// name, foreign command) and every unrelated server / top-level key
// survive; a timestamped backup holds the original bytes.
func TestCursorMCPListAndRemove(t *testing.T) {
	home := setUninstallTestEnv(t)
	mcpPath := filepath.Join(home, ".cursor", "mcp.json")
	mustWriteFile(t, mcpPath, cursorMCPFixture)

	h := harnessByName(harnessCursor)
	entries, err := h.ListMCPEntries(context.Background(), "")
	if err != nil {
		t.Fatalf("ListMCPEntries: %v", err)
	}
	if want := []string{"gramaton", "gramaton-work"}; !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v (gramaton-x runs a foreign command and is not ours)", entries, want)
	}

	removed, backup, err := h.RemoveMCPEntries(context.Background(), "", entries)
	if err != nil {
		t.Fatalf("RemoveMCPEntries: %v", err)
	}
	if !reflect.DeepEqual(removed, entries) {
		t.Errorf("removed = %v, want %v", removed, entries)
	}

	if backup == "" {
		t.Fatal("expected a backup path")
	}
	backupRaw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupRaw) != cursorMCPFixture {
		t.Error("backup does not hold the original bytes")
	}

	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rewritten mcp.json unparseable: %v", err)
	}
	servers := doc["mcpServers"].(map[string]any)
	for _, gone := range []string{"gramaton", "gramaton-work"} {
		if _, ok := servers[gone]; ok {
			t.Errorf("entry %q should have been removed", gone)
		}
	}
	var origDoc map[string]any
	if err := json.Unmarshal([]byte(cursorMCPFixture), &origDoc); err != nil {
		t.Fatal(err)
	}
	origServers := origDoc["mcpServers"].(map[string]any)
	for _, keep := range []string{"gramaton-x", "playwright"} {
		if !reflect.DeepEqual(servers[keep], origServers[keep]) {
			t.Errorf("entry %q was altered: got %v, want %v", keep, servers[keep], origServers[keep])
		}
	}
	if !reflect.DeepEqual(doc["unknownTopLevel"], origDoc["unknownTopLevel"]) {
		t.Errorf("unknown top-level key was altered: %v", doc["unknownTopLevel"])
	}
}

// TestCursorMCPRefusals: unparseable JSON and a non-object
// mcpServers value are refused untouched, exactly like install.
func TestCursorMCPRefusals(t *testing.T) {
	for name, content := range map[string]string{
		"unparseable":           "{not json",
		"non-object mcpServers": `{"mcpServers": "nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := setUninstallTestEnv(t)
			mcpPath := filepath.Join(home, ".cursor", "mcp.json")
			mustWriteFile(t, mcpPath, content)

			h := harnessByName(harnessCursor)
			if _, err := h.ListMCPEntries(context.Background(), ""); err == nil {
				t.Error("ListMCPEntries should refuse")
			}
			if _, _, err := h.RemoveMCPEntries(context.Background(), "", []string{"gramaton"}); err == nil {
				t.Error("RemoveMCPEntries should refuse")
			}
			raw, err := os.ReadFile(mcpPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != content {
				t.Error("refused file was modified")
			}
			if countBackups(t, mcpPath) != 0 {
				t.Error("refused file should not get a backup")
			}
		})
	}
}

// --- hook unregister -------------------------------------------------

// TestUnregisterClaudeHooksPreservesUserEntries: gramaton entries are
// stripped (default layout AND legacy flat layout), matcher blocks we
// emptied are dropped, user hooks and unrelated keys survive
// untouched, a backup is written, and a second run is a byte-level
// no-op with no new backup.
func TestUnregisterClaudeHooksPreservesUserEntries(t *testing.T) {
	home := setUninstallTestEnv(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	initial := `{
  "permissions": {"allow": ["thing"]},
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]},
      {"hooks": [{"type": "command", "command": "/home/x/.gramaton/hooks/claude-code/stop.sh"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "~/.gramaton/hooks/session-start.sh"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/user/pre.sh"}]}
    ]
  }
}`
	mustWriteFile(t, settingsPath, initial)

	ownPaths := hookOwnershipPaths(filepath.Join(home, ".gramaton"))
	changed, backup, err := unregisterClaudeHooks(context.Background(), ownPaths, true)
	if err != nil {
		t.Fatalf("unregisterClaudeHooks: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if backup == "" {
		t.Fatal("expected a backup path")
	}
	if raw, err := os.ReadFile(backup); err != nil || string(raw) != initial {
		t.Errorf("backup should hold the original bytes (err=%v)", err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rewritten settings.json unparseable: %v", err)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Error("unrelated top-level key lost")
	}
	hooks := doc["hooks"].(map[string]any)
	if _, ok := hooks["SessionStart"]; ok {
		t.Error("SessionStart should be dropped entirely (the strip emptied it)")
	}
	stop, ok := hooks["Stop"].([]any)
	if !ok || len(stop) != 1 {
		t.Fatalf("Stop should retain exactly the user block, got %v", hooks["Stop"])
	}
	if !strings.Contains(string(raw), "/user/custom/stop.sh") {
		t.Error("user's Stop hook lost")
	}
	if strings.Contains(string(raw), ".gramaton") {
		t.Errorf("gramaton entries survived:\n%s", raw)
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok || len(pre) != 1 {
		t.Errorf("PreToolUse should be untouched, got %v", hooks["PreToolUse"])
	}

	// Second run: clean no-op -- no change, no write, no new backup.
	backupsBefore := countBackups(t, settingsPath)
	changed, backup2, err := unregisterClaudeHooks(context.Background(), ownPaths, true)
	if err != nil {
		t.Fatalf("second unregister: %v", err)
	}
	if changed || backup2 != "" {
		t.Errorf("second run should be a no-op, got changed=%v backup=%q", changed, backup2)
	}
	raw2, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(raw2) {
		t.Error("second run rewrote the file")
	}
	if countBackups(t, settingsPath) != backupsBefore {
		t.Error("second run wrote a new backup")
	}
}

// TestUnregisterClaudeHooksRelocatedConfigDir (#83): a hook command
// under a relocated config dir carries no `/.gramaton/hooks/`
// fragment; ownership must come from the synthesized paths for the
// ACTIVE config dir -- and a foreign dir must not match.
func TestUnregisterClaudeHooksRelocatedConfigDir(t *testing.T) {
	home := setUninstallTestEnv(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	relocated := filepath.Join(home, "custom-config")
	cmd := strings.ReplaceAll(filepath.Join(relocated, "hooks", "claude-code", "stop.sh"), `\`, "/")
	initial := fmt.Sprintf(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": %q}]}]}}`, cmd)
	mustWriteFile(t, settingsPath, initial)

	// Foreign config dir: must NOT claim the entry.
	changed, _, err := unregisterClaudeHooks(context.Background(), hookOwnershipPaths(filepath.Join(home, "other-config")), true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("foreign config dir must not match a relocated hook command")
	}

	// The active (relocated) config dir: must strip it.
	changed, _, err = unregisterClaudeHooks(context.Background(), hookOwnershipPaths(relocated), true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("relocated-config-dir hook was not recognized as ours")
	}
	raw, _ := os.ReadFile(settingsPath)
	if strings.Contains(string(raw), "custom-config") {
		t.Errorf("relocated hook entry survived:\n%s", raw)
	}
}

// TestUnregisterHooksUnparseableRefused: an unparseable settings.json
// is refused -- error, no rewrite, no backup.
func TestUnregisterHooksUnparseableRefused(t *testing.T) {
	home := setUninstallTestEnv(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	mustWriteFile(t, settingsPath, "{definitely not json")

	_, backup, err := unregisterClaudeHooks(context.Background(), hookOwnershipPaths(filepath.Join(home, ".gramaton")), true)
	if err == nil {
		t.Fatal("expected refusal for unparseable file")
	}
	if backup != "" {
		t.Error("no backup should be written for a refused file")
	}
	raw, _ := os.ReadFile(settingsPath)
	if string(raw) != "{definitely not json" {
		t.Error("refused file was modified")
	}
	if countBackups(t, settingsPath) != 0 {
		t.Error("refused file got a backup")
	}
}

// TestUnregisterHooksMissingFileNoOp: no config file, no error, no
// change -- absent is simply not-present.
func TestUnregisterHooksMissingFileNoOp(t *testing.T) {
	home := setUninstallTestEnv(t)
	changed, backup, err := unregisterClaudeHooks(context.Background(), hookOwnershipPaths(filepath.Join(home, ".gramaton")), true)
	if err != nil || changed || backup != "" {
		t.Errorf("missing file should be a clean no-op, got changed=%v backup=%q err=%v", changed, backup, err)
	}
}

// TestUnregisterCodexHooks: commandWindows-only entries are
// recognized as ours (proxyDualVariant), CODEX_HOME relocation is
// honored, and user entries survive.
func TestUnregisterCodexHooks(t *testing.T) {
	home := setUninstallTestEnv(t)
	codexHome := filepath.Join(home, "relocated-codex")
	t.Setenv("CODEX_HOME", codexHome)
	hooksPath := filepath.Join(codexHome, "hooks.json")
	initial := `{
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "commandWindows": "C:\\Users\\x\\.gramaton\\hooks\\codex\\stop.cmd"}]},
      {"hooks": [{"type": "command", "command": "/user/own-stop.sh"}]}
    ]
  }
}`
	mustWriteFile(t, hooksPath, initial)

	changed, backup, err := unregisterCodexHooks(context.Background(), hookOwnershipPaths(filepath.Join(home, ".gramaton")), true)
	if err != nil {
		t.Fatalf("unregisterCodexHooks: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if backup == "" {
		t.Error("expected a backup")
	}
	raw, _ := os.ReadFile(hooksPath)
	if strings.Contains(string(raw), ".gramaton") {
		t.Errorf("commandWindows gramaton entry survived:\n%s", raw)
	}
	if !strings.Contains(string(raw), "/user/own-stop.sh") {
		t.Errorf("user entry lost:\n%s", raw)
	}
}

// TestUnregisterCursorHooks: flat-shape entries; the event our strip
// empties is dropped, the mixed event keeps the user entry, and the
// version key survives.
func TestUnregisterCursorHooks(t *testing.T) {
	home := setUninstallTestEnv(t)
	hooksPath := filepath.Join(home, ".cursor", "hooks.json")
	gram := strings.ReplaceAll(filepath.Join(home, ".gramaton", "hooks", "cursor", "session-start.sh"), `\`, "/")
	initial := fmt.Sprintf(`{
  "version": 1,
  "hooks": {
    "sessionStart": [{"command": %q}, {"command": "/user/mine.sh"}],
    "stop": [{"command": "/home/x/.gramaton/hooks/cursor/stop.sh"}]
  }
}`, gram)
	mustWriteFile(t, hooksPath, initial)

	changed, _, err := unregisterCursorHooks(context.Background(), hookOwnershipPaths(filepath.Join(home, ".gramaton")), true)
	if err != nil {
		t.Fatalf("unregisterCursorHooks: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	raw, _ := os.ReadFile(hooksPath)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != float64(1) {
		t.Errorf("version key altered: %v", doc["version"])
	}
	hooks := doc["hooks"].(map[string]any)
	if _, ok := hooks["stop"]; ok {
		t.Error("emptied stop event should be dropped")
	}
	ss, ok := hooks["sessionStart"].([]any)
	if !ok || len(ss) != 1 {
		t.Fatalf("sessionStart should retain exactly the user entry, got %v", hooks["sessionStart"])
	}
	if !strings.Contains(string(raw), "/user/mine.sh") {
		t.Error("user entry lost")
	}
}

// --- fence strip ------------------------------------------------------

// TestStripFence pins the fence-strip semantics across marker
// versions, line-ending conventions, and corruption states.
func TestStripFence(t *testing.T) {
	body := "guidance body\n"
	versioned := instructionsFenceBegin() + "\n" + body + instructionsFenceEnd + "\n"
	unversioned := instructionsFenceBeginPrefix + " -->\n" + body + instructionsFenceEnd + "\n"

	tests := []struct {
		name      string
		input     string
		want      string
		wantFound bool
		wantErr   bool
	}{
		{name: "no fence", input: "just user content\n", want: "just user content\n", wantFound: false},
		{name: "fence only", input: versioned, want: "", wantFound: true},
		{name: "unversioned fence matched", input: unversioned, want: "", wantFound: true},
		{name: "user content then fence (LF)", input: "# Mine\n\ncontent\n\n" + versioned, want: "# Mine\n\ncontent\n", wantFound: true},
		{name: "user content then fence (CRLF terminator preserved)", input: "# Mine\r\n\n" + versioned, want: "# Mine\r\n", wantFound: true},
		{name: "fence mid-file, outside content byte-preserved", input: "before\n\n" + versioned + "after\n", want: "before\n\nafter\n", wantFound: true},
		{name: "CRLF file, outside content byte-preserved", input: "before\r\n" + instructionsFenceBegin() + "\r\nbody\r\n" + instructionsFenceEnd + "\r\nafter\r\n", want: "before\r\nafter\r\n", wantFound: true},
		{name: "unbalanced: BEGIN only", input: "x\n" + instructionsFenceBegin() + "\nbody\n", wantErr: true},
		{name: "unbalanced: END only", input: "x\n" + instructionsFenceEnd + "\n", wantErr: true},
		{name: "END before BEGIN", input: instructionsFenceEnd + "\n" + instructionsFenceBegin() + "\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found, err := stripFence([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("stripFence: %v", err)
			}
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			if string(got) != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- install -> uninstall round trip ---------------------------------

// TestInstallUninstallRoundTrip is the parity sibling of the
// integration drift test: for every registry harness, installing the
// guidance and then uninstalling must restore the pre-install state
// -- byte-identical original for fenced layouts installed into a
// file with pre-existing content, absence for wholeFileOwned
// layouts. Registry-driven, so a new harness is covered
// automatically.
func TestInstallUninstallRoundTrip(t *testing.T) {
	for i := range harnesses {
		h := &harnesses[i]
		if len(h.InstructionsRelPath) == 0 {
			continue
		}
		t.Run(h.Name, func(t *testing.T) {
			setUninstallTestEnv(t)
			path, layout, err := instructionsPathForClient(h.Name)
			if err != nil {
				t.Fatal(err)
			}

			original := "# My own notes\n\nkeep me intact\n"
			if layout == fencedBlockInSharedFile {
				mustWriteFile(t, path, original)
			}

			if _, err := installInstructions(path, installBodyForClient(h.Name), layout); err != nil {
				t.Fatalf("install: %v", err)
			}

			probe := probeInstructions(h)
			if !probe.present {
				t.Fatalf("probe should see the installed guidance (err=%v)", probe.err)
			}
			res := uninstallInstructions(h, probe, true)
			if res == nil || res.Outcome != UninstallRemoved {
				t.Fatalf("uninstall outcome = %+v, want removed", res)
			}

			if layout == fencedBlockInSharedFile {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read after round trip: %v", err)
				}
				if string(raw) != original {
					t.Errorf("round trip not byte-identical:\ngot:  %q\nwant: %q", raw, original)
				}
				if _, err := os.Stat(path + ".bak"); err != nil {
					t.Errorf(".bak sibling missing after fenced strip: %v", err)
				}
				return
			}

			// wholeFileOwned: the file must be gone.
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("owned guidance file should be deleted, stat err=%v", err)
			}
			if h.OwnsInstructionsDir {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Errorf("gramaton-dedicated dir should be deleted, stat err=%v", err)
				}
			} else {
				// Kiro: the shared steering dir must survive.
				if _, err := os.Stat(filepath.Dir(path)); err != nil {
					t.Errorf("shared parent dir should survive: %v", err)
				}
			}
		})
	}
}

// TestUninstallInstructionsFencedOnlyFileDeleted: a shared file that
// was created BY install (fence is its only content) is deleted on
// uninstall, with the .bak left behind for rollback.
func TestUninstallInstructionsFencedOnlyFileDeleted(t *testing.T) {
	setUninstallTestEnv(t)
	h := harnessByName(harnessClaudeCode)
	path, layout, err := instructionsPathForClient(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installInstructions(path, installBodyForClient(h.Name), layout); err != nil {
		t.Fatal(err)
	}

	res := uninstallInstructions(h, probeInstructions(h), true)
	if res == nil || res.Outcome != UninstallRemoved {
		t.Fatalf("outcome = %+v, want removed", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("fence-only file should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf(".bak should survive the delete: %v", err)
	}
}

// TestUninstallInstructionsAbsent: nothing installed -> not-present
// result, no error, nothing created.
func TestUninstallInstructionsAbsent(t *testing.T) {
	setUninstallTestEnv(t)
	for i := range harnesses {
		h := &harnesses[i]
		if len(h.InstructionsRelPath) == 0 {
			continue
		}
		probe := probeInstructions(h)
		if probe.err != nil {
			t.Fatalf("%s: probe err: %v", h.Name, probe.err)
		}
		res := uninstallInstructions(h, probe, true)
		if res == nil || res.Outcome != UninstallNotPresent {
			t.Errorf("%s: outcome = %+v, want not present", h.Name, res)
		}
	}
}

// TestUninstallInstructionsUnbalancedRefused: corrupted fence markers
// leave the file untouched and report a failure.
func TestUninstallInstructionsUnbalancedRefused(t *testing.T) {
	setUninstallTestEnv(t)
	h := harnessByName(harnessClaudeCode)
	path, _, err := instructionsPathForClient(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	content := "user\n" + instructionsFenceBegin() + "\nno end marker\n"
	mustWriteFile(t, path, content)

	probe := probeInstructions(h)
	res := uninstallInstructions(h, probe, true)
	if res == nil || res.Outcome != UninstallFailed {
		t.Fatalf("outcome = %+v, want failed", res)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != content {
		t.Error("refused file was modified")
	}
}

// --- engine ------------------------------------------------------------

func TestUninstallTargets(t *testing.T) {
	all, err := UninstallTargets("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(harnesses) {
		t.Errorf("empty slug should select all %d harnesses, got %d", len(harnesses), len(all))
	}

	one, err := UninstallTargets("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Name != harnessCodex {
		t.Errorf("codex slug resolved to %v", one)
	}

	_, err = UninstallTargets("bogus")
	if err == nil {
		t.Fatal("unknown slug should error")
	}
	for _, valid := range []string{"claude-code", "kiro", "codex", "cursor"} {
		if !strings.Contains(err.Error(), valid) {
			t.Errorf("error should list valid slug %q: %v", valid, err)
		}
	}
}

// TestHarnessRegistryUninstallInvariants pins the structural rules
// the uninstall engine leans on: uninstall coverage must track
// install coverage automatically as harnesses are added.
func TestHarnessRegistryUninstallInvariants(t *testing.T) {
	for _, h := range harnesses {
		if h.RegisterMCP != nil && h.ListMCPEntries == nil {
			t.Errorf("%s: RegisterMCP set but ListMCPEntries nil -- uninstall can't enumerate what install registers", h.Name)
		}
		if h.ListMCPEntries != nil {
			if h.RemoveMCPEntries == nil {
				t.Errorf("%s: ListMCPEntries set but RemoveMCPEntries nil", h.Name)
			}
			if h.ManualMCPRemoveHint == nil || h.ManualMCPRemoveHint("gramaton") == "" {
				t.Errorf("%s: ListMCPEntries set but ManualMCPRemoveHint missing/empty", h.Name)
			}
		}
		if h.WireHooks != nil && h.UnwireHooks == nil {
			t.Errorf("%s: WireHooks set but UnwireHooks nil -- uninstall can't strip what install wires", h.Name)
		}
		if h.OwnsInstructionsDir && h.InstructionsLayout != wholeFileOwned {
			t.Errorf("%s: OwnsInstructionsDir requires the wholeFileOwned layout", h.Name)
		}
	}
}

// buildFullFixture materializes gramaton artifacts for every harness
// under a fake home + config dir: hook-config entries (with user
// entries alongside), guidance files, rendered scripts, and Cursor's
// mcp.json. Returns the config dir.
func buildFullFixture(t *testing.T, home string) string {
	t.Helper()
	cfgDir := filepath.Join(home, ".gramaton")

	// Claude Code: settings.json + fenced CLAUDE.md.
	mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]},
      {"hooks": [{"type": "command", "command": "/home/x/.gramaton/hooks/claude-code/stop.sh"}]}
    ]
  }
}`)
	mustWriteFile(t, filepath.Join(home, ".claude", "CLAUDE.md"),
		"# user notes\n\n"+instructionsFenceBegin()+"\nguidance\n"+instructionsFenceEnd+"\n")

	// Codex: hooks.json + fenced AGENTS.md (default ~/.codex).
	mustWriteFile(t, filepath.Join(home, ".codex", "hooks.json"), `{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/home/x/.gramaton/hooks/codex/session-start.sh", "commandWindows": "C:\\x\\.gramaton\\hooks\\codex\\session-start.cmd"}]}
    ]
  }
}`)
	mustWriteFile(t, filepath.Join(home, ".codex", "AGENTS.md"),
		instructionsFenceBegin()+"\nguidance\n"+instructionsFenceEnd+"\n")

	// Cursor: mcp.json + hooks.json + owned SKILL.md.
	mustWriteFile(t, filepath.Join(home, ".cursor", "mcp.json"), cursorMCPFixture)
	mustWriteFile(t, filepath.Join(home, ".cursor", "hooks.json"),
		`{"version": 1, "hooks": {"sessionStart": [{"command": "/home/x/.gramaton/hooks/cursor/session-start.sh"}]}}`)
	mustWriteFile(t, filepath.Join(home, ".cursor", "skills", "gramaton", "SKILL.md"), "owned skill\n")

	// Kiro: owned steering file plus a user sibling that must survive.
	mustWriteFile(t, filepath.Join(home, ".kiro", "steering", "gramaton.md"), "owned steering\n")
	mustWriteFile(t, filepath.Join(home, ".kiro", "steering", "user-topic.md"), "user steering\n")

	// Rendered proxy scripts for every harness.
	for _, embedDir := range []string{"claude-code", "kiro", "codex", "cursor"} {
		mustWriteFile(t, filepath.Join(cfgDir, "hooks", embedDir, "stop.sh"), "#!/bin/bash\nexec gramaton hook stop\n")
	}
	return cfgDir
}

// TestUninstallInventoryMutatesNothing: the inventory (dry-run) pass
// over a fully-populated fixture must not change a single byte or
// directory anywhere under the fake home.
func TestUninstallInventoryMutatesNothing(t *testing.T) {
	home := setUninstallTestEnv(t)
	cfgDir := buildFullFixture(t, home)
	targets, err := UninstallTargets("")
	if err != nil {
		t.Fatal(err)
	}

	before := snapshotTree(t, home)
	_, results := UninstallInventory(context.Background(), cfgDir, targets)
	after := snapshotTree(t, home)

	if !reflect.DeepEqual(before, after) {
		t.Errorf("inventory pass mutated the filesystem:\nbefore: %v\nafter:  %v", before, after)
	}

	// The inventory must actually see the fixture: at least the hook
	// configs, scripts, guidance, and Cursor MCP entries.
	present := 0
	notes := 0
	for _, r := range results {
		switch r.Outcome {
		case UninstallPresent:
			present++
		case UninstallNote:
			notes++
		case UninstallSkipped, UninstallFailed:
			t.Errorf("unexpected skip/failure in inventory: %+v", r)
		}
	}
	// 3 hook configs + 4 script dirs + 4 guidance files + 2 cursor
	// MCP entries = 13 present surfaces.
	if present != 13 {
		t.Errorf("present surfaces = %d, want 13:\n%+v", present, results)
	}
	// claude, kiro, codex binaries are all missing -> 3 informational
	// cannot-check notes with manual hints (never skips or failures).
	if notes != 3 {
		t.Errorf("note surfaces = %d, want 3:\n%+v", notes, results)
	}
}

// TestUninstallApplyFullFixture: a full apply removes every gramaton
// artifact, preserves every user artifact, and a second run is a
// clean no-op that neither changes bytes nor writes backups.
func TestUninstallApplyFullFixture(t *testing.T) {
	home := setUninstallTestEnv(t)
	cfgDir := buildFullFixture(t, home)
	targets, err := UninstallTargets("")
	if err != nil {
		t.Fatal(err)
	}

	plan, _ := UninstallInventory(context.Background(), cfgDir, targets)
	results := UninstallApply(context.Background(), plan)
	for _, r := range results {
		if r.Outcome == UninstallFailed {
			t.Errorf("unexpected failure: %+v", r)
		}
	}

	// Gramaton artifacts gone; user artifacts intact.
	claudeSettings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(claudeSettings), ".gramaton") {
		t.Error("claude settings.json still has gramaton hooks")
	}
	if !strings.Contains(string(claudeSettings), "/user/custom/stop.sh") {
		t.Error("claude user hook lost")
	}
	claudeMD, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if string(claudeMD) != "# user notes\n" {
		t.Errorf("CLAUDE.md after strip = %q, want user notes only", claudeMD)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("AGENTS.md contained only the fence and should be deleted")
	}
	codexHooks, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if strings.Contains(string(codexHooks), ".gramaton") {
		t.Error("codex hooks.json still has gramaton hooks")
	}
	cursorMCP, _ := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if strings.Contains(string(cursorMCP), `"gramaton"`) || strings.Contains(string(cursorMCP), `"gramaton-work"`) {
		t.Error("cursor mcp.json still has gramaton entries")
	}
	if !strings.Contains(string(cursorMCP), "gramaton-x") || !strings.Contains(string(cursorMCP), "playwright") {
		t.Error("cursor mcp.json lost non-gramaton entries")
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "skills", "gramaton")); !os.IsNotExist(err) {
		t.Error("cursor skill dir should be deleted")
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "gramaton.md")); !os.IsNotExist(err) {
		t.Error("kiro steering file should be deleted")
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "user-topic.md")); err != nil {
		t.Error("kiro user steering sibling lost")
	}
	for _, embedDir := range []string{"claude-code", "kiro", "codex", "cursor"} {
		if _, err := os.Stat(filepath.Join(cfgDir, "hooks", embedDir)); !os.IsNotExist(err) {
			t.Errorf("script dir %s should be deleted", embedDir)
		}
	}

	// Second run: idempotent no-op. No removals, no failures, no
	// byte changes, no new backups.
	before := snapshotTree(t, home)
	plan2, _ := UninstallInventory(context.Background(), cfgDir, targets)
	second := UninstallApply(context.Background(), plan2)
	after := snapshotTree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Error("second apply mutated the filesystem")
	}
	for _, r := range second {
		if r.Outcome == UninstallRemoved || r.Outcome == UninstallFailed {
			t.Errorf("second apply should find nothing, got %+v", r)
		}
	}
}

// TestUninstallMCPBinaryMissingAlwaysNotes pins the missing-binary
// contract: whether or not the harness left any other gramaton
// footprint behind (the wizard allows registering MCP while
// declining hooks and guidance), a missing vendor binary ALWAYS
// yields an informational note carrying the exact manual command --
// never a skip or failure, in inventory and apply alike.
func TestUninstallMCPBinaryMissingAlwaysNotes(t *testing.T) {
	home := setUninstallTestEnv(t) // PATH is empty: no binaries anywhere
	targets, err := UninstallTargets("claude-code")
	if err != nil {
		t.Fatal(err)
	}

	assertNote := func(t *testing.T, results []UninstallResult) {
		t.Helper()
		var note *UninstallResult
		for i, r := range results {
			if r.Surface == "MCP entries" {
				note = &results[i]
			}
			if r.Outcome == UninstallSkipped || r.Outcome == UninstallFailed {
				t.Errorf("missing binary must never skip/fail, got %+v", r)
			}
		}
		if note == nil || note.Outcome != UninstallNote {
			t.Fatalf("want an MCP note result, got %+v", results)
		}
		if !strings.Contains(note.Detail, "cannot check MCP registration") ||
			!strings.Contains(note.Detail, "claude mcp remove --scope user") {
			t.Errorf("note detail should carry the cannot-check text and manual command: %q", note.Detail)
		}
	}

	// Bare machine: no footprint at all.
	cfgDir := filepath.Join(home, ".gramaton")
	plan, inv := UninstallInventory(context.Background(), cfgDir, targets)
	assertNote(t, inv)
	assertNote(t, UninstallApply(context.Background(), plan))

	// MCP-only-style footprint variants must not change the outcome.
	mustWriteFile(t, filepath.Join(cfgDir, "hooks", "claude-code", "stop.sh"), "#!/bin/bash\n")
	plan2, inv2 := UninstallInventory(context.Background(), cfgDir, targets)
	assertNote(t, inv2)
	assertNote(t, UninstallApply(context.Background(), plan2))
}

// TestUninstallEngineMCPWithFakeBinary drives the engine MCP surface
// end to end with a PATH-visible fake binary and the exec seam:
// inventory enumerates without removing; apply consumes the PLAN --
// exactly one `mcp list` across both phases (a second list would
// re-spawn claude's health-check proxies against the servers the
// stop step just shut down) -- and removes with the right argv.
func TestUninstallEngineMCPWithFakeBinary(t *testing.T) {
	setUninstallTestEnv(t)
	fakeBinaryOnPath(t, "claude")

	var calls [][]string
	swapHarnessCommand(t, func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		if len(args) >= 2 && args[1] == "list" {
			return "gramaton: gramaton mcp - ✓ Connected\n", nil
		}
		return "", nil
	})

	targets, err := UninstallTargets("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(t.TempDir(), "cfg")

	plan, inv := UninstallInventory(context.Background(), cfgDir, targets)
	foundEntry := false
	for _, r := range inv {
		if r.Outcome == UninstallPresent && strings.Contains(r.Surface, `MCP entry "gramaton"`) {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Fatalf("inventory should list the gramaton MCP entry: %+v", inv)
	}
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "remove" {
			t.Fatalf("inventory must not remove anything, saw %v", c)
		}
	}

	res := UninstallApply(context.Background(), plan)
	removedEntry := false
	for _, r := range res {
		if r.Outcome == UninstallRemoved && strings.Contains(r.Surface, `MCP entry "gramaton"`) {
			removedEntry = true
		}
	}
	if !removedEntry {
		t.Fatalf("apply should remove the gramaton MCP entry: %+v", res)
	}

	// Exactly ONE list (from inventory) and ONE remove (from apply)
	// across the whole flow: apply must reuse the plan's enumeration.
	var lists, removes [][]string
	for _, c := range calls {
		switch {
		case len(c) >= 2 && c[1] == "list":
			lists = append(lists, c)
		case len(c) >= 2 && c[1] == "remove":
			removes = append(removes, c)
		}
	}
	if len(lists) != 1 {
		t.Errorf("`mcp list` invoked %d times across inventory+apply, want exactly 1: %v", len(lists), calls)
	}
	if len(removes) != 1 || !reflect.DeepEqual(removes[0], []string{"mcp", "remove", "--scope", "user", "gramaton"}) {
		t.Errorf("apply should run `mcp remove --scope user gramaton` exactly once, got %v", removes)
	}
}

// TestKiroBestEffortRemoveFailureSkips pins the MCPBestEffort
// downgrade: kiro's MCP integration is parked, so a removal failure
// surfaces as an informative skip (manual hint included), never as a
// failure that would flip the exit code.
func TestKiroBestEffortRemoveFailureSkips(t *testing.T) {
	setUninstallTestEnv(t)
	fakeBinaryOnPath(t, "kiro")

	swapHarnessCommand(t, func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) >= 2 && args[1] == "list" {
			return "gramaton gramaton mcp\n", nil
		}
		return "Unknown command: remove", fmt.Errorf("exit status 2")
	})

	targets, err := UninstallTargets("kiro")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := UninstallInventory(context.Background(), filepath.Join(t.TempDir(), "cfg"), targets)
	results := UninstallApply(context.Background(), plan)

	sawSkip := false
	for _, r := range results {
		if r.Outcome == UninstallFailed {
			t.Errorf("best-effort kiro must not produce failures: %+v", r)
		}
		if r.Outcome == UninstallSkipped && strings.Contains(r.Surface, `MCP entry "gramaton"`) {
			sawSkip = true
			if !strings.Contains(r.Detail, "kiro mcp remove") {
				t.Errorf("skip detail should carry the manual hint: %q", r.Detail)
			}
		}
	}
	if !sawSkip {
		t.Fatalf("want a skipped MCP entry result, got %+v", results)
	}
}

// TestInstallUninstallRoundTripHooksAndMCP is the hooks-and-MCP
// counterpart of TestInstallUninstallRoundTrip: the REAL install
// paths (Materialize + register*Hooks + registerCursorEntry) run
// against fixture configs seeded with user entries, then a full
// engine uninstall must strip everything gramaton installed while
// preserving the user entries -- so the install patchers and the
// uninstall strips can never drift apart.
func TestInstallUninstallRoundTripHooksAndMCP(t *testing.T) {
	home := setUninstallTestEnv(t)
	cfgDir := filepath.Join(home, ".gramaton")

	// Pre-existing user entries in every hook config.
	mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "permissions": {"allow": ["thing"]},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]}]}
}`)
	mustWriteFile(t, filepath.Join(home, ".codex", "hooks.json"),
		`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/user/codex-stop.sh"}]}]}}`)
	mustWriteFile(t, filepath.Join(home, ".cursor", "hooks.json"),
		`{"version": 1, "hooks": {"stop": [{"command": "/user/cursor-stop.sh"}]}}`)
	mustWriteFile(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"other": {"type": "stdio", "command": "npx", "args": ["x"]}}}`)

	// Real install: materialize proxy scripts and wire them through
	// the actual register patchers; register Cursor MCP entries
	// through the actual registration path (default + per-store).
	backend := DefaultHookBackend{}
	for _, embedDir := range []string{"claude-code", "codex", "cursor"} {
		scripts, err := backend.Materialize(embedDir, cfgDir)
		if err != nil {
			t.Fatalf("Materialize(%s): %v", embedDir, err)
		}
		if _, err := backend.RegisterHooks(context.Background(), embedDir, scripts); err != nil {
			t.Fatalf("RegisterHooks(%s): %v", embedDir, err)
		}
	}
	if _, err := registerCursorEntry("gramaton", []string{"mcp"}); err != nil {
		t.Fatalf("registerCursorEntry: %v", err)
	}
	if _, err := registerCursorEntry(storeMCPEntryName("work"), storeMCPArgs("work")); err != nil {
		t.Fatalf("registerCursorEntry(store): %v", err)
	}

	// Sanity: install really landed.
	installed, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(string(installed), "hooks/claude-code") {
		t.Fatalf("install fixture broken; settings.json: %s", installed)
	}

	// Full uninstall over every harness.
	targets, err := UninstallTargets("")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := UninstallInventory(context.Background(), cfgDir, targets)
	results := UninstallApply(context.Background(), plan)
	for _, r := range results {
		if r.Outcome == UninstallFailed {
			t.Errorf("unexpected failure: %+v", r)
		}
	}

	// Hook configs: gramaton entries gone, user entries preserved.
	claude, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(claude), "hooks/claude-code") || strings.Contains(string(claude), ".gramaton") {
		t.Errorf("claude settings.json still has installed hooks:\n%s", claude)
	}
	if !strings.Contains(string(claude), "/user/custom/stop.sh") || !strings.Contains(string(claude), `"permissions"`) {
		t.Errorf("claude user content lost:\n%s", claude)
	}
	codex, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if strings.Contains(string(codex), "hooks/codex") {
		t.Errorf("codex hooks.json still has installed hooks:\n%s", codex)
	}
	if !strings.Contains(string(codex), "/user/codex-stop.sh") {
		t.Errorf("codex user hook lost:\n%s", codex)
	}
	cursor, _ := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
	if strings.Contains(string(cursor), "hooks/cursor") {
		t.Errorf("cursor hooks.json still has installed hooks:\n%s", cursor)
	}
	if !strings.Contains(string(cursor), "/user/cursor-stop.sh") {
		t.Errorf("cursor user hook lost:\n%s", cursor)
	}

	// Cursor MCP: both real-registered entries gone, user server kept.
	mcp, _ := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if strings.Contains(string(mcp), `"gramaton"`) || strings.Contains(string(mcp), `"gramaton-work"`) {
		t.Errorf("cursor mcp.json still has gramaton entries:\n%s", mcp)
	}
	if !strings.Contains(string(mcp), `"other"`) {
		t.Errorf("cursor mcp.json lost the user server:\n%s", mcp)
	}

	// Materialized scripts gone.
	for _, embedDir := range []string{"claude-code", "codex", "cursor"} {
		if _, err := os.Stat(filepath.Join(cfgDir, "hooks", embedDir)); !os.IsNotExist(err) {
			t.Errorf("script dir %s should be deleted", embedDir)
		}
	}
}

// TestUninstallCodexRelocatedConfigRoot: with CODEX_HOME pointing at
// a relocated root, both the AGENTS.md fence strip and the
// hooks.json unwire must operate on the relocated files -- the same
// ConfigRootEnv resolution install uses.
func TestUninstallCodexRelocatedConfigRoot(t *testing.T) {
	home := setUninstallTestEnv(t)
	codexHome := filepath.Join(home, "relocated-codex")
	t.Setenv("CODEX_HOME", codexHome)

	mustWriteFile(t, filepath.Join(codexHome, "AGENTS.md"),
		"# codex user notes\n\n"+instructionsFenceBegin()+"\nguidance\n"+instructionsFenceEnd+"\n")
	mustWriteFile(t, filepath.Join(codexHome, "hooks.json"), `{
  "hooks": {"Stop": [
    {"hooks": [{"type": "command", "command": "/home/x/.gramaton/hooks/codex/stop.sh"}]},
    {"hooks": [{"type": "command", "command": "/user/keep.sh"}]}
  ]}
}`)

	targets, err := UninstallTargets("codex")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := UninstallInventory(context.Background(), filepath.Join(home, ".gramaton"), targets)
	results := UninstallApply(context.Background(), plan)
	for _, r := range results {
		if r.Outcome == UninstallFailed {
			t.Errorf("unexpected failure: %+v", r)
		}
	}

	agents, err := os.ReadFile(filepath.Join(codexHome, "AGENTS.md"))
	if err != nil {
		t.Fatalf("relocated AGENTS.md unreadable after strip: %v", err)
	}
	if string(agents) != "# codex user notes\n" {
		t.Errorf("relocated AGENTS.md after strip = %q, want the user notes only", agents)
	}
	hooks, _ := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if strings.Contains(string(hooks), ".gramaton") {
		t.Errorf("relocated hooks.json still has gramaton hooks:\n%s", hooks)
	}
	if !strings.Contains(string(hooks), "/user/keep.sh") {
		t.Errorf("relocated hooks.json lost the user hook:\n%s", hooks)
	}
}

// TestUninstallCodexRelativeConfigRootRefused: a relative CODEX_HOME
// is refused with failed surfaces (same absolute-path guard as
// install), never a panic or a write anywhere.
func TestUninstallCodexRelativeConfigRootRefused(t *testing.T) {
	setUninstallTestEnv(t)
	t.Setenv("CODEX_HOME", "relative/codex")

	targets, err := UninstallTargets("codex")
	if err != nil {
		t.Fatal(err)
	}
	plan, inv := UninstallInventory(context.Background(), filepath.Join(t.TempDir(), "cfg"), targets)

	assertRefused := func(t *testing.T, results []UninstallResult) {
		t.Helper()
		failed := 0
		for _, r := range results {
			if r.Outcome == UninstallFailed {
				failed++
				if !strings.Contains(r.Detail, "absolute path") {
					t.Errorf("failure detail should mention the absolute-path guard: %q", r.Detail)
				}
			}
		}
		// Both CODEX_HOME-resolved surfaces refuse: hook config and
		// agent guidance.
		if failed != 2 {
			t.Errorf("failed surfaces = %d, want 2 (hooks.json + AGENTS.md):\n%+v", failed, results)
		}
	}
	assertRefused(t, inv)
	assertRefused(t, UninstallApply(context.Background(), plan))
}

// TestUninstallFencedBackupFailureAborts (MINOR: backup-write parity)
// -- when the .bak sibling cannot be written, the fenced strip must
// abort the surface and leave the original untouched.
func TestUninstallFencedBackupFailureAborts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit test; NTFS ACL model differs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}
	setUninstallTestEnv(t)
	h := harnessByName(harnessClaudeCode)
	path, _, err := instructionsPathForClient(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	content := "# user notes\n\n" + instructionsFenceBegin() + "\nguidance\n" + instructionsFenceEnd + "\n"
	mustWriteFile(t, path, content)

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil { // read+exec: .bak creation fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	res := uninstallInstructions(h, probeInstructions(h), true)
	if res == nil || res.Outcome != UninstallFailed {
		t.Fatalf("outcome = %+v, want failed when the backup can't be written", res)
	}
	if !strings.Contains(res.Detail, "backup") {
		t.Errorf("failure detail should mention the backup: %q", res.Detail)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != content {
		t.Error("original was modified despite the aborted backup")
	}
}
