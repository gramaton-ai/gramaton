package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gramaton-ai/gramaton/internal/setup/templates"
)

// Harness names. DetectedClient.Name, wizard output, and registry
// lookups all key off these exact strings — treat them as stable
// identifiers, not display copy.
const (
	harnessClaudeCode = "Claude Code"
	harnessKiroCLI    = "kiro-cli"
	harnessCodex      = "Codex"
)

// Harness declaratively describes one supported AI harness (MCP
// client): how to detect it, how to register Gramaton as an MCP
// server with it, where its agent-guidance file lives, and how its
// hook scripts are wired. Wizard steps iterate the registry instead
// of switching on client names, so adding a harness is an entry
// here plus an addendum template (plus, occasionally, a bespoke
// RegisterMCP strategy).
//
// The registry deliberately holds data and strategy references, not
// behavior: install mechanics (fenced merges, settings.json patching,
// proxy-script synthesis) stay in their existing homes and read the
// per-harness facts from here.
type Harness struct {
	// Name is the human-readable label shown in wizard output AND
	// the identifier used for registry lookups.
	Name string

	// DetectBinary is the PATH binary whose presence marks this
	// harness as installed. Empty when the harness has no CLI
	// binary (e.g. a GUI IDE) — DetectDir is consulted instead.
	DetectBinary string

	// DetectDir is a home-relative directory whose existence marks
	// the harness as installed, for harnesses without a PATH
	// binary. Ignored when DetectBinary is non-empty.
	DetectDir string

	// RegisterMCP registers gramaton as an MCP server with this
	// harness. bin is the detected binary path (empty for
	// dir-detected harnesses). Returns alreadyRegistered=true when
	// the entry was already present (a soft success the wizard
	// reports differently). Nil means Step 3 cannot register this
	// harness programmatically.
	RegisterMCP func(ctx context.Context, bin string) (alreadyRegistered bool, err error)

	// ManualMCPHint is the one-line manual-registration hint shown
	// when no clients are detected or the user skips Step 3.
	ManualMCPHint string

	// InstructionsRelPath locates the agent-guidance file this
	// harness reads, as path elements relative to the user's home
	// directory. Empty means Step 4 (instructions) cannot target
	// this harness.
	InstructionsRelPath []string

	// ConfigRootEnv names an environment variable that relocates
	// this harness's config root (Codex's CODEX_HOME). When the
	// variable is set and non-empty, its value replaces the FIRST
	// element of InstructionsRelPath -- the home-relative config
	// dir -- when resolving the instructions path. Requires
	// len(InstructionsRelPath) >= 2.
	ConfigRootEnv string

	// InstructionsLayout selects the install strategy for the
	// guidance file: fenced block inside a shared user-owned file,
	// or a whole file Gramaton owns end to end.
	InstructionsLayout instructionsLayout

	// Addendum is the harness-specific guidance block substituted
	// at the CLIENT_ADDENDUM marker in the base template. Empty
	// strips the marker (graceful default for harnesses with no
	// divergent guidance).
	Addendum string

	// ReconnectHint fills {{mcp_reconnect_hint}} in the guidance
	// prose: a short clause telling the user how to re-attach a
	// disconnected MCP server in this harness (e.g. "for Claude
	// Code: `/mcp` in the prompt"). Required when
	// InstructionsRelPath is set — an empty hint would leave the
	// variable unfilled in the installed file.
	ReconnectHint string

	// HookEmbedDir is the directory name under <configDir>/hooks/
	// that Materialize writes this harness's proxy scripts into
	// (matching the historical hooks/ layout at repo root). Empty
	// means no hook scripts are bundled for this harness.
	HookEmbedDir string

	// HookEvents are the lifecycle events Gramaton wires for this
	// harness. Order is stable for deterministic Materialize
	// output and test assertions.
	HookEvents []hookEventSpec

	// ProxyStyle selects which proxy-script variants Materialize
	// writes: .sh always (Claude Code bundles Git Bash on Windows),
	// .sh-or-.cmd by host OS (kiro: native Windows, no bash), or
	// both variants on every host (Codex: its hook config carries
	// command + commandWindows and picks at runtime).
	ProxyStyle proxyStyle

	// WireHooks patches this harness's hook configuration to route
	// lifecycle events at the materialized scripts, preserving
	// non-gramaton entries. Nil means Step 5 cannot wire hooks
	// automatically -- the wizard prints the script paths with
	// manual wiring guidance instead.
	WireHooks func(ctx context.Context, scriptPaths []string) (unchanged bool, err error)

	// HookConfigPathHint is the human-readable location WireHooks
	// patches (e.g. "~/.claude/settings.json"), for wizard success
	// copy. Required when WireHooks is set.
	HookConfigPathHint string
}

