package setup

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/setup/templates"
)

// updateIntegration, when set, rewrites the integration/<client>/*.md
// snapshot files from the canonical templates instead of asserting
// equality. Standard golden-file pattern: edit the templates, run
//
//	go test ./internal/setup -update-integration
//
// to refresh the snapshots, then re-run without the flag to confirm
// the snapshots match.
var updateIntegration = flag.Bool(
	"update-integration",
	false,
	"rewrite integration/<client>/*.md snapshot files from canonical templates",
)

// integrationSnapshots maps each integration/<dir>/ snapshot file to
// the canonical renderer it must match. The snapshots are
// user-readable references linked from README.md and
// docs/integrator-guide.md; keeping them honest matters because
// integrators read them to understand the agent guidance.
//
// Most entries render through templateForClient (what `gramaton
// init` installs for a detected harness). The custom-agents entry
// has no harness behind it — it is the documentation-only artifact
// builders of bespoke agents merge into their own system prompts.
//
// Paths are relative to the test file's package directory
// (internal/setup/) -- the test resolves them against the repo root
// at runtime.
var integrationSnapshots = map[string]struct {
	rel    string
	render func() string
}{
	"claude-code": {
		rel:    filepath.Join("..", "..", "integration", "claude-code", "CLAUDE.md"),
		render: func() string { return templateForClient(harnessClaudeCode) },
	},
	"codex": {
		rel:    filepath.Join("..", "..", "integration", "codex", "AGENTS.md"),
		render: func() string { return templateForClient(harnessCodex) },
	},
	"custom-agents": {
		rel:    filepath.Join("..", "..", "integration", "custom-agents", "system-prompt.md"),
		render: templates.CustomAgents,
	},
}

// TestIntegrationSnapshotsMatchCanonical pins the snapshot files in
// integration/<client>/ to whatever templateForClient produces. Drift
// fails CI; the failure message points at the -update-integration
// flag for a one-command fix.
//
// The fenced installer (Claude Code) wraps content with BEGIN/END
// markers when it writes to ~/.claude/CLAUDE.md, but the snapshot
// shows just the body that lives between those markers -- that's
// what's useful for a reader who wants to understand the agent
// guidance without thinking about install mechanics.
func TestIntegrationSnapshotsMatchCanonical(t *testing.T) {
	for name, snap := range integrationSnapshots {
		t.Run(name, func(t *testing.T) {
			want := snap.render()

			if *updateIntegration {
				if err := os.MkdirAll(filepath.Dir(snap.rel), 0o755); err != nil {
					t.Fatalf("mkdir for %s: %v", snap.rel, err)
				}
				if err := os.WriteFile(snap.rel, []byte(want), 0o644); err != nil {
					t.Fatalf("write %s: %v", snap.rel, err)
				}
				t.Logf("updated %s", snap.rel)
				return
			}

			got, err := os.ReadFile(snap.rel)
			if err != nil {
				t.Fatalf("read %s: %v", snap.rel, err)
			}
			// Normalize line endings so a Windows checkout with
			// autocrlf=true (CRLF on disk) matches the LF-only output
			// of the renderers. Repository convention is LF.
			gotLF := strings.ReplaceAll(string(got), "\r\n", "\n")
			if gotLF != want {
				t.Fatalf("%s is out of sync with its canonical renderer.\nFix: go test ./internal/setup -update-integration",
					snap.rel)
			}
		})
	}
}
