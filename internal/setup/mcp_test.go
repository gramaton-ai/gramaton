package setup

import (
	"bytes"
	"context"
	"errors"
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

// Helper: build a wizard that will run Steps 1 and 2 with skip, then
// reach Step 3. Avoids repeating the boilerplate script in every
// Step-3 test.
//
// Answers layout (in order the scripted prompter will consume):
//
//	[0]: Step 0 fresh-vs-import choice  (pass "1" for fresh)
//	[1]: Step 1 embedding menu choice   (pass "5" for skip)
//	[2]: Step 2 LLM menu choice         (pass "5" for skip)
//	[3]: Step 3 YesNo confirm           (caller controls)
func newWizardForMCPTest(t *testing.T, backend MCPBackend, mcpConfirm string) (*Wizard, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriter(&buf)
	prompter := NewScriptedPrompter("1", "5", "5", mcpConfirm)

	tmpDir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = tmpDir + "/data"

	wiz := New(prompter, writer, &cfg, tmpDir+"/config.yaml", tmpDir)
	wiz.mcpBackend = backend
	return wiz, &buf
}

func TestStepMCPNoClientsDetected(t *testing.T) {
	backend := &fakeMCPBackend{} // empty client list
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
