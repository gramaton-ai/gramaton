package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Claude Code permission pre-approval.
//
// Claude Code prompts before every MCP tool call whose name is not in
// settings.json's permissions.allow list. For a knowledge store whose
// whole point is agents saving without friction, that prompt silently
// defeats automatic capture: the agent calls gramaton_session_save,
// the user sees a permission dialog, and "automatic" becomes "ask me
// every time". Worse, the entries rot: a tool rename orphans the old
// allow entry and the new name prompts again, with nothing reconciling
// the drift.
//
// registerClaudePermissions makes init the owner of exactly the
// gramaton slice of that list. Ownership boundary: entries prefixed
// mcp__gramaton__ -- the default MCP server name init itself
// registers. Attached-store servers (registered under other names)
// and every non-gramaton entry are never touched. Deny and ask
// entries are never written, never removed, and always win: a tool
// the user denied (or set to ask) is not added to allow, in any of
// the rule shapes Claude Code accepts (exact, trailing-glob, bare
// server name).

// claudePermissionPrefix marks the allow-list entries this installer
// owns: tools of the default "gramaton" MCP server entry (the name
// registerClaudeCodeEntry registers when no store name is given).
const claudePermissionPrefix = "mcp__gramaton__"

// errPermissionsBlocked reports that every gramaton tool is covered
// by the user's deny/ask rules (a server-wide or broader block), so
// the patcher wrote nothing. The wizard turns this into an honest
// skip line instead of a failure.
var errPermissionsBlocked = errors.New("permissions.deny/ask rules block every gramaton tool")

// permissionRuleBlocks reports whether any of the user's deny/ask
// rules covers the given mcp__gramaton__<tool> entry. Rule shapes
// honored (Claude Code permission syntax): the exact entry, a
// trailing-glob prefix (`mcp__*`, `mcp__gramaton__*`), and the bare
// server form `mcp__gramaton` which blocks every tool of the server.
func permissionRuleBlocks(rules []string, entry string) bool {
	bareServer := strings.TrimSuffix(claudePermissionPrefix, "__")
	for _, r := range rules {
		if r == entry || r == bareServer {
			return true
		}
		if p, ok := strings.CutSuffix(r, "*"); ok && strings.HasPrefix(entry, p) {
			return true
		}
	}
	return false
}

