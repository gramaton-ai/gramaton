package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestCheckPreflightConfigMissing(t *testing.T) {
	r := checkPreflightConfig("/no/such/path/config.yaml", os.ErrNotExist)
	if r.status != statusError {
		t.Errorf("status = %v, want statusError", r.status)
	}
	if !strings.Contains(r.message, "not found") {
		t.Errorf("message = %q, want contains 'not found'", r.message)
	}
	if !strings.Contains(r.remediation, "gramaton init") {
		t.Errorf("remediation should mention init: %q", r.remediation)
	}
}

func TestCheckPreflightConfigUnparseable(t *testing.T) {
	r := checkPreflightConfig("/some/path/config.yaml", errors.New("yaml: invalid character"))
	if r.status != statusError {
		t.Errorf("status = %v, want statusError", r.status)
	}
	if !strings.Contains(r.message, "failed to parse") {
		t.Errorf("message = %q, want contains 'failed to parse'", r.message)
	}
}

func TestCheckPreflightConfigOK(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := checkPreflightConfig(cfgPath, nil)
	if r.status != statusOK {
		t.Errorf("status = %v, want statusOK", r.status)
	}
}

func TestCheckPreflightDataDirOK(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	r := checkPreflightDataDir(cfg)
	if r.status != statusOK {
		t.Errorf("status = %v (%s), want statusOK", r.status, r.message)
	}
}

func TestCheckPreflightDataDirMissing(t *testing.T) {
	cfg := config.Config{DataDir: "/no/such/dir/under/preflight/test"}
	r := checkPreflightDataDir(cfg)
	if r.status != statusError {
		t.Errorf("status = %v, want statusError", r.status)
	}
	if !strings.Contains(r.message, "missing") {
		t.Errorf("message = %q, want contains 'missing'", r.message)
	}
}

func TestCheckPreflightEmbeddingProviders(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		wantStatus preflightStatus
		wantInMsg string
	}{
		{"bert", "bert", statusOK, "BERT"},
		{"ollama", "ollama", statusOK, "Ollama"},
		{"bedrock", "bedrock", statusOK, "Bedrock"},
		{"empty", "", statusWarn, "disabled"},
		{"unknown", "weird", statusWarn, "unknown provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{}
			cfg.Embedding.Provider = tc.provider
			r := checkPreflightEmbedding(cfg)
			if r.status != tc.wantStatus {
				t.Errorf("status = %v, want %v", r.status, tc.wantStatus)
			}
			if !strings.Contains(r.message, tc.wantInMsg) {
				t.Errorf("message = %q, want contains %q", r.message, tc.wantInMsg)
			}
		})
	}
}

func TestCheckPreflightEmbeddingOpenAIMissingKey(t *testing.T) {
	cfg := config.Config{}
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.APIKeyFile = "/no/such/path/openai.key"
	r := checkPreflightEmbedding(cfg)
	if r.status != statusError {
		t.Errorf("status = %v, want statusError", r.status)
	}
	if !strings.Contains(r.message, "missing") {
		t.Errorf("message = %q, want contains 'missing'", r.message)
	}
}

func TestCheckPreflightLLMNotConfigured(t *testing.T) {
	cfg := config.Config{}
	r := checkPreflightLLM(cfg)
	if r.status != statusWarn {
		t.Errorf("status = %v, want statusWarn", r.status)
	}
	if !strings.Contains(r.message, "not configured") {
		t.Errorf("message = %q", r.message)
	}
}

func TestCheckPreflightLLMAnthropicWithKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.APIKeyFile = keyPath
	r := checkPreflightLLM(cfg)
	if r.status != statusOK {
		t.Errorf("status = %v (%s), want statusOK", r.status, r.message)
	}
}

func TestCheckPreflightLLMMissingKeyFile(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.APIKeyFile = "/no/such/anthropic.key"
	r := checkPreflightLLM(cfg)
	if r.status != statusError {
		t.Errorf("status = %v, want statusError", r.status)
	}
}

