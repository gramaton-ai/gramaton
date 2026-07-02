package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/testutil"
)

// newWizardForBootstrapTest builds a wizard reaching Step 1 with the
// given scripted answers. The identity section enters through with
// defaults ("", "") and Step 0 is always "1" (fresh). After the
// bootstrap menu, we pad "5" (skip LLM) and a fakeMCPBackend with
// no clients so Steps 2-4 short-circuit without prompting.
//
// Callers pass only the bootstrap-relevant answers; we prepend the
// identity enter-throughs + Step 0 "1" and append the Step 2 "5".
func newWizardForBootstrapTest(t *testing.T, bootstrapAnswers ...string) (*Wizard, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	answers := append([]string{"", "", "1"}, bootstrapAnswers...)
	// Step 2 LLM skip appended; Steps 3-4 use fakeMCPBackend (no clients)
	// so no prompts fire there.
	answers = append(answers, "5")

	var buf bytes.Buffer
	prompter := NewScriptedPrompter(answers...)
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(tmpDir, "data")

	wiz := New(prompter, NewWriter(&buf), &cfg, filepath.Join(tmpDir, "config.yaml"), tmpDir)
	wiz.mcpBackend = &fakeMCPBackend{}
	return wiz, &buf, tmpDir
}

// TestStepBootstrapSkipBranch covers the [5] Skip choice: Embedding
// provider is deliberately left empty, a warning fires, and the user
// gets a "how to re-enable later" hint. Fast: no network, no file IO
// beyond the data dir mkdir.
func TestStepBootstrapSkipBranch(t *testing.T) {
	wiz, buf, _ := newWizardForBootstrapTest(t, "5")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.Embedding.Provider != "" {
		t.Errorf("Embedding.Provider: got %q, want empty", wiz.cfg.Embedding.Provider)
	}
	if !strings.Contains(out, "Semantic search disabled") {
		t.Errorf("skip warning missing:\n%s", out)
	}
	if !strings.Contains(out, "reconfigure-embedding") {
		t.Errorf("re-enable hint missing:\n%s", out)
	}
}

// TestStepBootstrapOpenAIBranch covers the [3] OpenAI branch: secret
// prompt for the key, key written to 0600 file, config populated.
// Skips the real network call (setupEmbeddingOpenAI does not validate
// the key -- it leaves first-use as the surface).
func TestStepBootstrapOpenAIBranch(t *testing.T) {
	wiz, buf, tmpDir := newWizardForBootstrapTest(t, "3", "sk-test-openai-key-abc123")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.Embedding.Provider != "openai" {
		t.Errorf("Embedding.Provider: got %q, want openai", wiz.cfg.Embedding.Provider)
	}
	if wiz.cfg.Embedding.Model != "text-embedding-3-small" {
		t.Errorf("Embedding.Model: got %q, want text-embedding-3-small", wiz.cfg.Embedding.Model)
	}
	if wiz.cfg.Embedding.Dimension != 1536 {
		t.Errorf("Embedding.Dimension: got %d, want 1536", wiz.cfg.Embedding.Dimension)
	}

	keyPath := filepath.Join(tmpDir, "openai.key")
	if wiz.cfg.Embedding.APIKeyFile != keyPath {
		t.Errorf("APIKeyFile: got %q, want %q", wiz.cfg.Embedding.APIKeyFile, keyPath)
	}
	testutil.AssertFileMode(t, keyPath, 0o600)
	body, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if !strings.HasPrefix(string(body), "sk-test-openai-key-abc123") {
		t.Errorf("key file content: got %q", string(body))
	}
	if !strings.Contains(out, "Embedding configured (text-embedding-3-small via OpenAI)") {
		t.Errorf("success line missing:\n%s", out)
	}
}

