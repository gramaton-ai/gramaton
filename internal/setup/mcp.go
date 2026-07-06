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
// Cursor is the documented exception: the IDE ships no CLI at all,
// so registerWithCursor hand-edits ~/.cursor/mcp.json — the only
// programmatic path Cursor offers.
//
// The trade-off: we depend on the client CLI being the same version
// the user installed. That's fine for Claude Code (stable command
// since it launched). kiro-cli's MCP-registration surface is less
// certain; we try the claude-compatible syntax first and report
// clearly if it fails so the user can fall back to manual config.
//
// # What we register
//
// Default-store entry (Register):
//
//	Entry name:  gramaton
//	Command:     gramaton
//	Args:        [mcp]
//
// Attached read-only store entry (RegisterStore, the wizard's
// read-only route):
//
//	Entry name:  gramaton-<store>
//	Command:     gramaton
//	Args:        [--store <store> mcp]
//
// The per-store entry rides the CLI's global --store flag
// (cli/root.go), so the MCP process resolves the named store's
// config dir, server, and STORE manifest; the frozen manifest then
// makes `gramaton mcp` register only the read-only tool surface
// (cli/mcp_cmd.go resolveMCPReadOnly). The distinct entry name keeps
// an attached store from clobbering a default "gramaton"
// registration on machines that also run their own writable store.
//
// Command is "gramaton" unquoted -- relies on gramaton being on PATH
// wherever the MCP client runs. For `go install`-based installs this
// is the normal GOPATH/bin on PATH; for users who installed into a
// non-PATH location, the registration succeeds but the client will
// fail to find gramaton at runtime. The wizard's verification pass
// surveys registration PRESENCE per harness (the VerifyMCPRegistered
// strategies below); actual connection testing is `gramaton
// preflight` / future-doctor territory.

// storeMCPEntryName is the MCP server-entry name registered for an
// attached named store: "gramaton-<store>". Store names are already
// validated against store.ValidateName's [a-zA-Z0-9_-] alphabet, so
// the composed name is safe as a CLI argument and a JSON key.
func storeMCPEntryName(storeName string) string {
	return "gramaton-" + storeName
}

// storeMCPArgs is the gramaton argv (after the binary name) a
// per-store MCP entry runs: `--store <name> mcp`.
func storeMCPArgs(storeName string) []string {
	return []string{"--store", storeName, "mcp"}
}

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

	// RegisterStore adds an attached named store's MCP entry to the
	// client's config: entry storeMCPEntryName(storeName) running
	// `gramaton --store <storeName> mcp`. Same result semantics as
	// Register. Used by the wizard's read-only attach route.
	RegisterStore(ctx context.Context, client DetectedClient, storeName string) (alreadyRegistered bool, err error)

	// RemoveStore removes a store's MCP entry from the client's config:
	// the default "gramaton" entry when storeName is empty, else the
	// "gramaton-<storeName>" entry. It enumerates by naming convention
	// first, so a foreign entry is never removed and an already-absent
	// entry is a no-op: removed=false, err=nil. The store-scoped
	// counterpart to Register/RegisterStore, used by SyncStoreHarness.
	RemoveStore(ctx context.Context, client DetectedClient, storeName string) (removed bool, err error)

	// ListEntries returns the gramaton-owned MCP entry names currently
	// registered with the client, enumerated by naming convention
	// ("gramaton" + "gramaton-<store>"). Used to report registration
	// state (store list) and find orphaned entries (store
	// sync-harness). Empty when the client has none.
	ListEntries(ctx context.Context, client DetectedClient) ([]string, error)
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

// RegisterStore dispatches to the harness's per-store registration
// strategy, mirroring Register.
func (DefaultMCPBackend) RegisterStore(ctx context.Context, client DetectedClient, storeName string) (bool, error) {
	h := harnessByName(client.Name)
	if h == nil || h.RegisterMCPStore == nil {
		return false, fmt.Errorf("unknown MCP client: %s", client.Name)
	}
	return h.RegisterMCPStore(ctx, client.Binary, storeName)
}

