package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// setupUninstallCLITest isolates a test from the real user
// environment: fake home, empty PATH (so no real harness binary is
// found -- the engine would otherwise shell out to it), a throwaway
// config dir wired through the --config-dir global, and reset
// flag/seam state. Returns the fake home.
func setupUninstallCLITest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows: os.UserHomeDir reads %USERPROFILE%, not $HOME
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("GRAMATON_STORE", "")

	origCfgDir := cfgDir
	cfgDir = filepath.Join(home, ".gramaton")

	origHarness, origDryRun, origYes := uninstallHarnessFlag, uninstallDryRun, uninstallYes
	origTTY, origInput := uninstallStdinIsTTY, uninstallInput
	origReap, origStop := uninstallReapProxies, uninstallStopServer
	t.Cleanup(func() {
		cfgDir = origCfgDir
		uninstallHarnessFlag, uninstallDryRun, uninstallYes = origHarness, origDryRun, origYes
		uninstallStdinIsTTY, uninstallInput = origTTY, origInput
		uninstallReapProxies, uninstallStopServer = origReap, origStop
	})
	uninstallHarnessFlag, uninstallDryRun, uninstallYes = "", false, false
	return home
}

func writeUninstallFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildUninstallCLIFixture seeds Claude Code and Cursor artifacts:
// a settings.json mixing user and gramaton hooks, a fenced
// CLAUDE.md, rendered proxy scripts, and a Cursor mcp.json with a
// gramaton entry.
func buildUninstallCLIFixture(t *testing.T, home string) {
	t.Helper()
	writeUninstallFixtureFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "/user/custom/stop.sh"}]},
      {"hooks": [{"type": "command", "command": "/home/x/.gramaton/hooks/claude-code/stop.sh"}]}
    ]
  }
}`)
	writeUninstallFixtureFile(t, filepath.Join(home, ".claude", "CLAUDE.md"),
		"# user notes\n\n<!-- BEGIN gramaton-managed v=0.0.1 (test) -->\nguidance\n<!-- END gramaton-managed -->\n")
	writeUninstallFixtureFile(t, filepath.Join(home, ".gramaton", "hooks", "claude-code", "stop.sh"),
		"#!/bin/bash\nexec gramaton hook stop\n")
	writeUninstallFixtureFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{
  "mcpServers": {
    "gramaton": {"type": "stdio", "command": "gramaton", "args": ["mcp"]},
    "other": {"type": "stdio", "command": "npx", "args": ["something"]}
  }
}`)
}

// snapshotHome captures every file and directory under home for
// untouched-filesystem assertions.
func snapshotHome(t *testing.T, home string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(home, path)
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
		t.Fatal(err)
	}
	return snap
}

// TestUninstallNonTTYWithoutYesErrors: piped stdin without --yes must
// abort with an error naming the flag, touching nothing.
func TestUninstallNonTTYWithoutYesErrors(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallStdinIsTTY = func() bool { return false }

	before := snapshotHome(t, home)
	var out bytes.Buffer
	err := runUninstall(context.Background(), &out)
	if err == nil {
		t.Fatal("expected an error on non-TTY stdin without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should point at --yes: %v", err)
	}
	if after := snapshotHome(t, home); !reflect.DeepEqual(before, after) {
		t.Error("filesystem changed despite aborting for missing confirmation")
	}
}

