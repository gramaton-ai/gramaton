package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubGramaton swaps RunGramaton for the test, returning (output,
// err) from the provided closure. Restored via t.Cleanup.
func stubGramaton(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	orig := RunGramaton
	RunGramaton = fn
	t.Cleanup(func() { RunGramaton = orig })
}

func TestClaudeCodeSessionStartHappyPath(t *testing.T) {
	tmp := withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"01K0GRAMATONSESSION1234"}`, nil
	})

	stdin := strings.NewReader(`{"session_id":"client-abc","cwd":"/Users/b/project","source":"resume"}`)
	var stdout bytes.Buffer
	ClaudeCodeSessionStart(stdin, &stdout)

	// gramaton session start --client-id client-abc --source resume
	want := []string{"session", "start", "--client-id", "client-abc", "--source", "resume"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}

	// Shared current-session.json written with gramaton + client ids.
	shared := filepath.Join(tmp, ".gramaton", "hook-state", "current-session.json")
	data, err := os.ReadFile(shared)
	if err != nil {
		t.Fatalf("current-session.json not written: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["session_id"] != "01K0GRAMATONSESSION1234" {
		t.Errorf("gramaton session_id = %q", got["session_id"])
	}
	if got["client_session_id"] != "client-abc" {
		t.Errorf("client_session_id = %q", got["client_session_id"])
	}

	// Per-cwd file written.
	byCwd := filepath.Join(tmp, ".gramaton", "hook-state", "by-cwd", "Users-b-project.session.json")
	byCwdData, err := os.ReadFile(byCwd)
	if err != nil {
		t.Fatalf("per-cwd file not written: %v", err)
	}
	var byCwdPayload map[string]string
	_ = json.Unmarshal(byCwdData, &byCwdPayload)
	if byCwdPayload["cwd"] != "/Users/b/project" {
		t.Errorf("per-cwd payload missing cwd: %v", byCwdPayload)
	}

	// Counter reset to 0.
	if got := ReadCounter("client-abc"); got != 0 {
		t.Errorf("counter after session-start = %d, want 0", got)
	}
}

func TestClaudeCodeSessionStartDefaultSource(t *testing.T) {
	withTempHome(t)
	var gotArgs []string
	stubGramaton(t, func(args ...string) (string, error) {
		gotArgs = args
		return `{"id":"g1"}`, nil
	})

	ClaudeCodeSessionStart(strings.NewReader(`{"session_id":"client-abc"}`), &bytes.Buffer{})

	// Default source is "startup".
	for i, a := range gotArgs {
		if a == "--source" && i+1 < len(gotArgs) && gotArgs[i+1] != "startup" {
			t.Errorf("default source = %q, want startup", gotArgs[i+1])
		}
	}
}

func TestClaudeCodeSessionStartRejectsUnsafeSessionID(t *testing.T) {
	withTempHome(t)
	called := false
	stubGramaton(t, func(args ...string) (string, error) {
		called = true
		return "", nil
	})
	ClaudeCodeSessionStart(strings.NewReader(`{"session_id":"../escape"}`), &bytes.Buffer{})
	if called {
		t.Error("RunGramaton should not be called for unsafe session_id")
	}
}

func TestClaudeCodeSessionStartMissingSessionID(t *testing.T) {
	withTempHome(t)
	called := false
	stubGramaton(t, func(args ...string) (string, error) {
		called = true
		return "", nil
	})
	ClaudeCodeSessionStart(strings.NewReader(`{}`), &bytes.Buffer{})
	if called {
		t.Error("RunGramaton should not be called when session_id is missing")
	}
}

func TestClaudeCodeStopIncrementsCounter(t *testing.T) {
	withTempHome(t)
	ClaudeCodeStop(strings.NewReader(`{"session_id":"client-abc"}`), &bytes.Buffer{})
	if got := ReadCounter("client-abc"); got != 1 {
		t.Errorf("counter after stop = %d, want 1", got)
	}
	ClaudeCodeStop(strings.NewReader(`{"session_id":"client-abc"}`), &bytes.Buffer{})
	if got := ReadCounter("client-abc"); got != 2 {
		t.Errorf("counter after second stop = %d, want 2", got)
	}
}

func TestClaudeCodeStopIgnoresMissingSessionID(t *testing.T) {
	withTempHome(t)
	ClaudeCodeStop(strings.NewReader(`{}`), &bytes.Buffer{})
	// No panic, silent exit. Nothing else to assert.
}

func TestClaudeCodePostCompactWritesFlag(t *testing.T) {
	tmp := withTempHome(t)
	ClaudeCodePostCompact(strings.NewReader(`{"session_id":"client-abc"}`), &bytes.Buffer{})
	flag := filepath.Join(tmp, ".gramaton", "hook-state", "client-abc.compacted")
	data, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("flag not written: %v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Error("flag file empty, want RFC3339 timestamp")
	}
}

func TestClaudeCodePreCompactUncapturedWritesFlag(t *testing.T) {
	tmp := withTempHome(t)
	// Seed the current-session.json so pre-compact can resolve the
	// gramaton session id.
	shared := filepath.Join(tmp, ".gramaton", "hook-state", "current-session.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(`{"session_id":"g-123","client_session_id":"client-abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	stubGramaton(t, func(args ...string) (string, error) {
		calls++
		switch args[1] {
		case "get":
			// 3 uncaptured segments across 2 topics.
			return `{
                "topics": [
                    {"segments": [
                        {"captured_as": "mem-1"},
                        {"captured_as": ""}
                    ]},
                    {"segments": [
                        {"captured_as": ""},
                        {"captured_as": ""}
                    ]}
                ]
            }`, nil
		case "archive":
			return `{"archive_path":"/tmp/archive-123.jsonl"}`, nil
		}
		return "", nil
	})

	stdin := strings.NewReader(`{"session_id":"client-abc","transcript_path":"/tmp/transcript.jsonl"}`)
	ClaudeCodePreCompact(stdin, &bytes.Buffer{})

	flag := filepath.Join(tmp, ".gramaton", "hook-state", "client-abc.precompact-uncaptured")
	data, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("flag not written: %v", err)
	}
	var got preCompactFlag
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 {
		t.Errorf("uncaptured count = %d, want 3", got.Count)
	}
	if got.ArchivePath != "/tmp/archive-123.jsonl" {
		t.Errorf("archive_path = %q", got.ArchivePath)
	}
	if got.WarnedAt == "" {
		t.Error("warned_at empty")
	}
	if calls != 2 {
		t.Errorf("RunGramaton call count = %d, want 2 (get + archive)", calls)
	}
}