// harnesses is the registry of supported AI harnesses, in detection
// (and wizard display) order. Append-only ordering: the order is
// stable for a given machine, which matters for the wizard's
// checkbox display and for test assertions.
var harnesses = []Harness{
	{
		Name:          harnessClaudeCode,
		DetectBinary:  "claude",
		RegisterMCP:   registerWithClaudeCode,
		ManualMCPHint: "claude mcp add --scope user gramaton gramaton -- mcp",
		// Claude Code loads ~/.claude/CLAUDE.md as one merged
		// system-prompt piece; users routinely add their own
		// content alongside. Fence the managed region.
		InstructionsRelPath: []string{".claude", "CLAUDE.md"},
		InstructionsLayout:  fencedBlockInSharedFile,
		Addendum:            templates.AddendumClaudeCode,
		ReconnectHint:       "for Claude Code: `/mcp` in the prompt",
		HookEmbedDir:        "claude-code",
		HookEvents:          claudeCodeEvents,
		ProxyStyle:          proxyPosixOnly,
		WireHooks:           registerClaudeHooks,
		HookConfigPathHint:  "~/.claude/settings.json",
	},
	{
		Name: harnessKiroCLI,
		// KNOWN BUG (deliberately preserved): the shipping binary
		// is `kiro-cli`, not `kiro`, so this probe misses standard
		// installs (verified against kiro-cli 2.4.1, 2026-05-24;
		// tracked locally). The registration
		// syntax below is likewise stale (kiro-cli wants
		// --name/--scope global/--command/--args --force). Kiro
		// integration work is deferred until later; this entry
		// migrates the old behavior bug-for-bug rather than
		// half-fixing a parked integration.
		DetectBinary:  "kiro",
		RegisterMCP:   registerWithKiroCli,
		ManualMCPHint: "(kiro-cli's equivalent -- check `kiro mcp --help`)",
		// Kiro loads every .md in ~/.kiro/steering/ on session
		// start, so single-purpose files are the idiomatic shape.
		// Own gramaton.md entirely; users add siblings for their
		// own topics. Verified: https://kiro.dev/docs/cli/steering/
		InstructionsRelPath: []string{".kiro", "steering", "gramaton.md"},
		InstructionsLayout:  wholeFileOwned,
		Addendum:            templates.AddendumKiro,
		ReconnectHint:       "for kiro-cli: start a new session",
		HookEmbedDir:        "kiro",
		HookEvents:          kiroEvents,
		ProxyStyle:          proxyNativePerOS,
		// WireHooks nil: kiro's hook-config schema is per-agent and
		// a fresh install has no default-agent config to patch
		// (verified 2026-05-24). Parked with the
		// rest of the Kiro integration.
	},
	{
		Name:         harnessCodex,
		DetectBinary: "codex",
		RegisterMCP:  registerWithCodex,
		// Verified syntax (codex-cli 0.133.0): positional name, then
		// `--` separating the server command. No --scope flag; the
		// config.toml entry is user-global by nature.
		ManualMCPHint: "codex mcp add gramaton -- gramaton mcp",
		// Codex loads $CODEX_HOME/AGENTS.md (default ~/.codex/) as
		// user-global agent instructions, shared with user content --
		// same merged-file model as Claude Code's CLAUDE.md, so the
		// HTML-comment fence convention carries over unchanged.
		// Verified: developers.openai.com/codex/guides/agents-md
		// (2026-05-24).
		InstructionsRelPath: []string{".codex", "AGENTS.md"},
		ConfigRootEnv:       "CODEX_HOME",
		InstructionsLayout:  fencedBlockInSharedFile,
		Addendum:            templates.AddendumCodex,
		ReconnectHint:       "for Codex: check `codex mcp list`, then start a new session",
		HookEmbedDir:        "codex",
		HookEvents:          codexEvents,
		// Dual variant: Codex's hooks.json carries both command and
		// commandWindows per entry, selecting per-OS at runtime, so
		// both script variants must exist on disk regardless of the
		// host this wizard ran on.
		ProxyStyle:         proxyDualVariant,
		WireHooks:          registerCodexHooks,
		HookConfigPathHint: "~/.codex/hooks.json",
	},
}

// harnessByName returns the registry entry for a DetectedClient.Name
// (e.g. "Claude Code"), or nil for unknown names. Callers treat nil
// as "not a supported harness" and surface their own error.
func harnessByName(name string) *Harness {
	for i := range harnesses {
		if harnesses[i].Name == name {
			return &harnesses[i]
		}
	}
	return nil
}

// harnessByEmbedDir returns the registry entry whose hook scripts
// live at <configDir>/hooks/<dir> (e.g. "claude-code"), or nil when
// no harness claims that directory.
func harnessByEmbedDir(dir string) *Harness {
	for i := range harnesses {
		if harnesses[i].HookEmbedDir == dir && dir != "" {
			return &harnesses[i]
		}
	}
	return nil
}

// harnessManualHints returns the per-harness manual MCP-registration
// hint lines (indented for Paragraph rendering), in registry order.
// Used by Step 3's "no clients detected" and "user skipped" copy so
// the hints live in exactly one place.
func harnessManualHints() []string {
	var lines []string
	for _, h := range harnesses {
		if h.ManualMCPHint != "" {
			lines = append(lines, "  "+h.ManualMCPHint)
		}
	}
	return lines
}

// harnessNamesForProse joins registry display names with " or " for
// sentence use ("Claude Code or kiro-cli"). Revisit the joining if
// the registry grows past a comfortable sentence length.
func harnessNamesForProse() string {
	names := make([]string, 0, len(harnesses))
	for _, h := range harnesses {
		names = append(names, h.Name)
	}
	return strings.Join(names, " or ")
}

// detectHarness probes one harness. PATH-binary detection wins when
// configured; directory detection covers harnesses with no CLI
// binary (GUI IDEs whose only footprint is a config dir under the
// user's home).
func detectHarness(h Harness) (DetectedClient, bool) {
	if h.DetectBinary != "" {
		if p, err := exec.LookPath(h.DetectBinary); err == nil {
			return DetectedClient{Name: h.Name, Binary: p}, true
		}
		return DetectedClient{}, false
	}
	if h.DetectDir != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return DetectedClient{}, false
		}
		if fi, err := os.Stat(filepath.Join(home, h.DetectDir)); err == nil && fi.IsDir() {
			// Binary stays empty: there is no executable to record
			// for dir-detected harnesses, and RegisterMCP strategies
			// for them must not assume one.
			return DetectedClient{Name: h.Name}, true
		}
	}
	return DetectedClient{}, false
}