// TestUninstallAbortOnNo: answering "n" (and the default empty
// answer) aborts cleanly -- no error, nothing changed, and the
// always-printed data-location footer still shows.
func TestUninstallAbortOnNo(t *testing.T) {
	for _, answer := range []string{"n\n", "\n"} {
		t.Run(fmt.Sprintf("answer %q", strings.TrimSpace(answer)), func(t *testing.T) {
			home := setupUninstallCLITest(t)
			buildUninstallCLIFixture(t, home)
			uninstallStdinIsTTY = func() bool { return true }
			uninstallInput = strings.NewReader(answer)

			before := snapshotHome(t, home)
			var out bytes.Buffer
			if err := runUninstall(context.Background(), &out); err != nil {
				t.Fatalf("abort should not error: %v", err)
			}
			if !strings.Contains(out.String(), "Aborted") {
				t.Errorf("output should say aborted:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "Gramaton data and configuration remain at") {
				t.Errorf("abort must still print the data-location footer:\n%s", out.String())
			}
			if after := snapshotHome(t, home); !reflect.DeepEqual(before, after) {
				t.Error("filesystem changed despite user answering no")
			}
		})
	}
}

// TestUninstallYesProceeds: --yes skips the prompt and removes the
// gramaton artifacts while preserving user content, then reports
// where the data remains.
func TestUninstallYesProceeds(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallYes = true
	uninstallStdinIsTTY = func() bool { return false } // --yes must not need a TTY

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settings), ".gramaton") {
		t.Error("gramaton hook entry survived")
	}
	if !strings.Contains(string(settings), "/user/custom/stop.sh") {
		t.Error("user hook entry lost")
	}
	claudeMD, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(claudeMD) != "# user notes\n" {
		t.Errorf("CLAUDE.md = %q, want the user notes only", claudeMD)
	}
	if _, err := os.Stat(filepath.Join(home, ".gramaton", "hooks", "claude-code")); !os.IsNotExist(err) {
		t.Error("rendered hook scripts should be deleted")
	}
	mcp, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mcp), `"gramaton"`) {
		t.Error("cursor gramaton MCP entry survived")
	}
	if !strings.Contains(string(mcp), `"other"`) {
		t.Error("cursor non-gramaton MCP entry lost")
	}

	if !strings.Contains(out.String(), "Gramaton data and configuration remain at") {
		t.Errorf("output should name the surviving data directory:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "binary is not removed") {
		t.Errorf("output should carry the binary-removal hint:\n%s", out.String())
	}
}

// TestUninstallDryRunMutatesNothing: --dry-run prints the inventory,
// requires no confirmation, exits clean, and changes nothing.
func TestUninstallDryRunMutatesNothing(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallDryRun = true
	uninstallStdinIsTTY = func() bool { return false } // no prompt in dry-run

	before := snapshotHome(t, home)
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("dry run should exit clean: %v", err)
	}
	if after := snapshotHome(t, home); !reflect.DeepEqual(before, after) {
		t.Error("dry run mutated the filesystem")
	}
	s := out.String()
	if !strings.Contains(s, "Dry run") {
		t.Errorf("output should say dry run:\n%s", s)
	}
	// The inventory must name the concrete surfaces.
	for _, want := range []string{"hook registrations", "hook scripts", "managed guidance block", `MCP entry "gramaton"`} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run inventory missing %q:\n%s", want, s)
		}
	}
	// The active-session warning is part of the explanation contract:
	// users must learn their running harness sessions will lose the
	// MCP connection and need a restart.
	for _, want := range []string{"will lose their Gramaton MCP connection", "restarted"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing the session-restart warning (%q):\n%s", want, s)
		}
	}
	if strings.Contains(s, "Proceed?") {
		t.Errorf("dry run must not prompt:\n%s", s)
	}
}

// TestUninstallNothingToRemove: a machine with no integrations at all
// reports "nothing to remove" and exits clean without prompting. The
// cannot-check MCP notes for missing harness binaries still print --
// they are informational and must neither suppress the headline nor
// flip the exit code.
func TestUninstallNothingToRemove(t *testing.T) {
	setupUninstallCLITest(t)
	uninstallStdinIsTTY = func() bool { return false } // would error if a prompt were required

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("nothing-to-remove should exit clean: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Errorf("output should say nothing to remove:\n%s", out.String())
	}
	// PATH is empty, so every vendor-CLI harness gets the note.
	if !strings.Contains(out.String(), "cannot check MCP registration (claude not on PATH)") {
		t.Errorf("missing-binary note should always print:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "claude mcp remove --scope user") {
		t.Errorf("note should carry the exact manual command:\n%s", out.String())
	}
}

// TestUninstallHarnessScoping: --harness cursor removes only Cursor's
// artifacts and leaves the Claude Code ones byte-identical.
func TestUninstallHarnessScoping(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallHarnessFlag = "cursor"
	uninstallYes = true

	claudeSettingsPath := filepath.Join(home, ".claude", "settings.json")
	claudeMDPath := filepath.Join(home, ".claude", "CLAUDE.md")
	settingsBefore, _ := os.ReadFile(claudeSettingsPath)
	mdBefore, _ := os.ReadFile(claudeMDPath)

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	mcp, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mcp), `"gramaton"`) {
		t.Error("cursor gramaton MCP entry should be removed")
	}

	settingsAfter, _ := os.ReadFile(claudeSettingsPath)
	mdAfter, _ := os.ReadFile(claudeMDPath)
	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Error("--harness cursor must not touch claude settings.json")
	}
	if !bytes.Equal(mdBefore, mdAfter) {
		t.Error("--harness cursor must not touch CLAUDE.md")
	}
	if _, err := os.Stat(filepath.Join(home, ".gramaton", "hooks", "claude-code")); err != nil {
		t.Error("--harness cursor must not delete claude-code scripts")
	}
}

