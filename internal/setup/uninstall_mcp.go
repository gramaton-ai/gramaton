package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MCP entry enumeration + removal for `gramaton uninstall`.
//
// The same shell-out-to-the-vendor-CLI rationale as registration
// applies (see mcp.go): the vendor CLI owns its config schema, so
// `claude mcp remove` / `codex mcp remove` are the removal paths for
// CLI-shipping harnesses, and Cursor -- which ships no CLI -- gets a
// direct surgical edit of ~/.cursor/mcp.json, mirroring
// registerCursorEntry.
//
// Enumeration reads the harness's config by naming convention
// (isGramatonMCPEntryName), never a record of what init wrote: a
// hand-added "gramaton-scratch" entry or a stale entry for a
// since-deleted store must be caught too.

// NoAutostartEnv, when set to "1" in a gramaton process's
// environment, suppresses the CLI client's auto-start of a
// background server (cli/client.go serverURL). Uninstall sets it on
// every vendor-CLI invocation -- list AND remove -- because `claude
// mcp list` health-checks each configured stdio entry by SPAWNING
// it, and a spawned `gramaton mcp` would otherwise auto-start the
// very server uninstall just stopped (or, on a dry run, one that was
// never running at all).
const NoAutostartEnv = "GRAMATON_NO_AUTOSTART"

// runHarnessCommand is the exec seam for uninstall's vendor-CLI
// calls (`claude mcp list/remove`, `codex ...`, `kiro ...`).
// Production shells out with NoAutostartEnv=1 (see above) and
// returns combined output; tests swap in a fake to exercise
// enumeration parsing and removal argv without real binaries.
var runHarnessCommand = func(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), NoAutostartEnv+"=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// isGramatonMCPEntryName reports whether an MCP server-entry name is
// gramaton-owned by naming convention: the default "gramaton" entry
// or a per-store "gramaton-<store>" entry (storeMCPEntryName).
// Exact-name matching only -- "gramatonx" or "my-gramaton" are not
// ours.
func isGramatonMCPEntryName(name string) bool {
	return name == "gramaton" || strings.HasPrefix(name, "gramaton-")
}

