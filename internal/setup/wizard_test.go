package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// TestWizardImportBranchEmptyPath exercises graceful abort when the
// user picks [2] import then presses Enter at the archive-path
// prompt. The wizard warns and continues to Steps 2-4 (user may
// still want to configure LLM/MCP/hooks even without imported data).
// The Step 3 + Step 4 answers here drive the rest of the wizard
// through the rejection-handling path; hooks+mcp use the full-flow
// prompter count (Step 0, import path, LLM menu, MCP confirm, hook
// confirm).
func TestWizardImportBranchEmptyPath(t *testing.T) {
	var buf bytes.Buffer
	writer := NewWriter(&buf)
	// Answers: Step 0 "2" (import), path "" (abort import cleanly),
	// Step 2 LLM "5" (skip), Step 3 MCP "n", Step 4 hooks "n".
	prompter := NewScriptedPrompter("2", "", "5", "n", "n")

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir() + "/data"

	wiz := New(prompter, writer, &cfg, t.TempDir()+"/config.yaml", t.TempDir())
	// Step 3 MCP detect will run against the real exec.LookPath
	// (no MCP backend injection here). That's fine: the prompter
	// sees "n" at the confirm and no registration happens. To keep
	// the test deterministic across machines (some have claude, some
	// don't), we inject a no-client backend.
	wiz.mcpBackend = &fakeMCPBackend{}

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "No path entered; aborting import") {
		t.Errorf("empty-path warn missing:\n%s", out)
	}
	// Wizard should still complete the remaining steps: even though
	// the user aborted import, subsequent per-machine setup is
	// independent. Next-steps block must fire.
	if !strings.Contains(out, "Gramaton is ready") {
		t.Errorf("wizard should complete after empty import path; next-steps missing:\n%s", out)
	}
}

// TestWizardImportBranchMissingFile confirms the import path rejects
// a nonexistent archive path with a clear error (not a panic, not a
// silent abort). We script enough answers for the rest of the flow
// to complete since runImport is a soft-abort on bad path.
func TestWizardImportBranchMissingFile(t *testing.T) {
	var buf bytes.Buffer
	writer := NewWriter(&buf)
	// Construct the path via filepath.Join so the OS-native separator
	// is used. The wizard echoes the absolute path back in its error
	// message; a hardcoded "/tmp/..." substring assertion would miss
	// on Windows where the rendered path is "D:\path\..." with
	// backslashes. Production correctness here is unchanged: it uses
	// os.IsNotExist for the missing-file check, which is portable.
	missing := filepath.Join(t.TempDir(), "definitely-not-a-real-archive.tar.gz")
	prompter := NewScriptedPrompter(
		"2",
		missing,
		"5", "n", "n",
	)

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir() + "/data"

	wiz := New(prompter, writer, &cfg, t.TempDir()+"/config.yaml", t.TempDir())
	wiz.mcpBackend = &fakeMCPBackend{}

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "No file at "+missing) {
		t.Errorf("missing-file error not reported:\n%s", out)
	}
}

// TestWizardImportBranchDirectoryPath catches the "user dragged a
// folder instead of a file" mistake.
func TestWizardImportBranchDirectoryPath(t *testing.T) {
	var buf bytes.Buffer
	writer := NewWriter(&buf)

	dir := t.TempDir()
	prompter := NewScriptedPrompter("2", dir, "5", "n", "n")

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir() + "/data"

	wiz := New(prompter, writer, &cfg, t.TempDir()+"/config.yaml", t.TempDir())
	wiz.mcpBackend = &fakeMCPBackend{}

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "is a directory, not a backup archive") {
		t.Errorf("directory-path error not reported:\n%s", out)
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
		"Step 1 of 5: Knowledge store",
		"Step 2 of 5: Autonomous curation",
		"Step 3 of 5: Connecting to your AI tools",
		"Step 4 of 5: Agent usage instructions",
		"Step 5 of 5: Automatic knowledge capture",
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

// TestWriteWithRollbackRestoresExisting pins the --force-re-run
// safety guarantee: if a key file exists before the wizard overwrites
// it, a cleanup must restore the ORIGINAL content rather than delete
// the file. The naive `os.Remove`-on-rollback pattern (pre-fix)
// destroyed both the old and new keys on Ctrl+C.
func TestWriteWithRollbackRestoresExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/anthropic.key"
	original := []byte("sk-ant-original-key\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// Minimal wizard scaffold — we're only exercising the cleanup
	// registration, not the prompt flow.
	wiz := New(NewScriptedPrompter(), NewWriter(&bytes.Buffer{}),
		&config.Config{}, tmpDir+"/config.yaml", tmpDir)

	// Simulate the replace-with-new-key path: overwrite with new.
	newKey := []byte("sk-ant-new-key-from-forced-init\n")
	if err := wiz.writeWithRollback(path, newKey, 0o600); err != nil {
		t.Fatalf("writeWithRollback: %v", err)
	}
	gotNew, _ := os.ReadFile(path)
	if string(gotNew) != string(newKey) {
		t.Errorf("after write: file = %q, want %q", gotNew, newKey)
	}

	// Simulate Ctrl+C firing cleanups.
	wiz.runCleanups()

	// Original key must be restored (not deleted).
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file missing after rollback — existing key destroyed: %v", err)
	}
	if string(restored) != string(original) {
		t.Errorf("rollback didn't restore original:\n  got:  %q\n  want: %q", restored, original)
	}
}

// TestWriteWithRollbackRemovesWhenFreshWrite verifies the opposite
// case: if the file did NOT exist before, rollback deletes the new
// file (preserves the clean-slate state).
func TestWriteWithRollbackRemovesWhenFreshWrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/anthropic.key"

	wiz := New(NewScriptedPrompter(), NewWriter(&bytes.Buffer{}),
		&config.Config{}, tmpDir+"/config.yaml", tmpDir)

	// Fresh install: no pre-existing file.
	if err := wiz.writeWithRollback(path, []byte("sk-ant-new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	wiz.runCleanups()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("fresh-install rollback should remove the file, got err = %v", err)
	}
}
