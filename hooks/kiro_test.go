package hooks

import (
	"bytes"
	"strings"
	"testing"
)

func TestKiroAgentSpawnHappyPath(t *testing.T) {
	withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"g-kiro-1"}`, nil
	})

	KiroAgentSpawn(strings.NewReader(`{"session_id":"agent-abc"}`), &bytes.Buffer{})

	want := []string{"session", "start", "--client-id", "agent-abc"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
	if got := ReadCounter("agent-abc"); got != 0 {
		t.Errorf("counter = %d, want 0 (reset)", got)
	}
}

func TestKiroAgentSpawnFallbackToAgentID(t *testing.T) {
	withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"g"}`, nil
	})
	// Kiro may send agent_id instead of session_id.
	KiroAgentSpawn(strings.NewReader(`{"agent_id":"agent-xyz"}`), &bytes.Buffer{})
	for i := 0; i+1 < len(gotArgs); i++ {
		if gotArgs[i] == "--client-id" && gotArgs[i+1] != "agent-xyz" {
			t.Errorf("client-id = %q, want agent-xyz (from agent_id fallback)", gotArgs[i+1])
		}
	}
}

func TestKiroAgentSpawnSessionIDPreferred(t *testing.T) {
	withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"g"}`, nil
	})
	// Both fields present — session_id wins.
	KiroAgentSpawn(strings.NewReader(`{"session_id":"primary","agent_id":"fallback"}`), &bytes.Buffer{})
	for i := 0; i+1 < len(gotArgs); i++ {
		if gotArgs[i] == "--client-id" && gotArgs[i+1] != "primary" {
			t.Errorf("client-id = %q, want primary", gotArgs[i+1])
		}
	}
}

func TestKiroAgentSpawnRejectsUnsafeID(t *testing.T) {
	withTempHome(t)
	called := false
	stubGramaton(t, func(args ...string) (string, error) {
		called = true
		return "", nil
	})
	KiroAgentSpawn(strings.NewReader(`{"session_id":"../escape"}`), &bytes.Buffer{})
	if called {
		t.Error("RunGramaton should not be called for unsafe session_id")
	}
}

func TestKiroUserPromptSubmitBelowThresholdNoReminder(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("agent-abc", 5) // below default threshold 10
	var stdout bytes.Buffer
	KiroUserPromptSubmit(strings.NewReader(`{"session_id":"agent-abc"}`), &stdout)
	if stdout.Len() > 0 {
		t.Errorf("stdout should be empty below threshold, got %q", stdout.String())
	}
	// Counter preserved.
	if got := ReadCounter("agent-abc"); got != 5 {
		t.Errorf("counter = %d, want 5 (preserved)", got)
	}
}

func TestKiroUserPromptSubmitAtThresholdInjectsReminder(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("agent-abc", 10)
	var stdout bytes.Buffer
	KiroUserPromptSubmit(strings.NewReader(`{"session_id":"agent-abc"}`), &stdout)
	out := stdout.String()
	if !strings.Contains(out, "Gramaton reminder") {
		t.Errorf("stdout missing reminder text: %q", out)
	}
	if !strings.Contains(out, "gramaton_session_prepare") {
		t.Errorf("reminder should mention gramaton_session_prepare: %q", out)
	}
	// Counter should reset after injection.
	if got := ReadCounter("agent-abc"); got != 0 {
		t.Errorf("counter = %d, want 0 (reset after reminder)", got)
	}
}

func TestKiroUserPromptSubmitHonorsEnvThreshold(t *testing.T) {
	withTempHome(t)
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "3")
	_ = WriteCounter("agent-abc", 3)
	var stdout bytes.Buffer
	KiroUserPromptSubmit(strings.NewReader(`{"session_id":"agent-abc"}`), &stdout)
	if !strings.Contains(stdout.String(), "Gramaton reminder") {
		t.Errorf("reminder should fire at env-overridden threshold 3")
	}
}

func TestKiroUserPromptSubmitFallbackToAgentID(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("agent-xyz", 10)
	var stdout bytes.Buffer
	KiroUserPromptSubmit(strings.NewReader(`{"agent_id":"agent-xyz"}`), &stdout)
	if !strings.Contains(stdout.String(), "Gramaton reminder") {
		t.Errorf("reminder should fire when id comes from agent_id")
	}
}

func TestKiroStopIncrementsCounter(t *testing.T) {
	withTempHome(t)
	KiroStop(strings.NewReader(`{"session_id":"agent-abc"}`), &bytes.Buffer{})
	KiroStop(strings.NewReader(`{"session_id":"agent-abc"}`), &bytes.Buffer{})
	KiroStop(strings.NewReader(`{"session_id":"agent-abc"}`), &bytes.Buffer{})
	if got := ReadCounter("agent-abc"); got != 3 {
		t.Errorf("counter = %d, want 3", got)
	}
}

func TestKiroStopSilentOnMissingID(t *testing.T) {
	withTempHome(t)
	KiroStop(strings.NewReader(`{}`), &bytes.Buffer{})
	// No panic, no side effects. Counter for "" shouldn't exist.
	if got := ReadCounter(""); got != 0 {
		t.Errorf("counter for empty id = %d, want 0", got)
	}
}