func TestClaudeCodePreCompactNoUncapturedSkipsArchive(t *testing.T) {
	tmp := withTempHome(t)
	shared := filepath.Join(tmp, ".gramaton", "hook-state", "current-session.json")
	_ = os.MkdirAll(filepath.Dir(shared), 0o700)
	_ = os.WriteFile(shared, []byte(`{"session_id":"g-123"}`), 0o600)

	calls := 0
	stubGramaton(t, func(args ...string) (string, error) {
		calls++
		if args[1] == "get" {
			return `{"topics":[{"segments":[{"captured_as":"mem-1"}]}]}`, nil
		}
		t.Errorf("unexpected call: %v", args)
		return "", nil
	})
	ClaudeCodePreCompact(strings.NewReader(`{"session_id":"client-abc","transcript_path":"/tmp/x"}`), &bytes.Buffer{})

	if calls != 1 {
		t.Errorf("got %d calls, want 1 (session get only)", calls)
	}
	// Flag file should NOT exist.
	flag := filepath.Join(tmp, ".gramaton", "hook-state", "client-abc.precompact-uncaptured")
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Error("flag file written when there were no uncaptured segments")
	}
}

func TestCountUncaptured(t *testing.T) {
	cases := []struct {
		name, input string
		want        int
	}{
		{"empty", `{}`, 0},
		{"all captured", `{"topics":[{"segments":[{"captured_as":"m1"}]}]}`, 0},
		{"none captured", `{"topics":[{"segments":[{"captured_as":""},{"captured_as":""}]}]}`, 2},
		{"mixed", `{"topics":[{"segments":[{"captured_as":"m"},{"captured_as":""}]},{"segments":[{"captured_as":""}]}]}`, 2},
		{"malformed", `not json`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countUncaptured(tc.input); got != tc.want {
				t.Errorf("countUncaptured = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"single", "single"},
		{"first\nsecond", "first"},
		{"  leading space\nafter", "leading space"},
		{"\nfirst-empty", ""},
	}
	for _, tc := range cases {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClaudeCodeUserPromptSubmitBelowThresholdNoReminder(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("cc-sess-1", 5) // below default threshold 10
	var stdout bytes.Buffer
	ClaudeCodeUserPromptSubmit(strings.NewReader(`{"session_id":"cc-sess-1"}`), &stdout)
	if stdout.Len() > 0 {
		t.Errorf("stdout should be empty below threshold, got %q", stdout.String())
	}
	if got := ReadCounter("cc-sess-1"); got != 5 {
		t.Errorf("counter = %d, want 5 (preserved)", got)
	}
}

func TestClaudeCodeUserPromptSubmitAtThresholdInjectsReminderAndResets(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("cc-sess-1", 10)
	var stdout bytes.Buffer
	ClaudeCodeUserPromptSubmit(strings.NewReader(`{"session_id":"cc-sess-1"}`), &stdout)
	out := stdout.String()
	if !strings.Contains(out, "Gramaton reminder") {
		t.Errorf("stdout missing reminder text: %q", out)
	}
	if !strings.Contains(out, "gramaton_session_prepare") {
		t.Errorf("reminder should name the prepare tool: %q", out)
	}
	if got := ReadCounter("cc-sess-1"); got != 0 {
		t.Errorf("counter = %d, want 0 (reset after injection)", got)
	}
}

func TestClaudeCodeUserPromptSubmitHonorsEnvThreshold(t *testing.T) {
	withTempHome(t)
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "3")
	_ = WriteCounter("cc-sess-1", 3)
	var stdout bytes.Buffer
	ClaudeCodeUserPromptSubmit(strings.NewReader(`{"session_id":"cc-sess-1"}`), &stdout)
	if !strings.Contains(stdout.String(), "Gramaton reminder") {
		t.Errorf("reminder should fire at env threshold 3: %q", stdout.String())
	}
}

func TestClaudeCodeUserPromptSubmitSilentOnMissingOrUnsafeID(t *testing.T) {
	withTempHome(t)
	// Threshold 1 plus a pre-seeded counter for each rejected id
	// makes the validation guards the ONLY thing suppressing output:
	// without this seeding, ReadCounter's zero default exits at the
	// threshold check and the test passes even with the guards
	// deleted.
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "1")
	for _, tc := range []struct{ input, id string }{
		{`{}`, ""},
		{`{"session_id":"../escape"}`, "../escape"},
	} {
		_ = WriteCounter(tc.id, 5)
		var stdout bytes.Buffer
		ClaudeCodeUserPromptSubmit(strings.NewReader(tc.input), &stdout)
		if stdout.Len() > 0 {
			t.Errorf("input %s: stdout should be empty, got %q", tc.input, stdout.String())
		}
	}
}
