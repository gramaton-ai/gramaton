package templates

import (
	"regexp"
	"strings"
	"testing"
)

func TestGuidanceVersionIsSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(GuidanceVersion) {
		t.Errorf("GuidanceVersion = %q, want MAJOR.MINOR.PATCH", GuidanceVersion)
	}
}

// TestBaseEmbedded catches empty-embed bugs and pins the structural
// pieces Render depends on: the addendum marker and both
// interpolation variables must be present in the source prose.
func TestBaseEmbedded(t *testing.T) {
	if len(base) == 0 {
		t.Fatal("base is empty — //go:embed directive broken")
	}
	for _, want := range []string{
		"## Knowledge Store (Gramaton)",
		"gramaton_search",
		AddendumMarker,
		"{{client_name}}",
		"{{mcp_reconnect_hint}}",
	} {
		if !strings.Contains(base, want) {
			t.Errorf("base.md missing %q", want)
		}
	}
	// The base must stay harness-neutral: any harness-specific
	// wording belongs in an addendum or an interpolation variable.
	if strings.Contains(base, "Claude Code") {
		t.Error("base.md hardcodes \"Claude Code\" — move it behind {{client_name}} / {{mcp_reconnect_hint}} or into the addendum")
	}
}

func TestRenderSubstitutesAddendumAndVars(t *testing.T) {
	got := Render("### Test addendum body", Vars{
		ClientName:    "TestClient",
		ReconnectHint: "wiggle the cable",
	})
	for _, want := range []string{
		"### Test addendum body",
		"TestClient instances",
		"reconnect (wiggle the cable, or",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
	if strings.Contains(got, AddendumMarker) {
		t.Error("rendered output still carries the addendum marker")
	}
	if strings.Contains(got, "{{") {
		t.Error("rendered output has unfilled interpolation vars")
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Error("rendered output must end with exactly one newline")
	}
}

func TestRenderEmptyAddendumStripsMarker(t *testing.T) {
	got := Render("", Vars{ClientName: "X", ReconnectHint: "y"})
	if strings.Contains(got, AddendumMarker) {
		t.Error("empty addendum should strip the marker line")
	}
	if strings.Contains(got, "<!--") {
		t.Errorf("no HTML comments should survive an empty-addendum render")
	}
}

// TestCustomAgentsRenderClean pins the checked-in custom-agents
// artifact's properties: fully interpolated, marker-free, and free
// of any specific harness's name. The harness-name ban is
// case-insensitive so "claude" or "~/.codex/" can't slip through in
// lowercase prose.
func TestCustomAgentsRenderClean(t *testing.T) {
	got := CustomAgents()
	if !strings.Contains(got, "## Knowledge Store (Gramaton)") {
		t.Error("custom-agents render missing base content")
	}
	for _, banned := range []string{"{{", strings.ToLower(AddendumMarker)} {
		if strings.Contains(strings.ToLower(got), banned) {
			t.Errorf("custom-agents render must not contain %q", banned)
		}
	}
	// "Cursor" stays case-sensitive: lowercase "cursor" is a normal
	// English word that could legitimately appear in prose.
	lower := strings.ToLower(got)
	for _, banned := range []string{"claude", "codex", "kiro"} {
		if strings.Contains(lower, banned) {
			t.Errorf("custom-agents render must not mention harness %q (case-insensitive)", banned)
		}
	}
	if strings.Contains(got, "Cursor") {
		t.Error("custom-agents render must not mention harness \"Cursor\"")
	}
}

// TestReadOnlyVariantEmbedded catches empty-embed bugs on the
// read-only guidance variant and pins its load-bearing content: the
// read-only statement, the read surface, and the write-tool absence.
func TestReadOnlyVariantEmbedded(t *testing.T) {
	if len(readOnly) == 0 {
		t.Fatal("readonly is empty — //go:embed directive broken")
	}
	for _, want := range []string{
		"read-only",
		"gramaton_search",
		"gramaton_inspect",
		"gramaton_explore",
		"nothing is ever saved",
		"{{mcp_reconnect_hint}}",
		"{{store_name}}",
	} {
		if !strings.Contains(readOnly, want) {
			t.Errorf("readonly.md missing %q", want)
		}
	}
	got := RenderReadOnly(Vars{ClientName: "X", ReconnectHint: "wiggle the cable", StoreName: "shared"})
	if strings.Contains(got, "{{") {
		t.Error("rendered read-only variant has unfilled interpolation vars")
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Error("rendered read-only variant must end with exactly one newline")
	}
}

// TestAddendaEmbedded pins each addendum's load-bearing content (or
// deliberate emptiness).
func TestAddendaEmbedded(t *testing.T) {
	if !strings.Contains(AddendumClaudeCode, "auto-memory") {
		t.Error("Claude Code addendum missing the auto-memory routing rule")
	}
	if !strings.Contains(AddendumCodex, "~/.codex/memories/") {
		t.Error("Codex addendum missing the native-memories routing rule")
	}
	// The subagent-delegation rule is now uniform across every
	// supported harness (all inherit the parent's MCP tools), so it
	// lives once in base.md rather than being duplicated per addendum.
	if !strings.Contains(base, "### Subagents and Gramaton") {
		t.Error("base.md missing the universal subagents section")
	}
	for name, addendum := range map[string]string{
		"Claude Code": AddendumClaudeCode,
		"Codex":       AddendumCodex,
		"Cursor":      AddendumCursor,
		"Kiro":        AddendumKiro,
	} {
		if strings.Contains(addendum, "### Subagents and Gramaton") {
			t.Errorf("%s addendum still carries the subagents section; it now belongs in base.md only", name)
		}
	}
	// Cursor and Kiro addenda carry no harness-specific prose -- the
	// marker is stripped at render time. If either grows content,
	// update these assertions deliberately.
	if strings.TrimSpace(AddendumCursor) != "" {
		t.Error("Cursor addendum should be empty now that the subagent rule lives in base.md")
	}
	if strings.TrimSpace(AddendumKiro) != "" {
		t.Error("Kiro addendum should be empty while Kiro work is parked; if it grew content, update this test deliberately")
	}
}
