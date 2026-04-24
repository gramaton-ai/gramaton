package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidSessionID(t *testing.T) {
	good := []string{
		"abc", "ABC", "0", "123", "a_b-c", "session-123", "01KPED88HKK12345",
	}
	bad := []string{
		"", "a/b", "a b", "a.b", "a;b", "../foo", "a\\b", "a;rm -rf /",
	}
	for _, s := range good {
		if !ValidSessionID(s) {
			t.Errorf("ValidSessionID(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidSessionID(s) {
			t.Errorf("ValidSessionID(%q) = true, want false", s)
		}
	}
}

func TestCwdSlug(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"unix root", "/foo/bar/baz", "foo-bar-baz"},
		{"unix with dots", "/Users/b/workspaces/gramaton", "Users-b-workspaces-gramaton"},
		{"windows drive", `C:\Users\b\foo`, "C-Users-b-foo"},
		{"windows forward-slashes already", "C:/Users/b/foo", "C-Users-b-foo"},
		{"empty", "", ""},
		{"just drive", "C:", "C-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CwdSlug(tc.in)
			if got != tc.want {
				t.Errorf("CwdSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecodeInputEmpty(t *testing.T) {
	in, err := DecodeInput(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty input should not error, got %v", err)
	}
	if in.SessionID != "" || in.AgentID != "" {
		t.Errorf("zero-value input expected, got %+v", in)
	}
}

func TestDecodeInputSessionID(t *testing.T) {
	in, err := DecodeInput(strings.NewReader(`{"session_id":"abc","cwd":"/tmp","source":"resume"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionID != "abc" || in.Cwd != "/tmp" || in.Source != "resume" {
		t.Errorf("unexpected decode: %+v", in)
	}
}

func TestDecodeInputMalformed(t *testing.T) {
	_, err := DecodeInput(strings.NewReader(`{not json`))
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestResolvedSessionIDPrefersSessionID(t *testing.T) {
	h := HookInput{SessionID: "primary", AgentID: "fallback"}
	if got := h.ResolvedSessionID(); got != "primary" {
		t.Errorf("resolved = %q, want primary", got)
	}
}

func TestResolvedSessionIDFallsBackToAgentID(t *testing.T) {
	h := HookInput{AgentID: "fallback"}
	if got := h.ResolvedSessionID(); got != "fallback" {
		t.Errorf("resolved = %q, want fallback", got)
	}
}

func TestResolvedSessionIDEmpty(t *testing.T) {
	h := HookInput{}
	if got := h.ResolvedSessionID(); got != "" {
		t.Errorf("resolved = %q, want empty", got)
	}
}

// withTempHome points HOME at a fresh temp dir for the duration of
// the test, so state functions don't touch the real ~/.gramaton/.
// Returns the temp dir.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}
	t.Setenv("HOME", tmp)
	return tmp
}

func TestCounterReadMissing(t *testing.T) {
	withTempHome(t)
	if got := ReadCounter("session-abc"); got != 0 {
		t.Errorf("ReadCounter on missing = %d, want 0", got)
	}
}

func TestCounterReadCorrupt(t *testing.T) {
	withTempHome(t)
	p, err := CounterPath("session-abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadCounter("session-abc"); got != 0 {
		t.Errorf("ReadCounter on corrupt = %d, want 0", got)
	}
}

func TestCounterWriteAndRead(t *testing.T) {
	withTempHome(t)
	if err := WriteCounter("session-abc", 42); err != nil {
		t.Fatal(err)
	}
	if got := ReadCounter("session-abc"); got != 42 {
		t.Errorf("ReadCounter = %d, want 42", got)
	}
}

func TestIncrementCounter(t *testing.T) {
	withTempHome(t)
	for i := 1; i <= 5; i++ {
		n, err := IncrementCounter("session-abc")
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Errorf("IncrementCounter step %d = %d, want %d", i, n, i)
		}
	}
}

func TestResetCounter(t *testing.T) {
	withTempHome(t)
	_ = WriteCounter("session-abc", 17)
	if err := ResetCounter("session-abc"); err != nil {
		t.Fatal(err)
	}
	if got := ReadCounter("session-abc"); got != 0 {
		t.Errorf("after reset = %d, want 0", got)
	}
}

func TestExtractThresholdDefault(t *testing.T) {
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "")
	if got := ExtractThreshold(); got != 10 {
		t.Errorf("default threshold = %d, want 10", got)
	}
}

func TestExtractThresholdEnvOverride(t *testing.T) {
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "5")
	if got := ExtractThreshold(); got != 5 {
		t.Errorf("env threshold = %d, want 5", got)
	}
}

func TestExtractThresholdInvalidEnv(t *testing.T) {
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "not-a-number")
	if got := ExtractThreshold(); got != 10 {
		t.Errorf("invalid env should fall back to 10, got %d", got)
	}
	t.Setenv("GRAMATON_EXTRACT_INTERVAL", "-5")
	if got := ExtractThreshold(); got != 10 {
		t.Errorf("negative env should fall back to 10, got %d", got)
	}
}

func TestLoggerWritesFormattedLines(t *testing.T) {
	tmp := withTempHome(t)
	logger := OpenLogger("test-hook")
	logger.Info("hello %s", "world")
	logger.Close()

	data, err := os.ReadFile(filepath.Join(tmp, ".gramaton", "hooks.log"))
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	if !strings.Contains(line, "test-hook: hello world") {
		t.Errorf("log line missing tag + message: %q", line)
	}
	if !strings.Contains(line, "[gramaton-hook]") {
		t.Errorf("log line missing prefix: %q", line)
	}
}

func TestLoggerAppends(t *testing.T) {
	withTempHome(t)
	l1 := OpenLogger("t")
	l1.Info("first")
	l1.Close()
	l2 := OpenLogger("t")
	l2.Info("second")
	l2.Close()

	base, _ := GramatonDir()
	data, err := os.ReadFile(filepath.Join(base, "hooks.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("first")) || !bytes.Contains(data, []byte("second")) {
		t.Errorf("log didn't accumulate both lines: %q", string(data))
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	withTempHome(t)
	dir, _ := HookStateDir()
	p := filepath.Join(dir, "test.json")

	payload := map[string]any{"key": "value", "n": 42}
	if err := WriteJSON(p, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key":"value"`) {
		t.Errorf("unexpected JSON: %s", data)
	}
}

func TestHookStateDirCreates(t *testing.T) {
	tmp := withTempHome(t)
	dir, err := HookStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, tmp) {
		t.Errorf("hook state dir %q not under temp HOME %q", dir, tmp)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("hook state dir not created: %v", err)
	}
}

func TestPerCwdSessionPath(t *testing.T) {
	withTempHome(t)
	p, err := PerCwdSessionPath("/Users/b/project")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "Users-b-project.session.json") {
		t.Errorf("PerCwdSessionPath = %q; expected slug suffix", p)
	}
}
