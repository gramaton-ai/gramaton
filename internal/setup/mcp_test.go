package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// fakeMCPBackend is a test double for Step 3. It lets tests pre-
// seed the detected client list and control what each Register call
// returns, without shelling out to real MCP clients.
type fakeMCPBackend struct {
	clients   []DetectedClient
	registers []fakeRegisterResult
	// calls captures the order and target of each Register call for
	// assertions (did the wizard call us for every client? in the
	// expected order?).
	calls []string
	// storeCalls captures RegisterStore invocations from the
	// read-only attach route, as "<client>:<store>".
	storeCalls []string
}

type fakeRegisterResult struct {
	already bool
	err     error
}

func (f *fakeMCPBackend) Detect() []DetectedClient { return f.clients }

func (f *fakeMCPBackend) Register(_ context.Context, c DetectedClient) (bool, error) {
	f.calls = append(f.calls, c.Name)
	if len(f.registers) == 0 {
		return false, nil // default: silent success
	}
	result := f.registers[0]
	f.registers = f.registers[1:]
	return result.already, result.err
}

func (f *fakeMCPBackend) RegisterStore(_ context.Context, c DetectedClient, storeName string) (bool, error) {
	f.storeCalls = append(f.storeCalls, c.Name+":"+storeName)
	return false, nil
}

// Helper: build a wizard that will run Steps 1 and 2 with skip, then
// reach Step 3. Avoids repeating the boilerplate script in every
// Step-3 test.
//
// Answers layout (in order the scripted prompter will consume):
//
//	[0]: Step 0 route choice            (pass "1" for fresh)
//	[1]: identity name   ("" = accept the OS-account default)
//	[2]: identity email  ("" = skip)
//	[3]: Step 1 embedding menu choice   (pass "5" for skip)
//	[4]: Step 2 LLM menu choice         (pass "5" for skip)
//	[5]: Step 3 YesNo confirm           (caller controls)
func newWizardForMCPTest(t *testing.T, backend MCPBackend, mcpConfirm string) (*Wizard, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriter(&buf)
	prompter := NewScriptedPrompter("1", "", "", "5", "5", mcpConfirm)

	tmpDir := t.TempDir()
	// Hermeticity: wiz.Run reaches stepVerify, whose MCP survey
	// bypasses the injected backend and probes the real PATH + home.
	// Sandbox both so dev machines with claude/codex installed don't
	// shell out (slow) or read real user config (nondeterministic).
	t.Setenv("PATH", "")
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	cfg := config.Defaults()
	cfg.DataDir = tmpDir + "/data"

	wiz := New(prompter, writer, &cfg, tmpDir+"/config.yaml", tmpDir)
	wiz.mcpBackend = backend
	return wiz, &buf
}

func TestStepMCPNoClientsDetected(t *testing.T) {
	backend := &fakeMCPBackend{}                    // empty client list
	wiz, buf := newWizardForMCPTest(t, backend, "") // no confirm needed

	// The scripted prompter will have extra unused answers; that's
	// fine -- the empty detected path short-circuits before the
	// confirm prompt.
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "No supported MCP clients were found") {
		t.Errorf("missing empty-detection message:\n%s", out)
	}
	// We should NOT have attempted any Register calls.
	if len(backend.calls) != 0 {
		t.Errorf("unexpected Register calls with no clients: %v", backend.calls)
	}
}

func TestStepMCPBothClientsDetectedUserConfirms(t *testing.T) {
	backend := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Claude Code", Binary: "/fake/bin/claude"},
			{Name: "kiro-cli", Binary: "/fake/bin/kiro"},
		},
		registers: []fakeRegisterResult{
			{already: false, err: nil}, // Claude Code: fresh registration
			{already: true, err: nil},  // kiro-cli: already registered
		},
	}
	wiz, buf := newWizardForMCPTest(t, backend, "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	// Detection report should list both clients.
	if !strings.Contains(out, "Claude Code") || !strings.Contains(out, "kiro-cli") {
		t.Errorf("detected-list missing clients in output:\n%s", out)
	}
	// Both Register calls should have fired.
	if len(backend.calls) != 2 {
		t.Errorf("want 2 Register calls, got %d: %v", len(backend.calls), backend.calls)
	}
	// Fresh-registered client should see "Added"; already-registered
	// client should see "already registered".
	if !strings.Contains(out, "Added Gramaton to Claude Code") {
		t.Errorf("missing fresh-registration confirmation:\n%s", out)
	}
	if !strings.Contains(out, "already registered with kiro-cli") {
		t.Errorf("missing already-registered confirmation:\n%s", out)
	}
	// Restart warning should fire since at least one client was
	// successfully configured.
	if !strings.Contains(out, "Restart your AI client") {
		t.Errorf("missing restart warning:\n%s", out)
	}
}

