package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Hook scripts for automatic-capture integration with MCP clients.
//
// As of Phase 2 of the Windows platform-support plan these are thin
// proxy files that forward stdin to `gramaton hook <event>` and exit
// with the subcommand's status. All real hook logic lives in the
// hooks/ Go package (hooks/claude_code.go, hooks/kiro.go). This
// simplification eliminates the embed_hooks/ tree duplication and
// removes the hidden python3 dependency the old shell scripts
// carried.
//
// Scripts are synthesized at init time from Go templates, so there's
// no need to check proxy files into the repo. Line endings are
// explicit (LF for .sh, CRLF for .cmd) so git's core.autocrlf can't
// corrupt the shebang.

// hookEventSpec describes one hook event for a given client.
// cliEvent is the positional arg passed to `gramaton hook`;
// fileBase is the proxy filename without extension;
// claudeEvent is the Claude Code settings.json event key (empty
// for clients where we don't hand-edit settings.json, e.g. Kiro).
type hookEventSpec struct {
	cliEvent    string
	fileBase    string
	claudeEvent string
}

// claudeCodeEvents are the four lifecycle events Gramaton wires
// into Claude Code. Order is stable for deterministic Materialize
// output and test assertions.
var claudeCodeEvents = []hookEventSpec{
	{cliEvent: "session-start", fileBase: "session-start", claudeEvent: "SessionStart"},
	{cliEvent: "stop", fileBase: "stop", claudeEvent: "Stop"},
	{cliEvent: "pre-compact", fileBase: "pre-compact", claudeEvent: "PreCompact"},
	{cliEvent: "post-compact", fileBase: "post-compact", claudeEvent: "PostCompact"},
}

// kiroEvents are the three lifecycle events Gramaton wires into
// Kiro. The cliEvent is prefixed with "kiro-" so `gramaton hook`
// can disambiguate agent-spawn from a hypothetical future
// Claude-Code-named event with the same short name.
var kiroEvents = []hookEventSpec{
	{cliEvent: "kiro-agent-spawn", fileBase: "agent-spawn"},
	{cliEvent: "kiro-user-prompt-submit", fileBase: "user-prompt-submit"},
	{cliEvent: "kiro-stop", fileBase: "stop"},
}

// eventsForClient returns the spec list for a known client name.
// Returns nil for unknown clients so Materialize reports a
// specific error to the wizard caller.
func eventsForClient(client string) []hookEventSpec {
	switch client {
	case "claude-code":
		return claudeCodeEvents
	case "kiro":
		return kiroEvents
	}
	return nil
}

// renderHookProxy synthesizes the proxy script for an event.
// Returns the filename (including extension) and the file body.
//
// Matrix:
//
//	claude-code on any OS  → .sh   (Claude Code bundles Git Bash on Windows)
//	kiro on non-Windows    → .sh
//	kiro on Windows        → .cmd  (Kiro CLI 2.0 is native Windows, no bash)
//
// Line endings are chosen deliberately: LF for .sh so bash can
// read the shebang intact (CRLF-prefixed `#!/bin/bash\r` makes
// bash look for a binary named `bash\r` and fail); CRLF for .cmd
// so cmd.exe treats the file as a native batch script.
func renderHookProxy(client, cliEvent, fileBase string) (filename, body string) {
	if client == "kiro" && runtime.GOOS == "windows" {
		return fileBase + ".cmd", fmt.Sprintf("@gramaton hook %s\r\n", cliEvent)
	}
	return fileBase + ".sh", fmt.Sprintf("#!/bin/bash\nexec gramaton hook %s\n", cliEvent)
}

// HookBackend is the test seam for Step 4. Production uses
// DefaultHookBackend; tests inject a fake to exercise the wizard
// orchestration without touching the real filesystem or the user's
// Claude Code settings.json.
type HookBackend interface {
	// Materialize generates the proxy scripts for `client` into a
	// canonical on-disk location (typically
	// ~/.gramaton/hooks/<client>/) and returns the absolute paths of
	// the installed scripts. Idempotent: re-running overwrites with
	// the current proxy template so upgrades propagate when users
	// re-run the wizard.
	Materialize(client string, configDir string) (scriptPaths []string, err error)

	// RegisterClaudeHooks patches ~/.claude/settings.json to point
	// the user-scope hooks at the given script paths. Existing
	// gramaton-owned hook entries (commands under
	// ~/.gramaton/hooks/) are replaced in place; other hook entries
	// (user's own, other tools') are left untouched. Returns (true,
	// nil) when our entries were already present and unchanged,
	// (false, nil) on a successful update, (false, err) on failure.
	RegisterClaudeHooks(ctx context.Context, scriptPaths []string) (unchanged bool, err error)
}