// registerClaudePermissions patches the user-scope
// ~/.claude/settings.json permissions.allow list to pre-approve the
// given gramaton tool names. Same surgical contract as
// registerClaudeHooks: preserve every unrelated key and every
// non-gramaton entry, replace the gramaton-owned slice wholesale so
// re-runs are idempotent and renames reconcile (a stale
// mcp__gramaton__ entry whose tool no longer exists is dropped).
// Returns (true, nil) when the file already matched.
func registerClaudePermissions(_ context.Context, toolNames []string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("resolve home dir: %w", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

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

	// Get or create the "permissions" sub-object; a wrong-TYPE value
	// gets the won't-touch treatment (same contract as hooks).
	permsObj, ok := existing["permissions"].(map[string]any)
	if !ok {
		if _, present := existing["permissions"]; present {
			return false, fmt.Errorf("%s has a non-object \"permissions\" value; won't touch it -- fix by hand", settingsPath)
		}
		permsObj = map[string]any{}
	}

	var allow []any
	if raw, present := permsObj["allow"]; present {
		arr, isArr := raw.([]any)
		if !isArr {
			return false, fmt.Errorf("%s has a non-array permissions.allow value; won't touch it -- fix by hand", settingsPath)
		}
		allow = arr
	}

	// Deny and ask both outrank allow and are read-only to us. A
	// non-array value is a won't-touch error even though we don't
	// modify either list: we cannot honor entries we cannot read, and
	// guessing risks pre-approving something the user meant to block.
	// Claude Code permission rules come in more shapes than exact
	// tool names -- `mcp__gramaton` (whole server), `mcp__gramaton__*`
	// / `mcp__*` (trailing-glob), and per-tool -- so blocking rules
	// are matched structurally, not by string equality.
	var blocking []string
	for _, key := range []string{"deny", "ask"} {
		raw, present := permsObj[key]
		if !present {
			continue
		}
		arr, isArr := raw.([]any)
		if !isArr {
			return false, fmt.Errorf("%s has a non-array permissions.%s value; won't touch it -- fix by hand", settingsPath, key)
		}
		for _, d := range arr {
			if s, isStr := d.(string); isStr {
				blocking = append(blocking, s)
			}
		}
	}

	wanted := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		entry := claudePermissionPrefix + name
		if permissionRuleBlocks(blocking, entry) {
			continue
		}
		wanted = append(wanted, entry)
	}
	sort.Strings(wanted)

	// Every tool blocked means the user has a standing server-wide
	// (or broader) deny/ask posture. Write nothing -- not even the
	// stale-entry reconciliation -- and tell the wizard why: an
	// installer half-editing the permission block of a user who shut
	// the door on it would be exactly the overreach the consent gate
	// exists to prevent.
	if len(toolNames) > 0 && len(wanted) == 0 {
		return false, errPermissionsBlocked
	}
	if len(wanted) == 0 {
		return true, nil
	}

	// Rebuild allow: every non-gramaton entry (including non-string
	// oddities) survives in place; the gramaton-owned slice is
	// replaced wholesale by `wanted`, appended at the end. First run
	// may reorder the user's hand-added gramaton entries to the tail;
	// every run after that is stable, and the JSON comparison below
	// reports unchanged.
	kept := make([]any, 0, len(allow)+len(wanted))
	for _, entry := range allow {
		if s, isStr := entry.(string); isStr && strings.HasPrefix(s, claudePermissionPrefix) {
			continue
		}
		kept = append(kept, entry)
	}
	for _, w := range wanted {
		kept = append(kept, w)
	}

	oldJSON, _ := json.Marshal(allow)
	newJSON, _ := json.Marshal(kept)
	if string(oldJSON) == string(newJSON) {
		return true, nil
	}

	permsObj["allow"] = kept
	existing["permissions"] = permsObj

	if _, err := writeConfigBackup(settingsPath, original); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}
	out, err := marshalSettingsJSON(existing)
	if err != nil {
		return false, fmt.Errorf("serialize settings: %w", err)
	}
	return false, writeAtomic(settingsPath, out, 0o600)
}

// unregisterClaudePermissions strips every gramaton-owned entry from
// permissions.allow: the strip-only counterpart of
// registerClaudePermissions, with the same ownership rule (the
// mcp__gramaton__ prefix) and preservation contract. Deny entries are
// left as found, stale or not. apply=false probes without writing.
// Returns whether anything was (or would be) removed, plus the backup
// path when a rewrite happened.
func unregisterClaudePermissions(_ context.Context, apply bool) (bool, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "", fmt.Errorf("resolve home dir: %w", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("read %s: %w", settingsPath, err)
	}
	existing := map[string]any{}
	if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
		return false, "", fmt.Errorf("parse %s: %w", settingsPath, err)
	}
	permsObj, ok := existing["permissions"].(map[string]any)
	if !ok {
		return false, "", nil
	}
	allow, ok := permsObj["allow"].([]any)
	if !ok {
		return false, "", nil
	}

	kept := make([]any, 0, len(allow))
	for _, entry := range allow {
		if s, isStr := entry.(string); isStr && strings.HasPrefix(s, claudePermissionPrefix) {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(allow) {
		return false, "", nil
	}
	if !apply {
		return true, "", nil
	}

	permsObj["allow"] = kept
	existing["permissions"] = permsObj
	backup, err := writeConfigBackup(settingsPath, raw)
	if err != nil {
		return true, "", err
	}
	out, err := marshalSettingsJSON(existing)
	if err != nil {
		return true, backup, fmt.Errorf("serialize settings: %w", err)
	}
	return true, backup, writeAtomic(settingsPath, out, 0o600)
}