func TestCheckPreflightLLMEnvUnset(t *testing.T) {
	t.Setenv("MY_TEST_KEY_THAT_IS_UNSET", "")
	cfg := config.Config{}
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.APIKeyEnv = "MY_TEST_KEY_THAT_IS_UNSET"
	r := checkPreflightLLM(cfg)
	if r.status != statusWarn {
		t.Errorf("status = %v, want statusWarn", r.status)
	}
	if !strings.Contains(r.message, "is not set") {
		t.Errorf("message = %q, want contains 'is not set'", r.message)
	}
}

func TestCheckPreflightLLMBedrockNoRegion(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Provider = "bedrock"
	r := checkPreflightLLM(cfg)
	if r.status != statusWarn {
		t.Errorf("status = %v, want statusWarn", r.status)
	}
	if !strings.Contains(r.message, "region is empty") {
		t.Errorf("message = %q", r.message)
	}
}

func TestCheckPreflightHooksHandlesMissingDir(t *testing.T) {
	dir := t.TempDir()
	results := checkPreflightHooks(dir)
	// Missing hooks dir is silent (user may not use that client).
	if len(results) != 0 {
		t.Errorf("expected no results for missing hooks, got %d: %+v", len(results), results)
	}
}

func TestCheckPreflightHooksReportsInstalled(t *testing.T) {
	dir := t.TempDir()
	hookDir := filepath.Join(dir, "hooks", "claude-code")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two .sh proxy scripts.
	for _, name := range []string{"session-start.sh", "stop.sh"} {
		if err := os.WriteFile(filepath.Join(hookDir, name), []byte("#!/bin/bash\nexec gramaton hook stop\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	results := checkPreflightHooks(dir)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	if results[0].status != statusOK {
		t.Errorf("status = %v, want statusOK; message=%q", results[0].status, results[0].message)
	}
	if !strings.Contains(results[0].message, "2 script(s)") {
		t.Errorf("message = %q, want '2 script(s)'", results[0].message)
	}
}

func TestCheckPreflightRecentErrorsMissingLog(t *testing.T) {
	dir := t.TempDir()
	r := checkPreflightRecentErrors(dir)
	if r.status != statusOK {
		t.Errorf("status = %v, want statusOK (no log = clean state)", r.status)
	}
}

func TestCheckPreflightRecentErrorsCleanLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gramaton.log")
	if err := os.WriteFile(logPath, []byte(`{"level":"INFO","msg":"started"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := checkPreflightRecentErrors(dir)
	if r.status != statusOK {
		t.Errorf("status = %v, want statusOK", r.status)
	}
}

func TestCheckPreflightRecentErrorsFindsErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gramaton.log")
	body := `{"level":"INFO","msg":"started"}` + "\n" +
		`{"level":"ERROR","msg":"index rebuild failed"}` + "\n" +
		`{"level":"INFO","msg":"recovered"}` + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := checkPreflightRecentErrors(dir)
	if r.status != statusWarn {
		t.Errorf("status = %v, want statusWarn (found ERROR line)", r.status)
	}
	if !strings.Contains(r.message, "1 ERROR") {
		t.Errorf("message = %q, want contains '1 ERROR'", r.message)
	}
}

func TestPrintPreflightResultIncludesIcon(t *testing.T) {
	cases := []struct {
		status preflightStatus
		want   string
	}{
		{statusOK, "✓"},
		{statusWarn, "⚠"},
		{statusError, "✗"},
		{statusSkip, "○"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		printPreflightResult(&buf, preflightResult{
			name:    "Test",
			status:  tc.status,
			message: "msg",
		})
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("status %v: output missing icon %q:\n%s", tc.status, tc.want, buf.String())
		}
	}
}

func TestPrintPreflightResultShowsRemediationOnWarnError(t *testing.T) {
	cases := []struct {
		name        string
		status      preflightStatus
		shouldShow  bool
	}{
		{"warn", statusWarn, true},
		{"error", statusError, true},
		{"ok hides remediation", statusOK, false},
		{"skip hides remediation", statusSkip, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printPreflightResult(&buf, preflightResult{
				name:        "Test",
				status:      tc.status,
				message:     "msg",
				remediation: "do the thing",
			})
			has := strings.Contains(buf.String(), "do the thing")
			if has != tc.shouldShow {
				t.Errorf("output:\n%s\nwant remediation visible=%v", buf.String(), tc.shouldShow)
			}
		})
	}
}
