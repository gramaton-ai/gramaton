package cli

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestPruneBannerLeadsEverySurface pins the agent guard: the warning
// is the FIRST content of the long help and of the command constant
// every run prints first. An agent reading any prune output sees the
// stop instruction before anything actionable.
func TestPruneBannerLeadsEverySurface(t *testing.T) {
	if !strings.HasPrefix(pruneCmd.Long, pruneAgentBanner) {
		t.Fatal("--help output does not lead with the agent warning")
	}
	if !strings.Contains(pruneAgentBanner, "If you are an AI agent, stop") {
		t.Fatal("banner lost its stop instruction")
	}
}

// TestNoAutostartSentinelLifecycle pins the concurrent-process guard:
// a live holder suppresses auto-start, release removes it, and a
// stale sentinel (dead pid) is ignored rather than wedging auto-start
// forever after a crashed run.
func TestNoAutostartSentinelLifecycle(t *testing.T) {
	dir := t.TempDir()

	release, err := writeNoAutostartSentinel(dir)
	if err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	pid, held := noAutostartSentinelHolder(dir)
	if !held || pid != os.Getpid() {
		t.Fatalf("holder = %d/%v, want this process", pid, held)
	}
	release()
	if _, held := noAutostartSentinelHolder(dir); held {
		t.Fatal("sentinel survived release")
	}

	// Stale: a pid that cannot be alive.
	if err := os.WriteFile(noAutostartSentinelPath(dir), []byte(strconv.Itoa(1<<30)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, held := noAutostartSentinelHolder(dir); held {
		t.Fatal("stale sentinel treated as live")
	}
}