// DefaultHookBackend is the production implementation.
type DefaultHookBackend struct{}

// Materialize writes the proxy scripts for `client` into
// <configDir>/hooks/<client>/. configDir is typically ~/.gramaton.
// Creates directories with 0o700 and scripts with 0o755. The
// exec bit is ignored on Windows but preserved on Unix.
//
// Pre-Phase-2 this function extracted real hook logic from
// embedded .sh files; as of Phase 2 the scripts are one-line
// proxies generated from Go templates. User customizations of
// proxy files are overwritten on re-run — documented in the
// wizard output.
func (DefaultHookBackend) Materialize(client string, configDir string) ([]string, error) {
	events := eventsForClient(client)
	if events == nil {
		return nil, fmt.Errorf("no hooks defined for client %q", client)
	}

	destDir := filepath.Join(configDir, "hooks", client)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	var paths []string
	for _, ev := range events {
		filename, body := renderHookProxy(client, ev.cliEvent, ev.fileBase)
		destPath := filepath.Join(destDir, filename)
		if err := os.WriteFile(destPath, []byte(body), 0o755); err != nil {
			return nil, fmt.Errorf("write %s: %w", destPath, err)
		}
		paths = append(paths, destPath)
	}
	// Deterministic order (os.WriteFile doesn't impose one); makes
	// test assertions stable.
	sort.Strings(paths)
	return paths, nil
}

// hookEventForClaude maps a proxy filename to the Claude Code event
// name that triggers it. Accepts both .sh and .cmd extensions so
// the function is robust to any future cross-platform variations.
// Unknown filenames return "" — RegisterClaudeHooks skips them.
func hookEventForClaude(scriptName string) string {
	base := strings.TrimSuffix(scriptName, ".cmd")
	base = strings.TrimSuffix(base, ".sh")
	for _, ev := range claudeCodeEvents {
		if base == ev.fileBase {
			return ev.claudeEvent
		}
	}
	return ""
}

