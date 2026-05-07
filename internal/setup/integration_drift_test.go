package setup

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// integrationSnapshots maps each detected MCP client name to the
// repository-relative snapshot file that documents what `gramaton
// init` installs for that client. The snapshots are user-readable
// references linked from README.md and docs/integrator-guide.md;
// keeping them honest matters because integrators read them to
// understand the agent guidance.
//
// Paths are relative to the test file's package directory
// (internal/setup/) -- the test resolves them against the repo root
// at runtime.
var integrationSnapshots = map[string]string{
	"Claude Code": filepath.Join("..", "..", "integration", "claude-code", "CLAUDE.md"),
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
	for client, rel := range integrationSnapshots {
		t.Run(client, func(t *testing.T) {
			want := templateForClient(client)

			if *updateIntegration {
				if err := os.WriteFile(rel, []byte(want), 0o644); err != nil {
					t.Fatalf("write %s: %v", rel, err)
				}
				t.Logf("updated %s", rel)
				return
			}

			got, err := os.ReadFile(rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			// Normalize line endings so a Windows checkout with
			// autocrlf=true (CRLF on disk) matches the LF-only output
			// of templateForClient. Repository convention is LF.
			gotLF := strings.ReplaceAll(string(got), "\r\n", "\n")
			if gotLF != want {
				t.Fatalf("%s is out of sync with templateForClient(%q).\nFix: go test ./internal/setup -update-integration",
					rel, client)
			}
		})
	}
}