// RemoveStore removes the store's MCP entry from the client's config,
// reusing uninstall's enumerate-by-convention-then-remove machinery
// scoped to a single entry (removeStoreEntry). client.Binary is the
// path Detect resolved (empty for dir-detected Cursor, whose
// list/remove strategies ignore it).
func (DefaultMCPBackend) RemoveStore(ctx context.Context, client DetectedClient, storeName string) (bool, error) {
	h := harnessByName(client.Name)
	if h == nil {
		return false, fmt.Errorf("unknown MCP client: %s", client.Name)
	}
	return removeStoreEntry(ctx, h, client.Binary, storeEntryName(storeName))
}

// ListEntries dispatches to the harness's enumeration strategy. A
// harness without one (the registry allows nil) reports no entries
// rather than erroring, so a survey never fails on an un-enumerable
// harness.
func (DefaultMCPBackend) ListEntries(ctx context.Context, client DetectedClient) ([]string, error) {
	h := harnessByName(client.Name)
	if h == nil {
		return nil, fmt.Errorf("unknown MCP client: %s", client.Name)
	}
	if h.ListMCPEntries == nil {
		return nil, nil
	}
	return h.ListMCPEntries(ctx, client.Binary)
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
	return registerClaudeCodeEntry(ctx, claudeBin, "gramaton", []string{"mcp"})
}

// registerClaudeCodeEntry is registerWithClaudeCode generalized over
// the entry name and gramaton argv, shared with the read-only attach
// route's per-store registration.
func registerClaudeCodeEntry(ctx context.Context, claudeBin, entry string, args []string) (bool, error) {
	// Idempotency check: is the entry already in `claude mcp list`?
	// If claude's output format changes in a future version we might
	// false-negative here and re-add, which claude's own add command
	// will then reject -- surfaced cleanly to the user.
	//
	// A failed list (e.g., no MCP configured yet) is not a hard
	// error -- we just proceed to add. If the real failure mode was
	// something else (permissions, corrupted config), the add below
	// will surface it.
	if registered, err := verifyClaudeMCPEntry(ctx, claudeBin, entry); err == nil && registered {
		return true, nil
	}

	// claude mcp add --scope user <entry> gramaton -- <args...>
	addArgs := append([]string{"mcp", "add", "--scope", "user", entry, "gramaton", "--"}, args...)
	addCmd := exec.CommandContext(ctx, claudeBin, addArgs...)
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
	return registerCodexEntry(ctx, codexBin, "gramaton", []string{"mcp"})
}

// registerCodexEntry is registerWithCodex generalized over the entry
// name and gramaton argv, shared with the read-only attach route.
func registerCodexEntry(ctx context.Context, codexBin, entry string, args []string) (bool, error) {
	addArgs := append([]string{"mcp", "add", entry, "--", "gramaton"}, args...)
	addCmd := exec.CommandContext(ctx, codexBin, addArgs...)
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
	return registerKiroEntry(ctx, kiroBin, "gramaton", []string{"mcp"})
}

// registerKiroEntry is registerWithKiroCli generalized over the
// entry name and gramaton argv; same best-effort caveats.
func registerKiroEntry(ctx context.Context, kiroBin, entry string, args []string) (bool, error) {
	// Idempotency probe: try `kiro mcp list`. If it succeeds AND
	// mentions the entry name, assume already registered. If the
	// subcommand doesn't exist we'll fall through.
	listCmd := exec.CommandContext(ctx, kiroBin, "mcp", "list")
	if listOut, err := listCmd.CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(listOut), "\n") {
			if strings.Contains(strings.TrimSpace(line), entry) {
				return true, nil
			}
		}
	}

	addArgs := append([]string{"mcp", "add", "--scope", "user", entry, "gramaton", "--"}, args...)
	addCmd := exec.CommandContext(ctx, kiroBin, addArgs...)
	out, err := addCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("kiro mcp add failed (kiro-cli may use different MCP-registration syntax): %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

// verifyClaudeMCPRegistered surveys `claude mcp list` for a
// gramaton entry (a line starting "gramaton:", matching the
// documented list format -- see docs/benchmarks.md). Used both as
// registerWithClaudeCode's idempotency probe and as Step 5's
// registration survey.
func verifyClaudeMCPRegistered(ctx context.Context, bin string) (bool, error) {
	return verifyClaudeMCPEntry(ctx, bin, "gramaton")
}

// verifyClaudeMCPEntry is verifyClaudeMCPRegistered generalized over
// the entry name. Prefix-matching "<entry>:" keeps "gramaton" from
// false-positively matching a per-store "gramaton-<name>:" line.
func verifyClaudeMCPEntry(ctx context.Context, bin, entry string) (bool, error) {
	out, err := exec.CommandContext(ctx, bin, "mcp", "list").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("couldn't run `claude mcp list` to verify registration: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), entry+":") {
			return true, nil
		}
	}
	return false, nil
}