func TestStepMCPUserDeclines(t *testing.T) {
	backend := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Claude Code", Binary: "/fake/bin/claude"},
		},
	}
	wiz, buf := newWizardForMCPTest(t, backend, "n")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if len(backend.calls) != 0 {
		t.Errorf("Register should not have been called after user declined: %v", backend.calls)
	}
	if !strings.Contains(out, "Skipping MCP client registration") {
		t.Errorf("missing skip confirmation in output:\n%s", out)
	}
	// Manual-registration hint should fire so users have a way
	// forward.
	if !strings.Contains(out, "claude mcp add") {
		t.Errorf("missing manual-registration hint:\n%s", out)
	}
}

func TestStepMCPRegistrationFailsForOneClient(t *testing.T) {
	backend := &fakeMCPBackend{
		clients: []DetectedClient{
			{Name: "Claude Code", Binary: "/fake/bin/claude"},
			{Name: "kiro-cli", Binary: "/fake/bin/kiro"},
		},
		registers: []fakeRegisterResult{
			{already: false, err: nil},
			{already: false, err: errors.New("kiro mcp add not supported")},
		},
	}
	wiz, buf := newWizardForMCPTest(t, backend, "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Added Gramaton to Claude Code") {
		t.Errorf("Claude Code success line missing:\n%s", out)
	}
	// kiro-cli failure should surface as a warn line carrying the
	// backend's error string.
	if !strings.Contains(out, "kiro-cli: kiro mcp add not supported") {
		t.Errorf("kiro-cli warn line missing expected error text:\n%s", out)
	}
	// Restart warning should still fire since at least one client
	// (Claude Code) was registered successfully.
	if !strings.Contains(out, "Restart your AI client") {
		t.Errorf("missing restart warning (partial success case):\n%s", out)
	}
}

// TestRegisterWithCursorFreshCreate covers the fresh-install path:
// no ~/.cursor/mcp.json exists. The file is created with the
// gramaton stdio entry under mcpServers.
func TestRegisterWithCursorFreshCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	already, err := registerWithCursor(context.Background(), "")
	if err != nil {
		t.Fatalf("registerWithCursor: %v", err)
	}
	if already {
		t.Error("fresh create should report newly registered")
	}

	raw, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatalf("mcp.json not created: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	entry, ok := parsed.MCPServers["gramaton"]
	if !ok {
		t.Fatalf("gramaton entry missing:\n%s", raw)
	}
	if entry.Type != "stdio" || entry.Command != "gramaton" || len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf("entry = %+v, want stdio/gramaton/[mcp]", entry)
	}
}

// TestRegisterWithCursorPreservesAndIdempotent seeds an mcp.json
// with another server, a top-level unrelated key, a stale gramaton
// entry, and a UTF-8 BOM. The stale entry is replaced, everything
// else survives, and a second run reports alreadyRegistered without
// rewriting the file.
func TestRegisterWithCursorPreservesAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(cursorDir, "mcp.json")

	initial := "\xEF\xBB\xBF" + `{
  "unrelated": true,
  "mcpServers": {
    "other-server": {"type": "stdio", "command": "other", "args": []},
    "gramaton": {"type": "stdio", "command": "/old/path/gramaton", "args": ["mcp", "--stale-flag"]}
  }
}`
	if err := os.WriteFile(mcpPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	already, err := registerWithCursor(context.Background(), "")
	if err != nil {
		t.Fatalf("first registerWithCursor: %v", err)
	}
	if already {
		t.Error("stale entry should be replaced, reported as a new registration")
	}

	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, `"other-server"`) {
		t.Error("other server entry was lost")
	}
	if !strings.Contains(content, `"unrelated"`) {
		t.Error("unrelated top-level key was lost")
	}
	if strings.Contains(content, "--stale-flag") {
		t.Error("stale gramaton entry was not replaced")
	}

	already, err = registerWithCursor(context.Background(), "")
	if err != nil {
		t.Fatalf("second registerWithCursor: %v", err)
	}
	if !already {
		t.Error("second run should report alreadyRegistered")
	}
	raw2, _ := os.ReadFile(mcpPath)
	if string(raw) != string(raw2) {
		t.Error("mcp.json changed on idempotent second run")
	}
}

// TestRegisterWithCursorMalformedJSON pins the won't-touch behavior.
func TestRegisterWithCursorMalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(cursorDir, "mcp.json")
	garbage := "{not json"
	if err := os.WriteFile(mcpPath, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := registerWithCursor(context.Background(), ""); err == nil {
		t.Fatal("expected parse error on malformed mcp.json")
	}
	raw, _ := os.ReadFile(mcpPath)
	if string(raw) != garbage {
		t.Error("malformed mcp.json was modified; must be left untouched")
	}
}