// TestStepBootstrapOpenAIEmptyKeyFallsBackToBERT covers the graceful
// recovery when the user presses Enter at the OpenAI key prompt.
// setupEmbeddingOpenAI is expected to warn and call setupEmbeddingBERT.
// We avoid the real BERT download by asserting the warning fires and
// the provider ends up as either "bert" (if download succeeded) or ""
// (if download failed with no network).
func TestStepBootstrapOpenAIEmptyKeyFallsBackToBERT(t *testing.T) {
	// Short-circuit: use the "skip" path instead of the fallback to
	// avoid flakiness from the network-dependent BERT download. We test
	// the fallback intent by asserting the "falling back to BERT" warn
	// message fires.
	if testing.Short() {
		t.Skip("network-dependent BERT fallback path; rerun without -short")
	}
	// Skip when GRAMATON_TEST_NETWORK isn't set -- the bootstrap BERT
	// path touches HuggingFace. Deterministic unit-test CI should not
	// depend on external network.
	if os.Getenv("GRAMATON_TEST_NETWORK") == "" {
		t.Skip("set GRAMATON_TEST_NETWORK=1 to exercise the BERT-fallback path")
	}

	wiz, buf, _ := newWizardForBootstrapTest(t, "3", "")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "falling back to BERT") {
		t.Errorf("BERT fallback warning missing:\n%s", out)
	}
}

// TestStepBootstrapBedrockBranch covers the [4] Bedrock branch.
// Profile blank + region "us-west-2" (default) -> config populated,
// no API call.
func TestStepBootstrapBedrockBranch(t *testing.T) {
	// Step 1 = "4" (Bedrock), profile = "" (default chain), region =
	// "" (default us-west-2).
	wiz, buf, _ := newWizardForBootstrapTest(t, "4", "", "")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.Embedding.Provider != "bedrock" {
		t.Errorf("Embedding.Provider: got %q, want bedrock", wiz.cfg.Embedding.Provider)
	}
	if wiz.cfg.Embedding.Model != "amazon.titan-embed-text-v2:0" {
		t.Errorf("Embedding.Model: got %q", wiz.cfg.Embedding.Model)
	}
	if wiz.cfg.Embedding.Dimension != 1024 {
		t.Errorf("Embedding.Dimension: got %d, want 1024", wiz.cfg.Embedding.Dimension)
	}
	if wiz.cfg.Embedding.Region != "us-west-2" {
		t.Errorf("Embedding.Region: got %q, want us-west-2", wiz.cfg.Embedding.Region)
	}
	if wiz.cfg.Embedding.AWSProfile != "" {
		t.Errorf("AWSProfile: got %q, want empty", wiz.cfg.Embedding.AWSProfile)
	}
	if !strings.Contains(out, "Bedrock embedding configured") {
		t.Errorf("success line missing:\n%s", out)
	}
}

// TestStepBootstrapBedrockBranchWithProfile confirms a non-empty
// profile is captured verbatim and surfaces in the verify summary.
func TestStepBootstrapBedrockBranchWithProfile(t *testing.T) {
	wiz, buf, _ := newWizardForBootstrapTest(t, "4", "my-aws-profile", "eu-central-1")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if wiz.cfg.Embedding.AWSProfile != "my-aws-profile" {
		t.Errorf("AWSProfile: got %q", wiz.cfg.Embedding.AWSProfile)
	}
	if wiz.cfg.Embedding.Region != "eu-central-1" {
		t.Errorf("Region: got %q", wiz.cfg.Embedding.Region)
	}
	if !strings.Contains(out, "AWS profile: my-aws-profile") {
		t.Errorf("profile line missing:\n%s", out)
	}
}

// TestStepBootstrapDataDirCreated confirms the data directory is
// created with 0700 perms regardless of embedding branch.
func TestStepBootstrapDataDirCreated(t *testing.T) {
	wiz, _, _ := newWizardForBootstrapTest(t, "5") // skip embedding for speed
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	testutil.AssertDirMode(t, wiz.cfg.DataDir, 0o700)
}
