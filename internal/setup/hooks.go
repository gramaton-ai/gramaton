package setup

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Hook scripts for automatic-capture integration with MCP clients.
//
// The canonical hook sources live at repo-root under hooks/, but Go's
// //go:embed directive can't reference `..` paths. So we carry a copy
// inside internal/setup/embed_hooks/ that's the embed source, with
// generate directives below that keep the copy in sync with hooks/.
// Running `go generate ./internal/setup/` re-copies before a release.
// The duplication is a known tech-debt item (post-OSS cleanup
// candidate); for v1 it's the simplest path that lets `go install`
// ship with working hooks.
//
//go:generate cp -rp ../../hooks/claude-code/. embed_hooks/claude-code/
//go:generate cp -rp ../../hooks/kiro/. embed_hooks/kiro/

//go:embed embed_hooks
var hooksFS embed.FS

// HookBackend is the test seam for Step 4. Production uses
// DefaultHookBackend; tests inject a fake to exercise the wizard
// orchestration without touching the real filesystem or the user's
// Claude Code settings.json.
type HookBackend interface {
	// Materialize extracts the embedded hook scripts for `client`
	// into a canonical on-disk location (typically
	// ~/.gramaton/hooks/<client>/) and returns the absolute paths of
	// the installed scripts. Idempotent: re-running overwrites with
	// the current embedded version so upgrades propagate when users
	// re-run the wizard.
	Materialize(client string, configDir string) (scriptPaths []string, err error)

	// RegisterClaudeHooks patches ~/.claude/settings.json to point
	// the user-scope hooks at the given script paths. Existing
	// gramaton-owned hook entries (commands starting with
	// ~/.gramaton/hooks/) are replaced in place; other hook entries
	// (user's own, other tools') are left untouched. Returns (true,
	// nil) when our entries were already present and unchanged,
	// (false, nil) on a successful update, (false, err) on failure.
	RegisterClaudeHooks(ctx context.Context, scriptPaths []string) (unchanged bool, err error)
}

// DefaultHookBackend is the production implementation.
type DefaultHookBackend struct{}

// Materialize writes the embedded scripts for `client` into
// <configDir>/hooks/<client>/. configDir is typically ~/.gramaton.
// Creates directories with 0700, writes scripts with 0755 (exec bit
// required for shells to run them directly).
//
// Script content is overwritten on each call so users who upgrade
// their Gramaton binary + re-run the wizard get the latest hook
// logic. Local edits the user made to the materialized scripts will
// be lost on re-run; this is documented in the wizard output so users
// who want to customize hooks know to do it in their own scripts
// sourced by ours, not by editing our files directly.
func (DefaultHookBackend) Materialize(client string, configDir string) ([]string, error) {
	embedRoot := filepath.Join("embed_hooks", client)
	entries, err := fs.ReadDir(hooksFS, embedRoot)
	if err != nil {
		return nil, fmt.Errorf("no embedded hooks for %q: %w", client, err)
	}

	destDir := filepath.Join(configDir, "hooks", client)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		srcPath := filepath.Join(embedRoot, e.Name())
		content, err := fs.ReadFile(hooksFS, srcPath)
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", srcPath, err)
		}
		destPath := filepath.Join(destDir, e.Name())
		if err := os.WriteFile(destPath, content, 0o755); err != nil {
			return nil, fmt.Errorf("write %s: %w", destPath, err)
		}
		paths = append(paths, destPath)
	}
	// Deterministic order (fs.ReadDir is alphabetical on most
	// filesystems but not guaranteed); makes test assertions stable.
	sort.Strings(paths)
	return paths, nil
}

// hookEventForClaude maps a claude-code script filename to the
// Claude Code event name that triggers it. Claude Code's hook config
// keys hooks by event name; we need to know which event each script
// handles to write the right JSON.
//
// Source of truth: the header comment in each script under
// hooks/claude-code/ states its event name. This map mirrors that.
// Unknown script names return "" so the registration step skips
// them (better than registering against a wrong event).
func hookEventForClaude(scriptName string) string {
	switch scriptName {
	case "session-start.sh":
		return "SessionStart"
	case "stop.sh":
		return "Stop"
	case "pre-compact.sh":
		return "PreCompact"
	case "post-compact.sh":
		return "PostCompact"
	}
	return ""
}

// RegisterClaudeHooks patches the user-scope ~/.claude/settings.json
// to route hook events at our materialized scripts. The merge is
// additive and surgical: we preserve every top-level key, every
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
	wanted := map[string]string{}
	for _, p := range scriptPaths {
		event := hookEventForClaude(filepath.Base(p))
		if event == "" {
			continue
		}
		wanted[event] = p
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
func isGramatonHookCommand(cmd string) bool {
	// Normalize: trim surrounding quotes or whitespace the user might
	// have added in hand-edited settings. Substring match keeps us
	// robust to absolute-path expansion variations.
	cmd = strings.TrimSpace(cmd)
	return strings.Contains(cmd, "/.gramaton/hooks/")
}
