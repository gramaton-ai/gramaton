package setup

import (
	"bytes"
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
// hooks/ Go package (hooks/claude_code.go, hooks/kiro.go,
// hooks/cursor.go). This simplification eliminates the embed_hooks/
// tree duplication and removes the hidden python3 dependency the old
// shell scripts carried.
//
// Scripts are synthesized at init time from Go templates, so there's
// no need to check proxy files into the repo. Line endings are
// explicit (LF for .sh, CRLF for .cmd) so git's core.autocrlf can't
// corrupt the shebang.

// hookEventSpec describes one hook event for a given client.
// cliEvent is the positional arg passed to `gramaton hook`;
// fileBase is the proxy filename without extension;
// configEvent is the event key in claude-protocol hook configs --
// Claude Code's settings.json and Codex's hooks.json share the
// event names (SessionStart, Stop, PreCompact, PostCompact).
// Empty for clients whose hook config we don't auto-patch (Kiro).
type hookEventSpec struct {
	cliEvent    string
	fileBase    string
	configEvent string
}

// claudeCodeEvents are the four lifecycle events Gramaton wires
// into Claude Code. Order is stable for deterministic Materialize
// output and test assertions.
var claudeCodeEvents = []hookEventSpec{
	{cliEvent: "session-start", fileBase: "session-start", configEvent: "SessionStart"},
	{cliEvent: "stop", fileBase: "stop", configEvent: "Stop"},
	{cliEvent: "pre-compact", fileBase: "pre-compact", configEvent: "PreCompact"},
	{cliEvent: "post-compact", fileBase: "post-compact", configEvent: "PostCompact"},
}

// codexEvents are the four lifecycle events Gramaton wires into
// Codex. Deliberately a separate slice from claudeCodeEvents even
// though today they're identical (Codex documents the same event
// names -- shared protocol ancestry): the two harnesses can diverge
// independently, e.g. if we later adopt Codex-only events like
// SubagentStop. The cliEvents are the Claude-protocol handlers in
// hooks/ -- Codex's stdin contract (session_id/cwd/
// hook_event_name) is compatible per vendor docs.
var codexEvents = []hookEventSpec{
	{cliEvent: "session-start", fileBase: "session-start", configEvent: "SessionStart"},
	{cliEvent: "stop", fileBase: "stop", configEvent: "Stop"},
	{cliEvent: "pre-compact", fileBase: "pre-compact", configEvent: "PreCompact"},
	{cliEvent: "post-compact", fileBase: "post-compact", configEvent: "PostCompact"},
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

// cursorEvents are the three lifecycle events Gramaton wires into
// Cursor. Event keys are camelCase (Cursor's hooks.json convention,
// verified from the vendor-shipped create-hook skill, 2026-06-09);
// Cursor has no postCompact event. The cliEvents are
// cursor-prefixed because the handlers adapt Cursor's stdin shape
// (conversation_id / workspace_roots) before routing to the shared
// claude-protocol cores.
var cursorEvents = []hookEventSpec{
	{cliEvent: "cursor-session-start", fileBase: "session-start", configEvent: "sessionStart"},
	{cliEvent: "cursor-stop", fileBase: "stop", configEvent: "stop"},
	{cliEvent: "cursor-pre-compact", fileBase: "pre-compact", configEvent: "preCompact"},
}

// proxyStyle selects which proxy-script variants Materialize writes
// for a harness. The variants differ in interpreter (.sh vs .cmd)
// and line endings, never in behavior.
type proxyStyle int

const (
	// proxyPosixOnly: always .sh, on every OS. Claude Code bundles
	// Git Bash on Windows, so the POSIX script runs everywhere.
	proxyPosixOnly proxyStyle = iota

	// proxyNativePerOS: .sh on Unix, .cmd on Windows -- chosen at
	// materialize time from the host OS. Kiro CLI 2.x is native
	// Windows with no bash.
	proxyNativePerOS

	// proxyDualVariant: BOTH .sh and .cmd, regardless of host OS.
	// For harnesses whose hook config selects per-OS at runtime
	// (Codex's command/commandWindows fields): the config carries
	// both paths, so both scripts must exist. Also means zero
	// runtime.GOOS branching in the wiring logic -- deterministic,
	// testable output on every CI leg.
	proxyDualVariant
)

// proxyFile is one synthesized proxy script: filename (with
// extension) plus full body, line endings already correct for the
// target interpreter.
type proxyFile struct {
	name, body string
}

// proxyFilesFor synthesizes the proxy-script variants for one event
// under the harness's ProxyStyle.
//
// Line endings are chosen deliberately: LF for .sh so bash can
// read the shebang intact (CRLF-prefixed `#!/bin/bash\r` makes
// bash look for a binary named `bash\r` and fail); CRLF for .cmd
// so cmd.exe treats the file as a native batch script.
func proxyFilesFor(h *Harness, ev hookEventSpec) []proxyFile {
	sh := proxyFile{
		name: ev.fileBase + ".sh",
		body: fmt.Sprintf("#!/bin/bash\nexec gramaton hook %s\n", ev.cliEvent),
	}
	cmd := proxyFile{
		name: ev.fileBase + ".cmd",
		body: fmt.Sprintf("@gramaton hook %s\r\n", ev.cliEvent),
	}
	switch h.ProxyStyle {
	case proxyNativePerOS:
		if runtime.GOOS == "windows" {
			return []proxyFile{cmd}
		}
		return []proxyFile{sh}
	case proxyDualVariant:
		return []proxyFile{sh, cmd}
	default: // proxyPosixOnly
		return []proxyFile{sh}
	}
}

// HookBackend is the test seam for Step 5. Production uses
// DefaultHookBackend; tests inject a fake to exercise the wizard
// orchestration without touching the real filesystem or the user's
// hook configs.
type HookBackend interface {
	// Materialize generates the proxy scripts for `client` into a
	// canonical on-disk location (typically
	// ~/.gramaton/hooks/<client>/) and returns the absolute paths of
	// the installed scripts. Idempotent: re-running overwrites with
	// the current proxy template so upgrades propagate when users
	// re-run the wizard.
	Materialize(client string, configDir string) (scriptPaths []string, err error)

	// RegisterHooks wires the materialized scripts into the named
	// client's hook configuration; client is the hook embed-dir
	// name, same as Materialize. Existing gramaton-owned hook
	// entries (commands under ~/.gramaton/hooks/) are replaced in
	// place; other hook entries (user's own, other tools') are left
	// untouched. Returns (true, nil) when our entries were already
	// present and unchanged, (false, nil) on a successful update,
	// (false, err) on failure.
	RegisterHooks(ctx context.Context, client string, scriptPaths []string) (unchanged bool, err error)
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
	h := harnessByEmbedDir(client)
	if h == nil || len(h.HookEvents) == 0 {
		return nil, fmt.Errorf("no hooks defined for client %q", client)
	}

	destDir := filepath.Join(configDir, "hooks", client)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	var paths []string
	for _, ev := range h.HookEvents {
		for _, pf := range proxyFilesFor(h, ev) {
			destPath := filepath.Join(destDir, pf.name)
			if err := os.WriteFile(destPath, []byte(pf.body), 0o755); err != nil {
				return nil, fmt.Errorf("write %s: %w", destPath, err)
			}
			paths = append(paths, destPath)
		}
	}
	// Deterministic order (os.WriteFile doesn't impose one); makes
	// test assertions stable.
	sort.Strings(paths)
	return paths, nil
}

// RegisterHooks dispatches to the harness's hook-wiring strategy
// (mirrors DefaultMCPBackend.Register). Clients without a WireHooks
// strategy are a programming error here -- stepHooks only calls
// RegisterHooks when the registry entry has one.
func (DefaultHookBackend) RegisterHooks(ctx context.Context, client string, scriptPaths []string) (bool, error) {
	h := harnessByEmbedDir(client)
	if h == nil || h.WireHooks == nil {
		return false, fmt.Errorf("no hook auto-wiring strategy for client %q", client)
	}
	return h.WireHooks(ctx, scriptPaths)
}

// hookEventForConfig maps a proxy filename to the claude-protocol
// config event name that triggers it, using the given harness event
// list. Accepts both .sh and .cmd extensions so the same lookup
// serves every proxy variant. Unknown filenames return "" — the
// wiring strategies skip them.
func hookEventForConfig(events []hookEventSpec, scriptName string) string {
	base := strings.TrimSuffix(scriptName, ".cmd")
	base = strings.TrimSuffix(base, ".sh")
	for _, ev := range events {
		if base == ev.fileBase {
			return ev.configEvent
		}
	}
	return ""
}

// stripBOM removes a UTF-8 byte-order mark if present. Windows
// editors (Notepad, notably) prepend one when saving JSON;
// encoding/json rejects it as a syntax error. Every read of a
// user-editable config file goes through this.
func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// registerClaudeHooks patches the user-scope ~/.claude/settings.json
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
func registerClaudeHooks(_ context.Context, scriptPaths []string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("resolve home dir: %w", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings.json (may not exist -- first-time Claude
	// Code users). We start with an empty map if missing. The raw
	// bytes are kept for the pre-rewrite backup.
	existing := map[string]any{}
	var original []byte
	if raw, err := os.ReadFile(settingsPath); err == nil {
		original = raw
		if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
			return false, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", settingsPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", settingsPath, err)
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
		event := hookEventForConfig(claudeCodeEvents, filepath.Base(p))
		if event == "" {
			continue
		}
		wanted[event] = strings.ReplaceAll(p, `\`, "/")
	}
	if len(wanted) == 0 {
		return true, nil // nothing to register; technically unchanged
	}

	// Get or create the "hooks" sub-object. A parseable file whose
	// "hooks" value has the wrong TYPE gets the same won't-touch
	// treatment as unparseable JSON -- silently replacing it would
	// destroy whatever the user meant by it.
	hooksObj, ok := existing["hooks"].(map[string]any)
	if !ok {
		if _, present := existing["hooks"]; present {
			return false, fmt.Errorf("%s has a non-object \"hooks\" value; won't touch it -- fix by hand", settingsPath)
		}
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
		// A non-array event value gets the won't-touch treatment.
		var blocks []any
		if raw, ok := hooksObj[event]; ok {
			arr, isArr := raw.([]any)
			if !isArr {
				return false, fmt.Errorf("%s has a non-array %q hooks value; won't touch it -- fix by hand", settingsPath, event)
			}
			blocks = arr
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
			inner, innerIsArr := bm["hooks"].([]any)
			if !innerIsArr {
				// Not the matcher-block shape we manage (missing or
				// non-array hooks key); preserve verbatim rather
				// than dropping the user's block.
				kept = append(kept, b)
				continue
			}
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

	// Back up the existing settings.json before writing. A failed
	// backup aborts: we are about to mutate the very file the backup
	// protects.
	if err := writeConfigBackup(settingsPath, original); err != nil {
		return false, err
	}

	// Ensure parent dir exists (first-time Claude Code users may not
	// have ~/.claude/ yet).
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serialize settings: %w", err)
	}
	return false, writeAtomic(settingsPath, out, 0o600)
}

// writeConfigBackup writes a timestamped .bak sibling of path holding
// the original bytes, before a patcher rewrites the file. A nil/empty
// original (file didn't exist) is a no-op. Failure is an error, not
// best-effort: proceeding without a backup would leave the user's
// only copy exposed to the rewrite.
func writeConfigBackup(path string, original []byte) error {
	if len(original) == 0 {
		return nil
	}
	backupPath := fmt.Sprintf("%s.bak-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backupPath, original, 0o600); err != nil {
		return fmt.Errorf("write backup %s: %w (aborting before modifying the original)", backupPath, err)
	}
	return nil
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

// cursorConfigDir resolves Cursor's config root: ~/.cursor. No env
// relocation variable is documented for Cursor (unlike CODEX_HOME).
func cursorConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cursor"), nil
}

// codexConfigDir resolves Codex's config root: $CODEX_HOME when set
// (vendor-supported relocation -- also honored implicitly by `codex
// mcp add`, since the codex CLI inherits our environment), else
// ~/.codex.
func codexConfigDir() (string, error) {
	if root := os.Getenv("CODEX_HOME"); root != "" {
		// Guard against a relative value: os.MkdirAll would silently
		// fabricate the tree under whatever directory the wizard ran
		// from, scattering config where codex will never look.
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("CODEX_HOME is set but not an absolute path (%q); fix or unset it and re-run", root)
		}
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

// registerCodexHooks patches $CODEX_HOME/hooks.json (default
// ~/.codex/hooks.json) to route Codex's lifecycle events at our
// materialized proxy scripts. Same surgical-merge contract as
// registerClaudeHooks: preserve every unrelated key and every hook
// entry that isn't gramaton-owned; replace ours in place so re-runs
// are idempotent.
//
// Schema (vendor docs, developers.openai.com/codex/hooks, read
// 2026-05-24 -- claude-protocol shape, one extra field):
//
//	{"hooks": {"SessionStart": [
//	  {"hooks": [{"type": "command",
//	              "command": "...", "commandWindows": "..."}]}]}}
//
// Each of our entries carries BOTH command (the .sh proxy,
// forward-slashed) and commandWindows (the .cmd proxy, path as
// materialized) -- Codex picks per-OS at runtime, which is why
// proxyDualVariant materializes both variants on every host and no
// runtime.GOOS branch appears here. JSON has no comments, so unlike
// AGENTS.md there is no fence convention; ownership is by command
// path (isGramatonHookCommand), exactly like settings.json.
//
// Why hooks.json and not config.toml's inline [hooks]: both are
// documented homes, but hooks.json keeps us out of the file `codex
// mcp add` writes (no two-writers risk) and out of the TOML-
// round-tripping business entirely.
func registerCodexHooks(_ context.Context, scriptPaths []string) (bool, error) {
	dir, err := codexConfigDir()
	if err != nil {
		return false, err
	}
	hooksPath := filepath.Join(dir, "hooks.json")

	existing := map[string]any{}
	var original []byte
	if raw, err := os.ReadFile(hooksPath); err == nil {
		original = raw
		if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
			return false, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", hooksPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", hooksPath, err)
	}

	// Group the materialized scripts by event: the .sh variant fills
	// `command`, the .cmd variant fills `commandWindows`. The .sh
	// path is forward-slashed for the same reason as Claude Code's
	// (backslashes are escape characters to a POSIX shell); the .cmd
	// path is written exactly as materialized -- on a Windows host
	// that's a native backslashed path, which is what cmd.exe wants.
	type variantPair struct{ sh, cmd string }
	wanted := map[string]*variantPair{}
	for _, p := range scriptPaths {
		event := hookEventForConfig(codexEvents, filepath.Base(p))
		if event == "" {
			continue
		}
		pair := wanted[event]
		if pair == nil {
			pair = &variantPair{}
			wanted[event] = pair
		}
		if strings.HasSuffix(p, ".cmd") {
			pair.cmd = p
		} else {
			pair.sh = strings.ReplaceAll(p, `\`, "/")
		}
	}
	if len(wanted) == 0 {
		return true, nil // nothing to register; technically unchanged
	}

	// Wrong-TYPE envelope values get the won't-touch treatment, same
	// as unparseable JSON.
	hooksObj, ok := existing["hooks"].(map[string]any)
	if !ok {
		if _, present := existing["hooks"]; present {
			return false, fmt.Errorf("%s has a non-object \"hooks\" value; won't touch it -- fix by hand", hooksPath)
		}
		hooksObj = map[string]any{}
	}

	changed := false
	for event, pair := range wanted {
		var blocks []any
		if raw, ok := hooksObj[event]; ok {
			arr, isArr := raw.([]any)
			if !isArr {
				return false, fmt.Errorf("%s has a non-array %q hooks value; won't touch it -- fix by hand", hooksPath, event)
			}
			blocks = arr
		}

		// Filter out existing gramaton-owned entries (either command
		// field may carry our path).
		kept := make([]any, 0, len(blocks))
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				kept = append(kept, b)
				continue
			}
			inner, innerIsArr := bm["hooks"].([]any)
			if !innerIsArr {
				// Not the matcher-block shape we manage; preserve
				// verbatim rather than dropping the user's block.
				kept = append(kept, b)
				continue
			}
			newInner := make([]any, 0, len(inner))
			for _, h := range inner {
				hm, ok := h.(map[string]any)
				if !ok {
					newInner = append(newInner, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				cmdWin, _ := hm["commandWindows"].(string)
				if isGramatonHookCommand(cmd) || isGramatonHookCommand(cmdWin) {
					continue // drop; we'll add our canonical entry
				}
				newInner = append(newInner, h)
			}
			if len(newInner) > 0 {
				bm["hooks"] = newInner
				kept = append(kept, bm)
			}
		}

		// Add our canonical block. No matcher field: omitted means
		// "all occurrences" per vendor docs.
		entry := map[string]any{"type": "command"}
		if pair.sh != "" {
			entry["command"] = pair.sh
		}
		if pair.cmd != "" {
			entry["commandWindows"] = pair.cmd
		}
		kept = append(kept, map[string]any{"hooks": []any{entry}})

		// Idempotency via JSON comparison, same rationale as the
		// Claude patcher: the only authoritative answer to "did this
		// change anything" is "is the final JSON different".
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

	if err := writeConfigBackup(hooksPath, original); err != nil {
		return false, err
	}

	// Ensure the config root exists (hooks.json is absent on a fresh
	// Codex install; the directory may be too under CODEX_HOME).
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serialize hooks.json: %w", err)
	}
	return false, writeAtomic(hooksPath, out, 0o600)
}

// registerCursorHooks patches ~/.cursor/hooks.json to route Cursor's
// lifecycle events at our materialized proxy scripts. Same
// surgical-merge contract as the other patchers: preserve every
// unrelated key and every hook entry that isn't gramaton-owned;
// replace ours in place so re-runs are idempotent.
//
// Schema (verified 2026-06-09 from Cursor's vendor-shipped
// create-hook skill -- NOT the nested matcher-block shape Claude
// Code and Codex use; entries sit directly under the event):
//
//	{"version": 1,
//	 "hooks": {"sessionStart": [{"command": "..."}]}}
//
// `version: 1` is required; we add it when absent and never touch an
// existing value. There is no commandWindows field in Cursor's
// schema, which is why the Cursor registry entry uses
// proxyNativePerOS -- the command points at the variant materialized
// for this host. The command path is written exactly as
// materialized: Cursor executes it natively (no Git Bash in the
// loop), so native separators are correct on every OS.
func registerCursorHooks(_ context.Context, scriptPaths []string) (bool, error) {
	dir, err := cursorConfigDir()
	if err != nil {
		return false, err
	}
	hooksPath := filepath.Join(dir, "hooks.json")

	existing := map[string]any{}
	var original []byte
	if raw, err := os.ReadFile(hooksPath); err == nil {
		original = raw
		if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
			return false, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", hooksPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", hooksPath, err)
	}

	wanted := map[string]string{}
	for _, p := range scriptPaths {
		event := hookEventForConfig(cursorEvents, filepath.Base(p))
		if event == "" {
			continue
		}
		wanted[event] = p
	}
	if len(wanted) == 0 {
		return true, nil // nothing to register; technically unchanged
	}

	// Wrong-TYPE envelope values get the won't-touch treatment, same
	// as unparseable JSON.
	hooksObj, ok := existing["hooks"].(map[string]any)
	if !ok {
		if _, present := existing["hooks"]; present {
			return false, fmt.Errorf("%s has a non-object \"hooks\" value; won't touch it -- fix by hand", hooksPath)
		}
		hooksObj = map[string]any{}
	}

	changed := false
	if _, ok := existing["version"]; !ok {
		existing["version"] = 1
		changed = true
	}

	for event, script := range wanted {
		var entries []any
		if raw, ok := hooksObj[event]; ok {
			arr, isArr := raw.([]any)
			if !isArr {
				return false, fmt.Errorf("%s has a non-array %q hooks value; won't touch it -- fix by hand", hooksPath, event)
			}
			entries = arr
		}

		// Filter out existing gramaton-owned entries (flat shape:
		// the command field sits directly on the entry).
		kept := make([]any, 0, len(entries))
		for _, e := range entries {
			em, ok := e.(map[string]any)
			if !ok {
				kept = append(kept, e)
				continue
			}
			cmd, _ := em["command"].(string)
			if isGramatonHookCommand(cmd) {
				continue // drop; we'll add our canonical entry
			}
			kept = append(kept, e)
		}

		// Add our canonical entry. No matcher (omitted = every
		// occurrence), no failClosed (Cursor defaults fail-open,
		// which is what we want -- a Gramaton bug must never block
		// the user's editor).
		kept = append(kept, map[string]any{"command": script})

		oldJSON, _ := json.Marshal(entries)
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

	if err := writeConfigBackup(hooksPath, original); err != nil {
		return false, err
	}

	// ~/.cursor/ exists on any real Cursor install (it's the
	// detection signal), but hooks.json does not -- create it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serialize hooks.json: %w", err)
	}
	return false, writeAtomic(hooksPath, out, 0o600)
}
