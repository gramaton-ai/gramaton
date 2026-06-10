package hooks

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeCursorInput(t *testing.T) {
	in, err := decodeCursorInput(strings.NewReader(
		`{"conversation_id":"conv-123","workspace_roots":["/Users/b/project","/Users/b/other"],"transcript_path":"/tmp/t.md","hook_event_name":"sessionStart","cursor_version":"1.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionID != "conv-123" {
		t.Errorf("SessionID = %q, want conv-123 (conversation_id mapping)", in.SessionID)
	}
	if in.Cwd != "/Users/b/project" {
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

	stdin := strings.NewReader(`{"conversation_id":"conv-abc","workspace_roots":["/Users/b/project"]}`)
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
