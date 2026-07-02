// Package templates holds the canonical Gramaton agent-guidance
// prose that `gramaton init` installs into AI harness instruction
// files, plus the version stamp used to detect outdated installs.
//
// The guidance is single-sourced: guidance/base.md carries the
// shared prose (tool usage, retrieval triggers, session flow), and
// per-harness guidance/addendum_*.md files carry only genuinely
// divergent guidance (e.g. routing rules against a harness's own
// memory system). Two interpolation variables keep the base
// harness-neutral:
//
//	{{client_name}}        harness display name ("Claude Code")
//	{{mcp_reconnect_hint}} harness-specific reconnect instructions
//
// The package deliberately depends on nothing but the standard
// library, so server-side guidance-drift checks (GH issue #80) can
// import GuidanceVersion without dragging in the wizard.
package templates

import (
	_ "embed"
	"strings"
)

// GuidanceVersion identifies the current revision of the guidance
// prose. It is stamped into the BEGIN fence marker of installed
// instruction files (v=X.Y.Z) so a later `gramaton init` — or the
// server's drift check, GH issue #80 — can tell how far behind an
// install is without hashing content.
//
// Bump discipline (documented in CONTRIBUTING.md, "Guidance-template
// versioning"): PATCH for wording fixes, MINOR for guidance an agent
// should re-read (new/renamed tools, changed routing rules), MAJOR
// reserved post-1.0. A minor-or-more gap triggers the user-facing
// "re-run `gramaton init --force`" nudge.
const GuidanceVersion = "0.2.0"

// AddendumMarker is the placeholder line in base.md where a
// harness's addendum is substituted. It sits just after the
// introductory framing so routing rules register before the deeper
// tool guidance. Render strips the marker when no addendum applies.
const AddendumMarker = "<!-- CLIENT_ADDENDUM -->"

// base is the shared agent-usage guide. Per-harness divergence is
// expressed via the addendum files below — never by forking this
// prose. Content lives in .md files so editors syntax-highlight it
// and diffs read as prose rather than escaped Go strings.
//
//go:embed guidance/base.md
var base string

// AddendumClaudeCode carries Claude Code-specific guidance:
// the routing rule between Claude Code's harness-level auto-memory
// (~/.claude/projects/<slug>/memory/) and Gramaton.
//
//go:embed guidance/addendum_claude_code.md
var AddendumClaudeCode string

// AddendumKiro is reserved for Kiro-specific guidance. Intentionally
// empty (the marker is stripped at render time): Kiro integration
// work is deferred until later.
//
//go:embed guidance/addendum_kiro.md
var AddendumKiro string

// AddendumCodex carries Codex-specific guidance: the routing rule
// between Codex's native memory system (~/.codex/memories/) and
// Gramaton.
//
//go:embed guidance/addendum_codex.md
var AddendumCodex string

// readOnly is the sibling guidance variant installed by the wizard's
// read-only attach route in place of base.md: it states that the
// attached store is read-only for everything, lists the read surface
// that keeps working (search, inspect, explore, ...), and says the
// write tools are not registered. A standalone body (not an
// addendum): most of base.md is save/session/collection-write
// guidance that would be misleading against a frozen store.
//
//go:embed guidance/readonly.md
var readOnly string

// AddendumCursor is reserved for Cursor-specific guidance.
// Intentionally empty (the marker is stripped at render time):
// Cursor has no verified native long-term memory analogue to route
// around, and the universal subagent-delegation rule now lives in
// base.md. The SKILL.md install mechanics live in the install layer,
// not the prose.
//
//go:embed guidance/addendum_cursor.md
var AddendumCursor string

// Vars are the interpolation values substituted into the guidance
// prose at render time.
type Vars struct {
	// ClientName replaces {{client_name}}: the harness display name
	// as it should read mid-sentence ("Claude Code", "Codex").
	ClientName string

	// ReconnectHint replaces {{mcp_reconnect_hint}}: a short
	// harness-specific clause telling the user how to re-attach a
	// disconnected MCP server, e.g. "for Claude Code: `/mcp` in the
	// prompt". It is spliced into "ask them to reconnect (<hint>,
	// or confirm `gramaton serve` / `gramaton init` is running)",
	// so it must read as a parenthetical clause, no trailing
	// punctuation.
	ReconnectHint string

	// StoreName replaces {{store_name}}: the local named store an
	// attached read-only store was registered under. Only used by
	// the read-only variant (RenderReadOnly); base.md carries no
	// {{store_name}} marker because the writable guidance targets
	// the default store.
	StoreName string
}

// Render produces the final guidance body: the shared base with the
// addendum substituted at AddendumMarker (or the marker stripped
// when the addendum is empty) and interpolation variables applied.
//
// Output is LF-normalized regardless of checkout line endings (a
// Windows checkout with autocrlf=true embeds CRLF) and ends with
// exactly one trailing newline. Installers that need CRLF output
// own that conversion at write time.
func Render(addendum string, v Vars) string {
	body := strings.ReplaceAll(base, "\r\n", "\n")
	add := strings.TrimSpace(strings.ReplaceAll(addendum, "\r\n", "\n"))
	if add == "" {
		// Strip the marker line and the surrounding blank lines so
		// the rendered file doesn't carry a dangling HTML comment
		// when no addendum applies.
		body = strings.Replace(body, "\n"+AddendumMarker+"\n\n", "\n", 1)
	} else {
		body = strings.Replace(body, AddendumMarker, add, 1)
	}
	body = strings.ReplaceAll(body, "{{client_name}}", v.ClientName)
	body = strings.ReplaceAll(body, "{{mcp_reconnect_hint}}", v.ReconnectHint)
	return strings.TrimSpace(body) + "\n"
}

// RenderReadOnly produces the read-only guidance variant: the
// standalone readonly.md body with the interpolation variables
// applied. No addendum substitution -- the per-harness addenda are
// save-routing rules that don't apply to a store nothing here ever
// writes to. Same output normalization contract as Render.
func RenderReadOnly(v Vars) string {
	body := strings.ReplaceAll(readOnly, "\r\n", "\n")
	body = strings.ReplaceAll(body, "{{client_name}}", v.ClientName)
	body = strings.ReplaceAll(body, "{{mcp_reconnect_hint}}", v.ReconnectHint)
	body = strings.ReplaceAll(body, "{{store_name}}", v.StoreName)
	return strings.TrimSpace(body) + "\n"
}

// CustomAgents renders the guidance artifact for builders of custom
// agents — code calling a model API directly (Anthropic, Bedrock,
// or any provider) with Gramaton attached over MCP. There is no
// harness to detect and no wizard step; the rendered artifact is
// checked in at integration/custom-agents/system-prompt.md (pinned
// by the drift test in internal/setup) for builders to merge into
// their agent's system prompt.
func CustomAgents() string {
	return Render("", Vars{
		ClientName:    "agent",
		ReconnectHint: "re-establish your MCP client connection",
	})
}
