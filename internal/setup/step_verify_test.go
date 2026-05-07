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

// newVerifyFixture constructs a minimal Wizard targeting a
// configured-as-skip-everything default state, ready for stepVerify
// to be called directly. The data directory is created at 0700 so
// the writability probe succeeds. Tests tweak wiz.cfg before calling
// stepVerify to exercise different branches.
func newVerifyFixture(t *testing.T) (*Wizard, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	// Skip-everything baseline.
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""

	var buf bytes.Buffer
	wiz := New(
		NewScriptedPrompter(), // no prompts in verify
		NewWriter(&buf),
		&cfg,
		filepath.Join(tmpDir, "config.yaml"),
		tmpDir,
	)
	return wiz, &buf, tmpDir
}

// TestStepVerifySkipEverything confirms the baseline verify pass:
// config saves, perms are reported, data dir writable, embedding
// and LLM both surface "disabled"/"not configured" warnings.
func TestStepVerifySkipEverything(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)

	wiz.stepVerify(context.Background())
	out := buf.String()

	mustContain := []string{
		"Verification",
		"Config saved:",
		"Config file permissions: 0600",
		"Data directory writable:",
		"Embedding: disabled",
		"LLM: not configured",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in verify output:\n%s", want, out)
		}
	}
	// Saved config must actually exist on disk.
	if _, err := os.Stat(wiz.cfgPath); err != nil {
		t.Errorf("config.yaml not written: %v", err)
	}
}

// TestStepVerifyConfigPermsAre0600 asserts the saved config.yaml has
// 0600 perms (key file paths live in it; world-readable would leak
// secrets even when keys are in separate files).
func TestStepVerifyConfigPermsAre0600(t *testing.T) {
	wiz, _, _ := newVerifyFixture(t)
	wiz.stepVerify(context.Background())

	testutil.AssertFileMode(t, wiz.cfgPath, 0o600)
}

// TestStepVerifyBERTEmbedding confirms the BERT check renders the
// expected "pure-Go BERT" line.
func TestStepVerifyBERTEmbedding(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)
	wiz.cfg.Embedding.Provider = "bert"

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Embedding: pure-Go BERT") {
		t.Errorf("missing BERT summary:\n%s", out)
	}
}

// TestStepVerifyOpenAIEmbeddingWithKeyFile exercises the api-key-
// file-present branch: the file must exist, and the verify line
// should confirm "OpenAI (cloud)".
func TestStepVerifyOpenAIEmbeddingWithKeyFile(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)

	keyPath := filepath.Join(tmpDir, "openai.key")
	if err := os.WriteFile(keyPath, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.Embedding.Provider = "openai"
	wiz.cfg.Embedding.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Embedding: OpenAI (cloud)") {
		t.Errorf("missing OpenAI summary:\n%s", out)
	}
}

// TestStepVerifyOpenAIEmbeddingWithMissingKeyFile exercises the
// warning path: provider set but api_key_file path doesn't resolve.
func TestStepVerifyOpenAIEmbeddingWithMissingKeyFile(t *testing.T) {
	wiz, buf, _ := newVerifyFixture(t)
	wiz.cfg.Embedding.Provider = "openai"
	wiz.cfg.Embedding.APIKeyFile = "/tmp/definitely-not-a-real-key-file-xyz"

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "OpenAI key file missing") {
		t.Errorf("missing key-file warning:\n%s", out)
	}
}

// TestStepVerifyLLMWithGoodKeyFile exercises the LLM-with-api-key-
// file path: key file exists at 0600, provider=anthropic. Expect the
// happy-path ✓ line.
func TestStepVerifyLLMWithGoodKeyFile(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)
	keyPath := filepath.Join(tmpDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.LLM.Provider = "anthropic"
	wiz.cfg.LLM.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "LLM: anthropic (key file present, 0600 perms)") {
		t.Errorf("missing LLM ok line:\n%s", out)
	}
}

// TestStepVerifyLLMWithKeyFileWrongPerms catches the 0644 case:
// user hand-edited a key file without setting 0600. Wizard must
// fail closed with a visible chmod hint.
func TestStepVerifyLLMWithKeyFileWrongPerms(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)
	keyPath := filepath.Join(tmpDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte("sk-ant-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wiz.cfg.LLM.Provider = "anthropic"
	wiz.cfg.LLM.APIKeyFile = keyPath

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "perms are 644") {
		t.Errorf("missing wrong-perm warn:\n%s", out)
	}
	if !strings.Contains(out, "chmod 600") {
		t.Errorf("missing chmod remediation:\n%s", out)
	}
}

// TestStepVerifyHooksInstalledAndExecutable exercises the hooks
// survey branch: install a fake hook script with 0755 perms,
// verifyHooks must report ✓.
func TestStepVerifyHooksInstalledAndExecutable(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)

	hookDir := filepath.Join(tmpDir, "hooks", "claude-code")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "session-start.sh"),
		[]byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "Hooks (claude-code): 1 script(s) installed and executable") {
		t.Errorf("missing hooks ok line:\n%s", out)
	}
}

// TestStepVerifyHooksNonExecutable catches the chmod-forgot case:
// hook script present but missing +x. Must warn with a fix hint.
func TestStepVerifyHooksNonExecutable(t *testing.T) {
	wiz, buf, tmpDir := newVerifyFixture(t)

	hookDir := filepath.Join(tmpDir, "hooks", "claude-code")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "session-start.sh"),
		[]byte("#!/bin/bash\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wiz.stepVerify(context.Background())
	out := buf.String()

	if !strings.Contains(out, "not executable") {
		t.Errorf("missing non-executable warn:\n%s", out)
	}
	if !strings.Contains(out, "chmod +x") {
		t.Errorf("missing chmod remediation:\n%s", out)
	}
}