// TestUninstallInvalidHarnessErrors: an unknown --harness value
// errors up front, listing the valid slugs, before any prompt.
func TestUninstallInvalidHarnessErrors(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallHarnessFlag = "vscode"

	before := snapshotHome(t, home)
	var out bytes.Buffer
	err := runUninstall(context.Background(), &out)
	if err == nil {
		t.Fatal("expected an error for an unknown harness")
	}
	for _, want := range []string{"claude-code", "kiro", "codex", "cursor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list valid slug %q: %v", want, err)
		}
	}
	if after := snapshotHome(t, home); !reflect.DeepEqual(before, after) {
		t.Error("invalid flag value must not change anything")
	}
}

// TestUninstallSecondRunNothingToRemove: after a successful
// uninstall, a re-run is a clean no-op ending at "nothing to
// remove" -- the informational cannot-check notes for missing
// binaries may still print but never suppress the headline or
// change bytes on disk.
func TestUninstallSecondRunNothingToRemove(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	uninstallYes = true

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("first run: %v", err)
	}

	before := snapshotHome(t, home)
	var out2 bytes.Buffer
	if err := runUninstall(context.Background(), &out2); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out2.String(), "Nothing to remove") {
		t.Errorf("second run should find nothing:\n%s", out2.String())
	}
	if after := snapshotHome(t, home); !reflect.DeepEqual(before, after) {
		t.Error("second run mutated the filesystem")
	}
}

// TestUninstallIgnoresStoreScoping: harness integrations are
// machine-level, so uninstall must resolve every surface from the
// BASE config dir even when GRAMATON_STORE points at a named store.
// Regression test for the config-dir drift where a set store made
// hook-script deletion resolve to stores/<name>/hooks/ and silently
// miss the real installs.
func TestUninstallIgnoresStoreScoping(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	t.Setenv("GRAMATON_STORE", "somestore")
	uninstallYes = true

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gramaton", "hooks", "claude-code")); !os.IsNotExist(err) {
		t.Error("base-config-dir hook scripts were missed with GRAMATON_STORE set")
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(settings), ".gramaton") {
		t.Error("gramaton hook entry survived with GRAMATON_STORE set")
	}
}

// TestUninstallPartialFailureExitsNonzero: one broken surface (an
// unparseable cursor mcp.json) must not strand the rest -- the other
// harnesses' surfaces are still removed -- but the run reports the
// failure and exits non-zero.
func TestUninstallPartialFailureExitsNonzero(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	badMCP := filepath.Join(home, ".cursor", "mcp.json")
	writeUninstallFixtureFile(t, badMCP, "{definitely not json")
	uninstallYes = true

	var out bytes.Buffer
	err := runUninstall(context.Background(), &out)
	if err == nil {
		t.Fatal("expected a failure exit for the refused mcp.json")
	}
	if !strings.Contains(err.Error(), "failure") {
		t.Errorf("error should summarize failures: %v", err)
	}

	// The refused file is untouched...
	raw, _ := os.ReadFile(badMCP)
	if string(raw) != "{definitely not json" {
		t.Error("refused mcp.json was modified")
	}
	// ...while the other surfaces still completed.
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(settings), ".gramaton") {
		t.Error("claude surface should still complete despite the cursor failure")
	}
	if _, err := os.Stat(filepath.Join(home, ".gramaton", "hooks", "claude-code")); !os.IsNotExist(err) {
		t.Error("hook scripts should still be deleted despite the cursor failure")
	}
	if !strings.Contains(out.String(), "✗") {
		t.Errorf("report should mark the failed surface:\n%s", out.String())
	}
}

