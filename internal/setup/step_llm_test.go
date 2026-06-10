package setup

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// Parser tests — covers input-hardening fixes that stop silent
// acceptance of values that would disable cost protection (Inf, NaN,
// enormous numbers) or leave users thinking a bad value took effect.

func TestParseMoneyUSD(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{"plain number", "5.00", 5.00, false},
		{"dollar prefix", "$5", 5.00, false},
		{"dollar prefix with cents", "$5.50", 5.50, false},
		{"whitespace padded", "  10.00  ", 10.00, false},
		{"zero", "0", 0, false},

		// Invalid inputs: each must error, caller falls back to default.
		{"empty", "", 0, true},
		{"garbage", "abc", 0, true},
		{"negative", "-5", 0, true},
		{"positive infinity", "inf", 0, true},
		{"negative infinity", "-inf", 0, true},
		{"NaN", "nan", 0, true},
		{"1e309 overflows to inf", "1e309", 0, true},
		{"above cap", "10001", 0, true},
		{"far above cap", "1000000", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMoneyUSD(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			// Extra safety: on success, never return Inf/NaN.
			if err == nil && (math.IsInf(got, 0) || math.IsNaN(got)) {
				t.Errorf("got non-finite on success: %v", got)
			}
		})
	}
}

func TestParseIntAtLeast(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		min     int
		want    int
		wantErr bool
	}{
		{"valid", "500", 1, 500, false},
		{"min value", "1", 1, 1, false},

		{"below min", "0", 1, 0, true},
		{"empty", "", 1, 0, true},
		{"garbage", "abc", 1, 0, true},
		{"above cap", "1000001", 1, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIntAtLeast(tc.input, tc.min)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Step 2 menu-branch tests.

// newWizardForLLMTest builds a wizard that reaches Step 2 with "1"
// (fresh) and "5" (skip embedding) pre-scripted, then feeds the
// caller's Step 2 answers. Steps 3/4 are short-circuited via a
// fakeMCPBackend with no detected clients. Keeps tests fast and
// deterministic — no network, no real filesystem mutation beyond
// the tmpdir.
func newWizardForLLMTest(t *testing.T, llmAnswers ...string) (*Wizard, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	answers := append([]string{"1", "5"}, llmAnswers...)

	var buf bytes.Buffer
	prompter := NewScriptedPrompter(answers...)
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(tmpDir, "data")

	wiz := New(prompter, NewWriter(&buf), &cfg, filepath.Join(tmpDir, "config.yaml"), tmpDir)
	wiz.mcpBackend = &fakeMCPBackend{}
	// Stub the AWS verifier so the Bedrock branch doesn't dial AWS
	// during scripted runs. Real verification is exercised by the
	// dedicated test for verifyAWSProfile.
	wiz.awsVerifier = func(ctx context.Context, region, profile string) (callerIdentity, error) {
		return callerIdentity{
			account: "111122223333",
			arn:     "arn:aws:sts::111122223333:assumed-role/test-role/session",
		}, nil
	}
	return wiz, &buf, tmpDir
}

// TestStepLLMSkipBranch covers [5] Skip: provider stays empty,
// rerank stays default-off, warning fires about deterministic-only
// curation mode.
func TestStepLLMSkipBranch(t *testing.T) {
	wiz, buf, _ := newWizardForLLMTest(t, "5")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.LLM.Provider != "" {
		t.Errorf("LLM.Provider: got %q, want empty", wiz.cfg.LLM.Provider)
	}
	if wiz.cfg.LLM.Rerank.Enabled {
		t.Errorf("Search.RerankEnabled: should stay false when LLM is skipped")
	}
	if !strings.Contains(out, "deterministic-only mode") {
		t.Errorf("skip warning missing:\n%s", out)
	}
}

// TestStepLLMHelpThenSkip covers [4] (help) → re-enter menu → [5]
// skip. Verifies the help guidance prints AND the menu loops back
// to the provider choice rather than aborting after help.
func TestStepLLMHelpThenSkip(t *testing.T) {
	wiz, buf, _ := newWizardForLLMTest(t, "4", "5")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "console.anthropic.com") {
		t.Errorf("help guidance missing signup URL:\n%s", out)
	}
	if wiz.cfg.LLM.Provider != "" {
		t.Errorf("LLM.Provider: got %q, want empty after help→skip", wiz.cfg.LLM.Provider)
	}
}

// TestStepLLMAnthropicDetectsExistingKeyAndKeeps covers the re-run
// path: when ~/.gramaton/anthropic.key already exists, the wizard
// offers to keep it instead of forcing the user to re-paste. Answers:
// "1" picks Anthropic, "y" keeps the existing key. No Secret() call
// consumed — the test would hang or fail if the keep-branch were
// skipped (ScriptedPrompter has no more answers to give).
func TestStepLLMAnthropicDetectsExistingKeyAndKeeps(t *testing.T) {
	wiz, buf, tmpDir := newWizardForLLMTest(t, "1", "y")

	// Pre-seed an existing Anthropic key file. Validation will
	// fail (fake key, no network), but that's non-fatal — the
	// wizard warns and continues. What we assert is:
	//   - provider got set to "anthropic"
	//   - APIKeyFile points at the existing file
	//   - the wizard printed the "key detected" notice
	keyPath := filepath.Join(tmpDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-fake-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.LLM.Provider != "anthropic" {
		t.Errorf("LLM.Provider = %q, want anthropic", wiz.cfg.LLM.Provider)
	}
	if wiz.cfg.LLM.APIKeyFile != keyPath {
		t.Errorf("APIKeyFile = %q, want %q", wiz.cfg.LLM.APIKeyFile, keyPath)
	}
	if !strings.Contains(out, "Anthropic key detected") {
		t.Errorf("output missing key-detected notice:\n%s", out)
	}
	if !strings.Contains(out, "Using existing key") {
		t.Errorf("output missing keep-existing confirmation:\n%s", out)
	}
}

// TestStepLLMAnthropicEmptyKeyFallsBackToSkip covers [1] Anthropic
// with an empty Secret — the wizard must warn, call llmSkip, and
// leave provider empty. No network call because Secret is empty.
func TestStepLLMAnthropicEmptyKeyFallsBackToSkip(t *testing.T) {
	wiz, buf, _ := newWizardForLLMTest(t, "1", "") // pick Anthropic, press Enter at secret
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.LLM.Provider != "" {
		t.Errorf("LLM.Provider: got %q, want empty after empty key", wiz.cfg.LLM.Provider)
	}
	if !strings.Contains(out, "No key entered") {
		t.Errorf("empty-key warning missing:\n%s", out)
	}
	if !strings.Contains(out, "deterministic-only mode") {
		t.Errorf("fall-through to skip missing its warning:\n%s", out)
	}
}

// TestStepLLMBedrockBranch covers [3] Bedrock with profile + region.
// Sets Models tier map to Bedrock ARNs; no AWS calls.
func TestStepLLMBedrockBranch(t *testing.T) {
	// [3] = Bedrock, profile = "test-profile", region = "", use default caps "y".
	wiz, buf, _ := newWizardForLLMTest(t, "3", "test-profile", "", "y")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.LLM.Provider != "bedrock" {
		t.Errorf("LLM.Provider: got %q, want bedrock", wiz.cfg.LLM.Provider)
	}
	if wiz.cfg.LLM.AWSProfile != "test-profile" {
		t.Errorf("AWSProfile: got %q", wiz.cfg.LLM.AWSProfile)
	}
	if wiz.cfg.LLM.Region != "us-west-2" {
		t.Errorf("Region: got %q, want us-west-2 (default)", wiz.cfg.LLM.Region)
	}
	if !strings.HasPrefix(wiz.cfg.LLM.Models.Medium, "anthropic.claude-") {
		t.Errorf("LLM.Models.Medium: got %q, want anthropic.claude-* Bedrock ID", wiz.cfg.LLM.Models.Medium)
	}
	if wiz.cfg.LLM.Models.Low == "" || wiz.cfg.LLM.Models.Medium == "" || wiz.cfg.LLM.Models.High == "" {
		t.Errorf("Models tier map should be populated with Bedrock IDs, got: %+v", wiz.cfg.LLM.Models)
	}
	if !wiz.cfg.LLM.Rerank.Enabled {
		t.Errorf("Search.RerankEnabled: should flip to true when LLM is configured")
	}
	if !strings.Contains(out, "Bedrock with Anthropic models configured") {
		t.Errorf("success line missing:\n%s", out)
	}
	if !strings.Contains(out, "Spending caps set") {
		t.Errorf("caps confirmation missing:\n%s", out)
	}
}

// TestStepLLMBedrockCustomCapsWithBadInputs covers the customize-caps
// path: if the user enters an invalid number, the wizard must keep
// the default AND emit a visible warn naming the bad value.
func TestStepLLMBedrockCustomCapsWithBadInputs(t *testing.T) {
	// [3] Bedrock, profile "", region "", [n] customize caps, then
	// bad USD/day, bad calls/day, bad USD/cycle.
	wiz, buf, _ := newWizardForLLMTest(t,
		"3", "", "", "n",
		"abc", // invalid USD/day
		"abc", // invalid calls/day
		"abc", // invalid USD/cycle
	)
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	// Defaults must survive.
	if wiz.cfg.LLM.CostLimits.MaxCostUSDPerDay != 5.00 {
		t.Errorf("MaxCostUSDPerDay: got %v, want 5.00 (default preserved)", wiz.cfg.LLM.CostLimits.MaxCostUSDPerDay)
	}
	if wiz.cfg.LLM.CostLimits.MaxCallsPerDay != 500 {
		t.Errorf("MaxCallsPerDay: got %d, want 500 (default preserved)", wiz.cfg.LLM.CostLimits.MaxCallsPerDay)
	}
	if wiz.cfg.LLM.CostLimits.MaxCostUSDPerRun != 1.00 {
		t.Errorf("MaxCostUSDPerRun: got %v, want 1.00 (default preserved)", wiz.cfg.LLM.CostLimits.MaxCostUSDPerRun)
	}
	// User-visible warns should name the invalid inputs (one per bad
	// field). Anything silently ignored would leave the user thinking
	// their value took effect.
	if !strings.Contains(out, "Invalid USD/day") {
		t.Errorf("missing USD/day warn:\n%s", out)
	}
	if !strings.Contains(out, "Invalid calls/day") {
		t.Errorf("missing calls/day warn:\n%s", out)
	}
	if !strings.Contains(out, "Invalid USD/cycle") {
		t.Errorf("missing USD/cycle warn:\n%s", out)
	}
}