// RegisterClaudeHooks patches the user-scope ~/.claude/settings.json
// to route hook events at our materialized proxy scripts. The merge
// is additive and surgical: we preserve every top-level key, every
// unrelated hook-event entry, and every hook command that isn't
// pointing at ~/.gramaton/hooks/. Our own entries are replaced in
// place so re-running the wizard is idempotent.
//
// Why direct JSON editing (vs shelling out to some hypothetical
// `claude hooks add` command): Claude Code does NOT currently have a
// `hooks` CLI subcommand (verified 2026-04-22: `claude hooks` treats
// the input as a prompt for the agent, not a subcommand). Hand-
// editing settings.json is the only programmatic path.
//
// Schema source: verified against the user's actual ~/.claude/
// settings.json at the time of writing. Each event maps to an
// array of "matcher blocks", each with a `hooks` array of
// {"type": "command", "command": "..."} entries. We write a single
// block per event with one entry, pointing at our materialized
// script.
func (DefaultHookBackend) RegisterClaudeHooks(_ context.Context, scriptPaths []string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("resolve home dir: %w", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings.json (may not exist -- first-time Claude
	// Code users). We start with an empty map if missing.
	existing := map[string]any{}
	if raw, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return false, fmt.Errorf("parse settings.json: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read settings.json: %w", err)
	}

	// Build the set of entries we want, keyed by event name.
	// Paths are converted to forward slashes so Claude Code's
	// bundled Git Bash (which runs hook commands on Windows)
	// doesn't treat the backslashes in a native Windows path as
	// escape characters. `C:\Users\op\.gramaton\...` would reach
	// bash as `C:Usersop.gramaton...` with the \U, \o, \. all
	// silently stripped, and `bash: ... No such file or
	// directory` results. Git Bash accepts forward-slashed
	// Windows paths (`C:/Users/op/.gramaton/...`) natively.
	//
	// strings.ReplaceAll (not filepath.ToSlash) because ToSlash is
	// a no-op on Unix and we want the behavior to be testable on
	// any OS — Unix paths never contain backslashes, so the
	// ReplaceAll is a no-op there too.
	wanted := map[string]string{}
	for _, p := range scriptPaths {
		event := hookEventForClaude(filepath.Base(p))
		if event == "" {
			continue
		}
		wanted[event] = strings.ReplaceAll(p, `\`, "/")
	}
	if len(wanted) == 0 {
		return true, nil // nothing to register; technically unchanged
	}

	// Get or create the "hooks" sub-object.
	hooksObj, _ := existing["hooks"].(map[string]any)
	if hooksObj == nil {
		hooksObj = map[string]any{}
	}

	// Check whether our desired state already matches current state,
	// so we can return `unchanged` without rewriting the file (avoids
	// pointless mtime bumps + lets the wizard report a meaningful
	// "already registered").
	changed := false

	for event, ourScript := range wanted {
		// Pull existing matcher blocks for this event (Claude's
		// schema is an array of objects, each with a "hooks" array).
		var blocks []any
		if raw, ok := hooksObj[event]; ok {
			if arr, isArr := raw.([]any); isArr {
				blocks = arr
			}
		}

		// Filter out any existing gramaton-owned command entries.
		// Match by command-string path containing /.gramaton/hooks/
		// (tilde-prefixed or fully-expanded). This catches both the
		// new subdir layout (~/.gramaton/hooks/claude-code/*.sh) and
		// the legacy flat layout (~/.gramaton/hooks/*.sh) that a
		// user may have from an earlier manual setup.
		kept := make([]any, 0, len(blocks))
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				kept = append(kept, b)
				continue
			}
			inner, _ := bm["hooks"].([]any)
			newInner := make([]any, 0, len(inner))
			for _, h := range inner {
				hm, ok := h.(map[string]any)
				if !ok {
					newInner = append(newInner, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if isGramatonHookCommand(cmd) {
					continue // drop this entry; we'll add our canonical one
				}
				newInner = append(newInner, h)
			}
			if len(newInner) > 0 {
				bm["hooks"] = newInner
				kept = append(kept, bm)
			}
		}

		// Add our canonical block.
		kept = append(kept, map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": ourScript,
				},
			},
		})

		// Idempotency: compare the fully-rebuilt `kept` slice against
		// the original `blocks` via JSON serialization. The only
		// authoritative answer to "did this call change anything" is
		// "is the final JSON different from the initial JSON?".
		// Stripping+re-adding an identical entry must report
		// unchanged so re-runs don't rewrite the file pointlessly.
		oldJSON, _ := json.Marshal(blocks)
		newJSON, _ := json.Marshal(kept)
		if string(oldJSON) != string(newJSON) {
			changed = true
		}
		hooksObj[event] = kept
	}

	if !changed {
		return true, nil
	}

	existing["hooks"] = hooksObj

	// Back up existing settings.json before writing.
	if _, err := os.Stat(settingsPath); err == nil {
		backupPath := fmt.Sprintf("%s.bak-%s", settingsPath, time.Now().UTC().Format("20060102T150405Z"))
		if data, err := os.ReadFile(settingsPath); err == nil {
			_ = os.WriteFile(backupPath, data, 0o600)
		}
	}

	// Ensure parent dir exists (first-time Claude Code users may not
	// have ~/.claude/ yet).
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}

	// Atomic write: tmp + rename.
	tmp := settingsPath + ".tmp"
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serialize settings: %w", err)
	}
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", tmp, settingsPath, err)
	}

	return false, nil
}

// isGramatonHookCommand reports whether a hook command string points
// at a gramaton-owned hook script. We identify ours by the path
// prefix; anything under ~/.gramaton/hooks/ is assumed to be ours.
// This is deliberately generous: it catches the canonical subdir
// layout, the legacy flat layout, and any tilde/non-tilde variant.
//
// Windows-safe: normalizes backslashes to forward slashes before
// the substring match so a settings.json command like
// `C:\Users\b\.gramaton\hooks\claude-code\session-start.sh` is
// recognized as ours.
func isGramatonHookCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.ReplaceAll(cmd, `\`, "/")
	return strings.Contains(cmd, "/.gramaton/hooks/")
}
