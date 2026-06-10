package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeCursorInput(t *testing.T) {
	in, err := decodeCursorInput(strings.NewReader(
		`{"conversation_id":"conv-123","workspace_roots":["/Users/dev/project","/Users/dev/other"],"transcript_path":"/tmp/t.md","hook_event_name":"sessionStart","cursor_version":"1.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionID != "conv-123" {
		t.Errorf("SessionID = %q, want conv-123 (conversation_id mapping)", in.SessionID)
	}
	if in.Cwd != "/Users/dev/project" {
		t.Errorf("Cwd = %q, want first workspace root", in.Cwd)
	}
	if in.TranscriptPath != "/tmp/t.md" {
		t.Errorf("TranscriptPath = %q", in.TranscriptPath)
	}
}

func TestDecodeCursorInputEmptyAndMalformed(t *testing.T) {
	if in, err := decodeCursorInput(strings.NewReader("")); err != nil || in.SessionID != "" {
		t.Errorf("empty stdin should be zero value, no error; got %+v, %v", in, err)
	}
	if _, err := decodeCursorInput(strings.NewReader("{not json")); err == nil {
		t.Error("malformed JSON should error")
	}
	// No workspace roots: cwd stays empty rather than panicking.
	in, err := decodeCursorInput(strings.NewReader(`{"conversation_id":"c1","workspace_roots":[]}`))
	if err != nil || in.Cwd != "" {
		t.Errorf("empty workspace_roots: got cwd %q, err %v", in.Cwd, err)
	}
}

// TestCursorSessionStartHappyPath mirrors the Claude Code happy-path
// test through the Cursor adapter: conversation_id drives the
// --client-id, the first workspace root drives the per-cwd pointer
// file.
func TestCursorSessionStartHappyPath(t *testing.T) {
	withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"01K0GRAMATONSESSION1234"}`, nil
	})

	stdin := strings.NewReader(`{"conversation_id":"conv-abc","workspace_roots":["/Users/dev/project"]}`)
	CursorSessionStart(stdin, &bytes.Buffer{})

	// Cursor has no source field; the core defaults to "startup".
	want := []string{"session", "start", "--client-id", "conv-abc", "--source", "startup"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}

	// Counter reset by the shared core.
	if got := ReadCounter("conv-abc"); got != 0 {
		t.Errorf("counter after session-start = %d, want 0", got)
	}
}

func TestCursorSessionStartMissingConversationID(t *testing.T) {
	withTempHome(t)
	called := false
	stubGramaton(t, func(args ...string) (string, error) {
		called = true
		return "", nil
	})
	CursorSessionStart(strings.NewReader(`{"workspace_roots":["/x"]}`), &bytes.Buffer{})
	if called {
		t.Error("RunGramaton should not be called without a conversation_id")
	}
}

func TestCursorStopIncrementsCounter(t *testing.T) {
	withTempHome(t)
	CursorStop(strings.NewReader(`{"conversation_id":"conv-abc"}`), &bytes.Buffer{})
	if got := ReadCounter("conv-abc"); got != 1 {
		t.Errorf("counter after cursor stop = %d, want 1", got)
	}
}

// TestCursorPreCompactUncapturedWritesFlag pins the adapter wiring
// for preCompact end to end: Cursor's stdin shape drives the shared
// preCompactCore (session lookup, transcript archive, nudge flag).
// Without this, CursorPreCompact routing to the wrong core would
// pass the rest of the suite.
func TestCursorPreCompactUncapturedWritesFlag(t *testing.T) {
	tmp := withTempHome(t)

	// State as CursorSessionStart would have left it: the shared
	// pointer file mapping to a gramaton session id.
	stateDir := filepath.Join(tmp, ".gramaton", "hook-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cur := `{"session_id":"01KGRAMATONSESSION123","client_session_id":"conv-abc"}`
	if err := os.WriteFile(filepath.Join(stateDir, "current-session.json"), []byte(cur), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	stubGramaton(t, func(args ...string) (string, error) {
		calls = append(calls, args)
		if len(args) >= 2 && args[0] == "session" {
			switch args[1] {
			case "get":
				// One uncaptured segment -> archive + flag expected.
				return `{"topics":[{"segments":[{"captured_as":""},{"captured_as":"01XCAPTURED"}]}]}`, nil
			case "archive":
				return `{"archive_path":"/tmp/archive.jsonl.gz"}`, nil
			}
		}
		return "", nil
	})

	stdin := strings.NewReader(`{"conversation_id":"conv-abc","workspace_roots":["/x"],"transcript_path":"/tmp/transcript.jsonl"}`)
	CursorPreCompact(stdin, &bytes.Buffer{})

	if len(calls) != 2 {
		t.Fatalf("want 2 gramaton calls (get + archive), got %v", calls)
	}

	flagPath, err := PreCompactFlagPath("conv-abc")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(flagPath)
	if err != nil {
		t.Fatalf("precompact-uncaptured flag not written: %v", err)
	}
	var flag struct {
		Count       int    `json:"count"`
		ArchivePath string `json:"archive_path"`
	}
	if err := json.Unmarshal(raw, &flag); err != nil {
		t.Fatal(err)
	}
	if flag.Count != 1 {
		t.Errorf("flag count = %d, want 1", flag.Count)
	}
	if flag.ArchivePath != "/tmp/archive.jsonl.gz" {
		t.Errorf("flag archive_path = %q", flag.ArchivePath)
	}
}

// TestPreCompactPrefersPerCwdSession pins the multi-harness session
// resolution: when the payload carries a cwd whose per-cwd binding
// differs from the global pointer (another harness's session-start
// ran more recently elsewhere), the archive must target the per-cwd
// session, not the global one.
func TestPreCompactPrefersPerCwdSession(t *testing.T) {
	tmp := withTempHome(t)
	stateDir := filepath.Join(tmp, ".gramaton", "hook-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Global pointer: some OTHER harness's session won the last write.
	global := `{"session_id":"01KGLOBALOTHERSESSION","client_session_id":"other"}`
	if err := os.WriteFile(filepath.Join(stateDir, "current-session.json"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	// Per-cwd binding for OUR workspace.
	perCwdPath, err := PerCwdSessionPath("/Users/dev/project")
	if err != nil {
		t.Fatal(err)
	}
	perCwd := `{"session_id":"01KPERCWDSESSION","client_session_id":"conv-abc","cwd":"/Users/dev/project"}`
	if err := os.WriteFile(perCwdPath, []byte(perCwd), 0o600); err != nil {
		t.Fatal(err)
	}

	var getID string
	stubGramaton(t, func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "session" && args[1] == "get" {
			getID = args[2]
			// No uncaptured segments -> core exits before archiving.
			return `{"topics":[]}`, nil
		}
		return "", nil
	})

	stdin := strings.NewReader(`{"conversation_id":"conv-abc","workspace_roots":["/Users/dev/project"]}`)
	CursorPreCompact(stdin, &bytes.Buffer{})

	if getID != "01KPERCWDSESSION" {
		t.Errorf("session get targeted %q, want the per-cwd session (global pointer must not win)", getID)
	}
}