// dedupeSortedEntries dedupes and sorts an entry-name list so
// removal order (and test assertions) are deterministic regardless
// of vendor list-output ordering.
func dedupeSortedEntries(entries []string) []string {
	seen := map[string]bool{}
	out := entries[:0]
	for _, e := range entries {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// isGramatonCommandToken reports whether a command token from vendor
// list output runs the gramaton binary: bare "gramaton" or a path
// ending in /gramaton (backslashes normalized; a trailing .exe is
// tolerated for Windows-registered absolute paths). This is the
// ownership check's second leg -- a convention-NAMED entry whose
// command is a foreign binary is not ours to remove.
func isGramatonCommandToken(tok string) bool {
	tok = strings.ReplaceAll(strings.TrimSpace(tok), `\`, "/")
	tok = strings.TrimSuffix(tok, ".exe")
	return tok == "gramaton" || strings.HasSuffix(tok, "/gramaton")
}

// parseColonMCPList extracts gramaton-owned entry names from
// `claude mcp list`-style output: one server per line, shaped
// "name: command args - status" (the status tail is "✓ Connected",
// "✗ Failed to connect", etc. -- both shapes parse identically, so a
// health check failed by NoAutostartEnv suppression doesn't hide the
// entry). The name before the first colon must match the naming
// convention AND the command token after it must run gramaton
// (isGramatonCommandToken). Header lines never match because their
// pre-colon text isn't a gramaton-convention name.
func parseColonMCPList(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if !isGramatonMCPEntryName(name) {
			continue
		}
		rest := strings.Fields(line[idx+1:])
		if len(rest) == 0 || !isGramatonCommandToken(rest[0]) {
			continue
		}
		entries = append(entries, name)
	}
	return dedupeSortedEntries(entries)
}

// parseTokenMCPList extracts gramaton-owned entry names from tabular
// `codex mcp list`-style output: the server name is the first
// whitespace-separated token of its row, the command the second.
// Selection requires the convention name AND a gramaton command
// token -- a user's unrelated server that happens to RUN gramaton
// under a non-convention name is not ours, and neither is a
// convention-named entry running a foreign binary.
//
// strictCommand=false (kiro, whose list format is unverified)
// tolerates a row with no visible command column: the name
// convention alone selects it, but a VISIBLE foreign command still
// excludes it.
func parseTokenMCPList(out string, strictCommand bool) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !isGramatonMCPEntryName(fields[0]) {
			continue
		}
		switch {
		case len(fields) >= 2:
			if !isGramatonCommandToken(fields[1]) {
				continue
			}
		case strictCommand:
			continue // no command column visible; refuse under strict
		}
		entries = append(entries, fields[0])
	}
	return dedupeSortedEntries(entries)
}

// listClaudeMCPEntries enumerates gramaton-owned entries from
// `claude mcp list`.
func listClaudeMCPEntries(ctx context.Context, bin string) ([]string, error) {
	out, err := runHarnessCommand(ctx, bin, "mcp", "list")
	if err != nil {
		return nil, fmt.Errorf("`claude mcp list` failed: %w: %s", err, strings.TrimSpace(out))
	}
	return parseColonMCPList(out), nil
}

// removeClaudeMCPEntries removes entries one at a time via
// `claude mcp remove --scope user <entry>`. The scope must match the
// `--scope user` the wizard registered with -- without it, claude
// resolves the entry against local/project scope and misses ours.
func removeClaudeMCPEntries(ctx context.Context, bin string, entries []string) ([]string, string, error) {
	var removed []string
	for _, e := range entries {
		out, err := runHarnessCommand(ctx, bin, "mcp", "remove", "--scope", "user", e)
		if err != nil {
			return removed, "", fmt.Errorf("claude mcp remove %s failed: %w: %s", e, err, strings.TrimSpace(out))
		}
		removed = append(removed, e)
	}
	return removed, "", nil
}

// listCodexMCPEntries enumerates gramaton-owned entries from
// `codex mcp list` (tabular output; first token per row is the
// server name). CODEX_HOME is honored for free: the codex CLI
// inherits our environment, same as registration.
func listCodexMCPEntries(ctx context.Context, bin string) ([]string, error) {
	out, err := runHarnessCommand(ctx, bin, "mcp", "list")
	if err != nil {
		return nil, fmt.Errorf("`codex mcp list` failed: %w: %s", err, strings.TrimSpace(out))
	}
	return parseTokenMCPList(out, true), nil
}

// removeCodexMCPEntries removes entries via `codex mcp remove
// <entry>` (verified syntax family of codex-cli 0.133.0: positional
// name, no scope flag -- config.toml entries are user-global by
// nature).
func removeCodexMCPEntries(ctx context.Context, bin string, entries []string) ([]string, string, error) {
	var removed []string
	for _, e := range entries {
		out, err := runHarnessCommand(ctx, bin, "mcp", "remove", e)
		if err != nil {
			return removed, "", fmt.Errorf("codex mcp remove %s failed: %w: %s", e, err, strings.TrimSpace(out))
		}
		removed = append(removed, e)
	}
	return removed, "", nil
}

// listKiroMCPEntries enumerates gramaton-owned entries from
// `kiro mcp list`, best-effort: kiro-cli's list-output format is
// unverified (the integration is parked -- see the registry entry's
// KNOWN BUG note), so the conservative first-token parse is used and
// a failed list surfaces informatively rather than fatally.
func listKiroMCPEntries(ctx context.Context, bin string) ([]string, error) {
	out, err := runHarnessCommand(ctx, bin, "mcp", "list")
	if err != nil {
		return nil, fmt.Errorf("`kiro mcp list` failed (kiro-cli may use different MCP syntax): %w: %s", err, strings.TrimSpace(out))
	}
	return parseTokenMCPList(out, false), nil
}

// removeKiroMCPEntries removes entries via the claude-compatible
// `kiro mcp remove --scope user <entry>`, best-effort with the same
// caveats as registerKiroEntry: if kiro-cli's real syntax differs,
// the error tells the user what was tried.
func removeKiroMCPEntries(ctx context.Context, bin string, entries []string) ([]string, string, error) {
	var removed []string
	for _, e := range entries {
		out, err := runHarnessCommand(ctx, bin, "mcp", "remove", "--scope", "user", e)
		if err != nil {
			return removed, "", fmt.Errorf("kiro mcp remove %s failed (kiro-cli may use different MCP syntax): %w: %s", e, err, strings.TrimSpace(out))
		}
		removed = append(removed, e)
	}
	return removed, "", nil
}

// readCursorMCP reads and parses ~/.cursor/mcp.json for the
// uninstall path. A missing file returns all-nil (nothing
// registered). Unparseable JSON and a present-but-non-object
// mcpServers value are refused with the same won't-touch contract as
// registerCursorEntry; a missing mcpServers key returns a nil
// servers map (nothing of ours can be present).
func readCursorMCP() (doc map[string]any, servers map[string]any, mcpPath string, raw []byte, err error) {
	dir, err := cursorConfigDir()
	if err != nil {
		return nil, nil, "", nil, err
	}
	mcpPath = filepath.Join(dir, "mcp.json")
	raw, err = os.ReadFile(mcpPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, mcpPath, nil, nil
	}
	if err != nil {
		return nil, nil, mcpPath, nil, fmt.Errorf("read %s: %w", mcpPath, err)
	}
	doc = map[string]any{}
	if err := json.Unmarshal(stripBOM(raw), &doc); err != nil {
		return nil, nil, mcpPath, nil, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", mcpPath, err)
	}
	serversVal, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		if _, present := doc["mcpServers"]; present {
			return nil, nil, mcpPath, nil, fmt.Errorf("%s has a non-object \"mcpServers\" value; won't touch it -- fix by hand", mcpPath)
		}
		return doc, nil, mcpPath, raw, nil
	}
	return doc, serversVal, mcpPath, raw, nil
}

// listCursorMCPEntries enumerates gramaton-owned entries in
// ~/.cursor/mcp.json. Ownership requires BOTH the naming convention
// AND command == "gramaton": a user's unrelated server that happens
// to be named gramaton-something but runs a different binary is not
// ours to delete. bin is unused (Cursor is dir-detected; no CLI).
func listCursorMCPEntries(_ context.Context, _ string) ([]string, error) {
	_, servers, _, _, err := readCursorMCP()
	if err != nil {
		return nil, err
	}
	var entries []string
	for name, v := range servers {
		if !isGramatonMCPEntryName(name) {
			continue
		}
		em, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := em["command"].(string); cmd == "gramaton" {
			entries = append(entries, name)
		}
	}
	return dedupeSortedEntries(entries), nil
}

// removeCursorMCPEntries deletes the named keys from mcpServers in
// one pass -- one backup, one atomic rewrite -- preserving every
// other server and every unrelated top-level key. The entries were
// selected by listCursorMCPEntries's ownership check; a key that has
// vanished in between is silently fine.
func removeCursorMCPEntries(_ context.Context, _ string, entries []string) ([]string, string, error) {
	doc, servers, mcpPath, raw, err := readCursorMCP()
	if err != nil {
		return nil, "", err
	}
	if servers == nil {
		return nil, "", nil
	}
	var removed []string
	for _, e := range entries {
		if _, ok := servers[e]; ok {
			delete(servers, e)
			removed = append(removed, e)
		}
	}
	if len(removed) == 0 {
		return nil, "", nil
	}
	doc["mcpServers"] = servers

	backup, err := writeConfigBackup(mcpPath, raw)
	if err != nil {
		return nil, "", err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, backup, fmt.Errorf("serialize mcp.json: %w", err)
	}
	if err := writeAtomic(mcpPath, out, 0o600); err != nil {
		return nil, backup, err
	}
	return removed, backup, nil
}
