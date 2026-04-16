package hooks_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func scriptPath(t *testing.T, client, script string) string {
	t.Helper()
	// Resolve from test file location.
	path := filepath.Join(client, script)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("script not found: %s", path)
	}
	return path
}

func runHookScript(t *testing.T, client, script string, input map[string]any) (string, string, error) {
	t.Helper()
	path := scriptPath(t, client, script)

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	// Set up environment so scripts don't try to call real gramaton.
	tmpDir := t.TempDir()
	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(string(data))
	cmd.Env = append(os.Environ(),
		"HOME="+tmpDir,
		"GRAMATON_BIN=echo",  // Use echo as a no-op gramaton standin.
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

// --- Claude Code hooks ---

func TestClaudeCodeSessionStartExecutes(t *testing.T) {
	_, _, err := runHookScript(t, "claude-code", "session-start.sh", map[string]any{
		"session_id": "test-session-123",
	})
	if err != nil {
		t.Fatalf("session-start.sh failed: %v", err)
	}
}

func TestClaudeCodeSessionStartNoSessionID(t *testing.T) {
	_, _, err := runHookScript(t, "claude-code", "session-start.sh", map[string]any{})
	if err != nil {
		t.Fatalf("session-start.sh should exit 0 with no session_id: %v", err)
	}
}

func TestClaudeCodeStopIncrementsCounter(t *testing.T) {
	tmpDir := t.TempDir()
	counterDir := filepath.Join(tmpDir, ".gramaton", "hook-state")
	os.MkdirAll(counterDir, 0o755)

	path := scriptPath(t, "claude-code", "stop.sh")
	input, _ := json.Marshal(map[string]any{"session_id": "counter-test"})

	// Run stop 3 times.
	for i := 0; i < 3; i++ {
		cmd := exec.Command("bash", path)
		cmd.Stdin = strings.NewReader(string(input))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if err := cmd.Run(); err != nil {
			t.Fatalf("stop.sh run %d failed: %v", i+1, err)
		}
	}

	// Check counter file.
	counterFile := filepath.Join(counterDir, "counter-test.count")
	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	count := strings.TrimSpace(string(data))
	if count != "3" {
		t.Errorf("counter = %v, want 3", count)
	}
}

func TestClaudeCodePostCompactWritesFlag(t *testing.T) {
	tmpDir := t.TempDir()

	path := scriptPath(t, "claude-code", "post-compact.sh")
	input, _ := json.Marshal(map[string]any{"session_id": "compact-test"})

	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("post-compact.sh failed: %v", err)
	}

	// Check flag file.
	flagFile := filepath.Join(tmpDir, ".gramaton", "hook-state", "compact-test.compacted")
	if _, err := os.Stat(flagFile); err != nil {
		t.Errorf("compaction flag file not created: %v", err)
	}
}

func TestClaudeCodePreCompactExecutes(t *testing.T) {
	_, _, err := runHookScript(t, "claude-code", "pre-compact.sh", map[string]any{
		"session_id": "precompact-test",
	})
	// pre-compact calls gramaton session get which will "fail" with echo,
	// but the script should exit 0 regardless.
	if err != nil {
		t.Fatalf("pre-compact.sh failed: %v", err)
	}
}

// --- Kiro hooks ---

func TestKiroAgentSpawnExecutes(t *testing.T) {
	_, _, err := runHookScript(t, "kiro", "agent-spawn.sh", map[string]any{
		"session_id": "kiro-test-123",
	})
	if err != nil {
		t.Fatalf("agent-spawn.sh failed: %v", err)
	}
}

func TestKiroStopIncrementsCounter(t *testing.T) {
	tmpDir := t.TempDir()
	counterDir := filepath.Join(tmpDir, ".gramaton", "hook-state")
	os.MkdirAll(counterDir, 0o755)

	path := scriptPath(t, "kiro", "stop.sh")
	input, _ := json.Marshal(map[string]any{"session_id": "kiro-counter"})

	for i := 0; i < 5; i++ {
		cmd := exec.Command("bash", path)
		cmd.Stdin = strings.NewReader(string(input))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if err := cmd.Run(); err != nil {
			t.Fatalf("kiro stop.sh run %d: %v", i+1, err)
		}
	}

	data, _ := os.ReadFile(filepath.Join(counterDir, "kiro-counter.count"))
	if strings.TrimSpace(string(data)) != "5" {
		t.Errorf("counter = %v, want 5", strings.TrimSpace(string(data)))
	}
}

func TestKiroUserPromptNoReminderBelowThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	counterDir := filepath.Join(tmpDir, ".gramaton", "hook-state")
	os.MkdirAll(counterDir, 0o755)
	// Set counter to 3 (below default threshold of 10).
	os.WriteFile(filepath.Join(counterDir, "kiro-remind.count"), []byte("3"), 0o644)

	path := scriptPath(t, "kiro", "user-prompt-submit.sh")
	input, _ := json.Marshal(map[string]any{"session_id": "kiro-remind"})

	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("user-prompt-submit.sh: %v", err)
	}
	if stdout.String() != "" {
		t.Errorf("should not output reminder below threshold, got: %s", stdout.String())
	}
}

func TestKiroUserPromptReminderAtThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	counterDir := filepath.Join(tmpDir, ".gramaton", "hook-state")
	os.MkdirAll(counterDir, 0o755)
	// Set counter to 10 (at default threshold).
	os.WriteFile(filepath.Join(counterDir, "kiro-remind2.count"), []byte("10"), 0o644)

	path := scriptPath(t, "kiro", "user-prompt-submit.sh")
	input, _ := json.Marshal(map[string]any{"session_id": "kiro-remind2"})

	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("user-prompt-submit.sh: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "gramaton_session_prepare") {
		t.Errorf("expected reminder mentioning gramaton_session_prepare, got: %s", output)
	}

	// Counter should be reset.
	data, _ := os.ReadFile(filepath.Join(counterDir, "kiro-remind2.count"))
	if strings.TrimSpace(string(data)) != "0" {
		t.Errorf("counter should be reset to 0 after reminder, got: %s", strings.TrimSpace(string(data)))
	}
}
