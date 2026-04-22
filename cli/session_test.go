package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCwdSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/Users/test/workspaces/gramaton", "Users-test-workspaces-gramaton"},
		{"/", ""},
		{"", ""},
		{"/a", "a"},
		{"/a/b/c", "a-b-c"},
	}
	for _, c := range cases {
		if got := cwdSlug(c.in); got != c.want {
			t.Errorf("cwdSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveCurrentSession_PerCwdPreferred(t *testing.T) {
	base := t.TempDir()
	byCwd := filepath.Join(base, "hook-state", "by-cwd")
	if err := os.MkdirAll(byCwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := filepath.Join(base, "hook-state", "current-session.json")

	cwd := "/Users/test/proj-a"
	if err := os.WriteFile(filepath.Join(byCwd, cwdSlug(cwd)+".session.json"),
		[]byte(`{"session_id":"GRAMA-A","client_session_id":"CC-A","cwd":"/Users/test/proj-a"}`), 0o644); err != nil {
		t.Fatalf("write per-cwd: %v", err)
	}
	if err := os.WriteFile(legacy,
		[]byte(`{"session_id":"GRAMA-LEGACY","client_session_id":"CC-LEGACY"}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	parsed, path, err := resolveCurrentSession(base, cwd)
	if err != nil {
		t.Fatalf("resolveCurrentSession: %v", err)
	}
	if parsed["session_id"] != "GRAMA-A" {
		t.Errorf("expected per-cwd session_id GRAMA-A, got %v", parsed["session_id"])
	}
	if path != filepath.Join(byCwd, cwdSlug(cwd)+".session.json") {
		t.Errorf("expected per-cwd path, got %s", path)
	}
}

func TestResolveCurrentSession_FallsBackToLegacy(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "hook-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := filepath.Join(stateDir, "current-session.json")
	if err := os.WriteFile(legacy,
		[]byte(`{"session_id":"GRAMA-FB","client_session_id":"CC-FB"}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	parsed, path, err := resolveCurrentSession(base, "/Users/test/proj-noexist")
	if err != nil {
		t.Fatalf("resolveCurrentSession: %v", err)
	}
	if parsed["session_id"] != "GRAMA-FB" {
		t.Errorf("expected legacy session_id GRAMA-FB, got %v", parsed["session_id"])
	}
	if path != legacy {
		t.Errorf("expected legacy path, got %s", path)
	}
}

func TestResolveCurrentSession_BothMissing(t *testing.T) {
	base := t.TempDir()
	_, _, err := resolveCurrentSession(base, "/Users/test/whatever")
	if err == nil {
		t.Fatal("expected error when no session files exist")
	}
}

func TestResolveCurrentSession_NoCrossClobber(t *testing.T) {
	// Two concurrent Claude Code instances writing distinct per-cwd files
	// should each resolve to their own session, even though they share
	// the legacy current-session.json (last-writer-wins).
	base := t.TempDir()
	byCwd := filepath.Join(base, "hook-state", "by-cwd")
	if err := os.MkdirAll(byCwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cwdA, cwdB := "/Users/test/proj-a", "/Users/test/proj-b"
	if err := os.WriteFile(filepath.Join(byCwd, cwdSlug(cwdA)+".session.json"),
		[]byte(`{"session_id":"GRAMA-A","client_session_id":"CC-A"}`), 0o644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(byCwd, cwdSlug(cwdB)+".session.json"),
		[]byte(`{"session_id":"GRAMA-B","client_session_id":"CC-B"}`), 0o644); err != nil {
		t.Fatalf("write B: %v", err)
	}
	// Legacy file reflects the last writer (B), but per-cwd lookup still
	// distinguishes A and B.
	if err := os.WriteFile(filepath.Join(base, "hook-state", "current-session.json"),
		[]byte(`{"session_id":"GRAMA-B","client_session_id":"CC-B"}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	parsedA, _, err := resolveCurrentSession(base, cwdA)
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if parsedA["session_id"] != "GRAMA-A" {
		t.Errorf("cwd A resolved to %v, want GRAMA-A", parsedA["session_id"])
	}
	parsedB, _, err := resolveCurrentSession(base, cwdB)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	if parsedB["session_id"] != "GRAMA-B" {
		t.Errorf("cwd B resolved to %v, want GRAMA-B", parsedB["session_id"])
	}
}