// verifyCodexMCPRegistered surveys `codex mcp list` for a gramaton
// entry. Substring match rather than a line-format assumption:
// codex's list output is tabular and its exact shape isn't pinned
// in our corpus, but the server NAME appearing anywhere in the
// output is a stable signal either way.
func verifyCodexMCPRegistered(ctx context.Context, bin string) (bool, error) {
	out, err := exec.CommandContext(ctx, bin, "mcp", "list").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("couldn't run `codex mcp list` to verify registration: %w", err)
	}
	return strings.Contains(string(out), "gramaton"), nil
}

// verifyCursorMCPRegistered checks ~/.cursor/mcp.json for a
// gramaton entry under mcpServers. A missing file simply means not
// registered (Cursor doesn't auto-create it); a present-but-
// unparseable file is an error worth surfacing.
func verifyCursorMCPRegistered(_ context.Context, _ string) (bool, error) {
	dir, err := cursorConfigDir()
	if err != nil {
		return false, err
	}
	mcpPath := filepath.Join(dir, "mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", mcpPath, err)
	}
	var parsed struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(stripBOM(raw), &parsed); err != nil {
		return false, fmt.Errorf("parse %s: %w", mcpPath, err)
	}
	_, ok := parsed.MCPServers["gramaton"]
	return ok, nil
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
	return registerCursorEntry("gramaton", []string{"mcp"})
}

// registerCursorEntry is registerWithCursor generalized over the
// entry name and gramaton argv, shared with the read-only attach
// route's per-store registration.
func registerCursorEntry(entry string, args []string) (bool, error) {
	dir, err := cursorConfigDir()
	if err != nil {
		return false, err
	}
	mcpPath := filepath.Join(dir, "mcp.json")

	existing := map[string]any{}
	var original []byte
	if raw, err := os.ReadFile(mcpPath); err == nil {
		original = raw
		if err := json.Unmarshal(stripBOM(raw), &existing); err != nil {
			return false, fmt.Errorf("parse %s: %w (won't touch a file we can't parse -- fix or remove it and re-run)", mcpPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", mcpPath, err)
	}

	// Wrong-TYPE envelope values get the won't-touch treatment, same
	// as unparseable JSON.
	servers, ok := existing["mcpServers"].(map[string]any)
	if !ok {
		if _, present := existing["mcpServers"]; present {
			return false, fmt.Errorf("%s has a non-object \"mcpServers\" value; won't touch it -- fix by hand", mcpPath)
		}
		servers = map[string]any{}
	}

	// Schema verified from vendor docs (cursor.com/docs/context/mcp,
	// 2026-05-24): standard mcpServers envelope, stdio transport.
	// "gramaton" unqualified relies on PATH, same trade-off as the
	// CLI-based registrations (documented at the top of this file).
	argsAny := make([]any, len(args))
	for i, a := range args {
		argsAny[i] = a
	}
	desired := map[string]any{
		"type":    "stdio",
		"command": "gramaton",
		"args":    argsAny,
	}

	if current, ok := servers[entry]; ok {
		curJSON, _ := json.Marshal(current)
		wantJSON, _ := json.Marshal(desired)
		if string(curJSON) == string(wantJSON) {
			return true, nil // already registered, byte-identical
		}
	}

	servers[entry] = desired
	existing["mcpServers"] = servers

	// Back up before rewriting, then atomic write. 0600 perms:
	// mcp.json can carry API keys in env/headers for other servers.
	if _, err := writeConfigBackup(mcpPath, original); err != nil {
		return false, err
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