// seedRunningStore fabricates a store with a live-looking server:
// a data dir (so store.List sees it) plus a server.json recording
// this test process's own PID (alive by construction). The stop
// seams are faked, so nothing is ever signalled for real.
func seedRunningStore(t *testing.T, base string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`{"pid": %d, "port": 65535, "bind": "127.0.0.1", "version": "test"}`, os.Getpid())
	writeUninstallFixtureFile(t, filepath.Join(base, "server.json"), info)
}

// TestUninstallStopsProxiesBeforeServer pins the load-bearing stop
// order through the seams: for each store, the MCP-proxy reap runs
// BEFORE the server shutdown (a surviving proxy auto-starts a
// replacement server on its next tool call).
func TestUninstallStopsProxiesBeforeServer(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	base := filepath.Join(home, ".gramaton")
	seedRunningStore(t, base)

	var seq []string
	uninstallReapProxies = func(dir string, _ io.Writer) (int, int) {
		seq = append(seq, "reap:"+dir)
		return 1, 0
	}
	uninstallStopServer = func(dir string) error {
		seq = append(seq, "stop:"+dir)
		return nil
	}
	uninstallYes = true

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}
	want := []string{"reap:" + base, "stop:" + base}
	if !reflect.DeepEqual(seq, want) {
		t.Errorf("stop sequence = %v, want %v (proxies reaped first)", seq, want)
	}
}

// TestUninstallStopFailuresExitNonzero: a server that cannot be
// stopped, and gramaton proxies that survive the kill escalation,
// both count toward the failure total and flip the exit code.
func TestUninstallStopFailuresExitNonzero(t *testing.T) {
	home := setupUninstallCLITest(t)
	buildUninstallCLIFixture(t, home)
	base := filepath.Join(home, ".gramaton")
	seedRunningStore(t, base)

	uninstallReapProxies = func(_ string, _ io.Writer) (int, int) {
		return 0, 1 // one proxy survived SIGKILL escalation
	}
	uninstallStopServer = func(_ string) error {
		return fmt.Errorf("shutdown request failed: connection refused")
	}
	uninstallYes = true

	var out bytes.Buffer
	err := runUninstall(context.Background(), &out)
	if err == nil {
		t.Fatal("stop failures must exit non-zero")
	}
	if !strings.Contains(err.Error(), "2 failure") {
		t.Errorf("both the surviving proxy and the failed stop should count: %v", err)
	}
	if !strings.Contains(out.String(), "did not exit") {
		t.Errorf("report should name the surviving proxy:\n%s", out.String())
	}
}

// TestSanitizeTerminal pins the control-character stripping applied
// to strings sourced from harness configs before printing.
func TestSanitizeTerminal(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"a\x1b[31mred\x1b[0mb", "aredb"},
		{"osc\x1b]0;title\x07done", "oscdone"},
		{"bare\x1besc", "baresc"},
		{"tab\tand\nnewline", "tabandnewline"},
		{"del\x7fchar", "delchar"},
		{"unicode ✓ passes", "unicode ✓ passes"},
	}
	for _, tt := range tests {
		if got := sanitizeTerminal(tt.in); got != tt.want {
			t.Errorf("sanitizeTerminal(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestUninstallSanitizesConfigStrings: an ANSI-escape-bearing entry
// name planted in cursor's mcp.json must never reach the terminal
// raw -- no ESC byte anywhere in the output.
func TestUninstallSanitizesConfigStrings(t *testing.T) {
	home := setupUninstallCLITest(t)
	writeUninstallFixtureFile(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"gramaton-\u001b[31mevil": {"type": "stdio", "command": "gramaton", "args": ["mcp"]}}}`)
	uninstallDryRun = true

	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Errorf("raw ESC byte reached the terminal output:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "gramaton-") {
		t.Errorf("sanitized entry name should still be recognizable:\n%s", out.String())
	}
}
