package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MCP client detection + registration for Step 3 of the wizard.
//
// # Why shell out to each client's own CLI (vs hand-editing its config)
//
// Each MCP client (Claude Code, kiro-cli, Codex) owns a config file
// schema. Hand-editing that file requires us to understand the exact
// layout (which keys are required, what shape `mcpServers` takes, how
// to preserve other user-added servers) and keep up with schema
// changes across client versions. Shelling out to `claude mcp add` /
// `codex mcp add` delegates the schema concern to the tool that owns
// it. Costs one subprocess per registration; saves a class of "works
// on my machine but breaks after a client update" bugs. (For Codex
// specifically it also preserves user comments in config.toml, which
// no Go TOML library round-trips — verified 2026-06-09.)
//
// The trade-off: we depend on the client CLI being the same version
// the user installed. That's fine for Claude Code (stable command
// since it launched). kiro-cli's MCP-registration surface is less
// certain; we try the claude-compatible syntax first and report
// clearly if it fails so the user can fall back to manual config.
//
// # What we register
//
// Entry name:  gramaton
// Command:     gramaton
// Args:        [mcp]
//
// Command is "gramaton" unquoted -- relies on gramaton being on PATH
// wherever the MCP client runs. For `go install`-based installs this
// is the normal GOPATH/bin on PATH; for users who installed into a
// non-PATH location, the registration succeeds but the client will
// fail to find gramaton at runtime. Step 5 verification (future pass)
// will re-run `claude mcp list` and flag connection failures so users
// see this before it's too late.

// DetectedClient describes one MCP client installed on this system
// that Step 3 can register Gramaton against. Name is a human-
// readable label ("Claude Code", "kiro-cli") used in wizard output;
// Binary is the absolute path returned by exec.LookPath, kept so
// we run the same binary we detected (not whatever PATH resolves
// to at registration time).
type DetectedClient struct {
	Name   string
	Binary string
}

// MCPBackend is the test seam for Step 3. Production uses
// DefaultMCPBackend; tests inject a fake to exercise the wizard's
// orchestration without running real clients.
type MCPBackend interface {
	// Detect returns the clients found on this system. Empty slice if
	// nothing was detected; never nil.
	Detect() []DetectedClient

	// Register adds Gramaton to the named client's config. Returns
	// (true, nil) if Gramaton was already registered (a soft success
	// the wizard can report differently), (false, nil) on a new
	// registration, or (false, err) on failure.
	Register(ctx context.Context, client DetectedClient) (alreadyRegistered bool, err error)
}

// DefaultMCPBackend is the production implementation. Uses exec to
// detect binaries and shell out to each client's CLI for registration.
type DefaultMCPBackend struct{}

// Detect probes every harness in the registry (PATH binary or
// config-dir presence, per entry). Order of the returned slice
// matches registry order -- it's stable for a given machine, which
// matters for the wizard's display.
func (DefaultMCPBackend) Detect() []DetectedClient {
	var clients []DetectedClient
	for _, h := range harnesses {
		if c, ok := detectHarness(h); ok {
			clients = append(clients, c)
		}
	}
	return clients
}

// Register dispatches to the harness's registration strategy.
// Unknown clients are a programming error (Detect returned something
// the registry doesn't know) so we surface that loudly.
func (DefaultMCPBackend) Register(ctx context.Context, client DetectedClient) (bool, error) {
	h := harnessByName(client.Name)
	if h == nil || h.RegisterMCP == nil {
		return false, fmt.Errorf("unknown MCP client: %s", client.Name)
	}
	return h.RegisterMCP(ctx, client.Binary)
}

// registerWithClaudeCode invokes `claude mcp add` to register Gramaton
// at user scope. First checks `claude mcp list` for existing gramaton
// entry so re-running the wizard is idempotent: if gramaton is
// already registered, we report that as a soft success rather than
// double-registering or erroring.
//
// Scope is `--scope user` (not project-local) because the wizard
// targets user-wide setup; project scope would only register for the
// current directory and confuse users who expect Gramaton to be
// available everywhere.
func registerWithClaudeCode(ctx context.Context, claudeBin string) (bool, error) {
	// Idempotency check: is gramaton already in `claude mcp list`?
	// We grep the output for a line starting with "gramaton:"
	// matching the documented list format
	// (see docs/benchmarks.md). If claude's output format changes
	// in a future version we might false-negative here and re-add,
	// which claude's own add command will then reject -- surfaced
	// cleanly to the user.
	listCmd := exec.CommandContext(ctx, claudeBin, "mcp", "list")
	listOut, err := listCmd.CombinedOutput()
	if err == nil {
		for _, line := range strings.Split(string(listOut), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "gramaton:") {
				return true, nil // already registered
			}
		}
	}
	// List may have failed (e.g., no MCP configured yet). That's not
	// a hard error -- we just proceed to add. If the real failure
	// mode was something else (permissions, corrupted config), the
	// add below will surface it.

	// claude mcp add --scope user gramaton gramaton -- mcp
	addCmd := exec.CommandContext(ctx, claudeBin,
		"mcp", "add",
		"--scope", "user",
		"gramaton", "gramaton",
		"--", "mcp")
	var stderr bytes.Buffer
	addCmd.Stderr = &stderr
	if out, err := addCmd.Output(); err != nil {
		// Return the combined stdout+stderr so the wizard's Warn
		// line gives the user something to diagnose with. Keeping
		// the raw error wrapped lets errors.Is checks work if
		// callers add sentinel detection later.
		return false, fmt.Errorf("claude mcp add failed: %w: %s %s",
			err, strings.TrimSpace(string(out)), strings.TrimSpace(stderr.String()))
	}
	return false, nil
}

