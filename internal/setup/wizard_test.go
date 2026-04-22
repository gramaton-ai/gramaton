package setup

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// TestWizardImportBranch is a smoke test for the orchestration: drive
// the wizard through the import branch (Step 0 -> [2]) which currently
// prints a placeholder and returns. This validates that:
//   - The welcome banner prints.
//   - The fresh-vs-import choice is parsed correctly.
//   - The import-path placeholder fires.
//   - No nil-pointer panics crash the Run path.
//
// Doesn't cover Step 1+; those are covered by focused per-step tests
// as they come online (Step 1 needs the embed package, which needs
// either network or mocking; Step 2 needs a mocked Anthropic client;
// both are follow-up work).
func TestWizardImportBranchPrintsPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	writer := NewWriter(&buf)
	prompter := NewScriptedPrompter("2") // pick [2] import

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()

	wiz := New(prompter, writer, &cfg, t.TempDir()+"/config.yaml", t.TempDir())
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	out := buf.String()
	// The welcome block should have fired.
	if !strings.Contains(out, "Welcome to Gramaton") {
		t.Errorf("missing welcome banner in output:\n%s", out)
	}
	// Step 0 choice should have printed.
	if !strings.Contains(out, "First time") || !strings.Contains(out, "Import a backup") {
		t.Errorf("missing Step 0 prompt in output:\n%s", out)
	}
	// The import placeholder should have fired -- this is the
	// confirmation that the user's [2] answer routed correctly.
	if !strings.Contains(out, "Import flow is not yet implemented") {
		t.Errorf("import placeholder missing in output:\n%s", out)
	}
	// Next-steps block should NOT have printed on the import path
	// (the placeholder returns early). If it did, orchestration is
	// wrong.
	if strings.Contains(out, "Gramaton is ready") {
		t.Errorf("next-steps block fired on import path, which returns early:\n%s", out)
	}
}

// TestWizardFreshPathShowsBootstrapStep validates the fresh-install
// branch orchestrates through Step 1's embedding menu. The Skip
// option (idx 4) lets us exit Step 1 without actually downloading a
// model or hitting the network. We continue through LLM (skip) and
// the stubbed steps to confirm the full Run path completes.
func TestWizardFreshPathSkipEverything(t *testing.T) {
	var buf bytes.Buffer
	writer := NewWriter(&buf)
	// Answers, in order:
	//   Step 0: [1] fresh
	//   Step 1: [5] skip embedding
	//   Step 2: [5] skip LLM
	// Steps 3-5 are non-prompting stubs.
	prompter := NewScriptedPrompter("1", "5", "5")

	tmpDir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = tmpDir + "/data"

	wiz := New(prompter, writer, &cfg, tmpDir+"/config.yaml", tmpDir)
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	out := buf.String()
	// Every step header should have printed.
	for _, want := range []string{
		"Welcome to Gramaton",
		"Step 1 of 4: Knowledge store",
		"Step 2 of 4: Autonomous curation",
		"Step 3 of 4: Connecting to your AI tools",
		"Step 4 of 4: Automatic knowledge capture",
		"Verification",
		"Gramaton is ready.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output", want)
		}
	}
	// Skip warnings should have fired for embedding and LLM.
	if !strings.Contains(out, "Semantic search disabled") {
		t.Errorf("embedding skip warning missing")
	}
	if !strings.Contains(out, "deterministic-only") {
		t.Errorf("LLM skip warning missing")
	}
	// Config should have been saved with empty providers.
	if cfg.Embedding.Provider != "" {
		t.Errorf("expected empty Embedding.Provider, got %q", cfg.Embedding.Provider)
	}
	if cfg.LLM.Provider != "" {
		t.Errorf("expected empty LLM.Provider, got %q", cfg.LLM.Provider)
	}
}