// registerWithCodex invokes `codex mcp add gramaton -- gramaton mcp`
// to register Gramaton in Codex's user-global config.toml.
//
// No idempotency probe, deliberately: `codex mcp add` REPLACES an
// existing entry with the same name. Verified empirically against
// codex-cli 0.133.0 (2026-06-09, throwaway CODEX_HOME): re-running
// add swaps the entry in place, exits 0, and preserves user comments
// elsewhere in config.toml -- which also means re-registration never
// damages a hand-edited config. Replace-on-add can't distinguish
// "was already there" from "newly added", so alreadyRegistered is
// always false; the end state is identical either way.
//
// CODEX_HOME is honored for free: the codex CLI inherits our
// environment and writes wherever its own config-root resolution
// points.
func registerWithCodex(ctx context.Context, codexBin string) (bool, error) {
	addCmd := exec.CommandContext(ctx, codexBin,
		"mcp", "add",
		"gramaton",
		"--", "gramaton", "mcp")
	out, err := addCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("codex mcp add failed: %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

// registerWithKiroCli tries the claude-compatible `mcp add` syntax
// against kiro-cli. If the command doesn't exist or uses different
// arguments, we surface a clear error with the manual-config
// instruction.
//
// Design note: kiro-cli's MCP registration surface isn't documented
// in Gramaton's corpus. Best-effort here: assume kiro followed
// Claude Code's CLI conventions (plausible for an Anthropic-adjacent
// fork or independent implementation aiming at compatibility). If
// that assumption is wrong, the user sees a warning line telling them
// what went wrong and how to register manually. Better to try and
// fail-informative than to skip kiro entirely.
//
// TODO: when kiro-cli's documented MCP-registration command lands in
// its help output, replace this best-effort shell-out with the
// correct syntax.
func registerWithKiroCli(ctx context.Context, kiroBin string) (bool, error) {
	// Idempotency probe: try `kiro mcp list`. If it succeeds AND
	// mentions "gramaton", assume already registered. If the
	// subcommand doesn't exist we'll fall through.
	listCmd := exec.CommandContext(ctx, kiroBin, "mcp", "list")
	if listOut, err := listCmd.CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(listOut), "\n") {
			if strings.Contains(strings.TrimSpace(line), "gramaton") {
				return true, nil
			}
		}
	}

	addCmd := exec.CommandContext(ctx, kiroBin,
		"mcp", "add",
		"--scope", "user",
		"gramaton", "gramaton",
		"--", "mcp")
	out, err := addCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("kiro mcp add failed (kiro-cli may use different MCP-registration syntax): %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

// registerWithCursor writes Gramaton's MCP entry directly into
// ~/.cursor/mcp.json. Cursor IDE ships no CLI; direct file write is
// the only documented programmatic registration path, and the file
// is NOT auto-created on a fresh install -- we create it (and any
// missing parents). The bin parameter is unused: Cursor is
// dir-detected, so there is no binary to record.
//
// The merge is surgical, same contract as the hook patchers: every
// other server under mcpServers and every unrelated top-level key is
// preserved; only the gramaton entry is upserted. Returns
// alreadyRegistered=true when an identical gramaton entry is already
// present; a differing gramaton entry (e.g. hand-edited args) is
// replaced, matching the replace-on-add semantics of the CLI-based
// registrations.
func registerWithCursor(_ context.Context, _ string) (bool, error) {
	dir, err := cursorConfigDir()
	if err != nil {
		return false, err
	}
	mcpPath := filepath.Join(dir, "mcp.json")

	existing := map[string]any{}
	exists := false
	if raw, err := os.ReadFile(mcpPath); err == nil {
		exists = true
		if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
			return false, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", mcpPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", mcpPath, err)
	}

	servers, _ := existing["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	// Schema verified from vendor docs (cursor.com/docs/context/mcp,
	// 2026-05-24): standard mcpServers envelope, stdio transport.
	// "gramaton" unqualified relies on PATH, same trade-off as the
	// CLI-based registrations (documented at the top of this file).
	desired := map[string]any{
		"type":    "stdio",
		"command": "gramaton",
		"args":    []any{"mcp"},
	}

	if current, ok := servers["gramaton"]; ok {
		curJSON, _ := json.Marshal(current)
		wantJSON, _ := json.Marshal(desired)
		if string(curJSON) == string(wantJSON) {
			return true, nil // already registered, byte-identical
		}
	}

	servers["gramaton"] = desired
	existing["mcpServers"] = servers

	// Back up before rewriting, then atomic write. 0600 perms:
	// mcp.json can carry API keys in env/headers for other servers.
	if exists {
		backupPath := fmt.Sprintf("%s.bak-%s", mcpPath, time.Now().UTC().Format("20060102T150405Z"))
		if data, err := os.ReadFile(mcpPath); err == nil {
			_ = os.WriteFile(backupPath, data, 0o600)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serialize mcp.json: %w", err)
	}
	return false, writeAtomic(mcpPath, out, 0o600)
}

// ErrNoMCPBackend is returned by a Wizard whose MCPBackend field was
// explicitly cleared (by a test, for example) and who then tried to
// run Step 3. Should never fire in production.
var ErrNoMCPBackend = errors.New("no MCP backend configured")
