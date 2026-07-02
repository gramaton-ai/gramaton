package setup

import (
	"bytes"
	"context"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// TestOSAccountNameFrom pins the GECOS-truncation and username-
// fallback rules on controlled *user.User values, independent of the
// account the tests run under (OSAccountName itself is untestable
// beyond wiring -- user.Current answers differently per machine).
func TestOSAccountNameFrom(t *testing.T) {
	tests := []struct {
		name string
		u    *user.User
		want string
	}{
		{
			name: "GECOS subfields truncated at first comma",
			u:    &user.User{Name: "Ada Lovelace,Room 3,555", Username: "ada"},
			want: "Ada Lovelace",
		},
		{
			name: "plain full name passes through",
			u:    &user.User{Name: "Ada Lovelace", Username: "ada"},
			want: "Ada Lovelace",
		},
		{
			name: "empty full name falls back to username",
			u:    &user.User{Name: "", Username: "ada"},
			want: "ada",
		},
		{
			name: "whitespace-only full name falls back to username",
			u:    &user.User{Name: "   ", Username: "ada"},
			want: "ada",
		},
		{
			name: "both empty yields empty",
			u:    &user.User{},
			want: "",
		},
		{
			name: "nil user yields empty",
			u:    nil,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := osAccountNameFrom(tt.u); got != tt.want {
				t.Errorf("osAccountNameFrom(%+v) = %q, want %q", tt.u, got, tt.want)
			}
		})
	}
}

// newWizardForIdentityTest builds a wizard whose identity answers the
// caller controls, with the rest of the flow scripted to skip ("1"
// fresh, "5" skip embedding, "5" skip LLM; Steps 3-5 short-circuit
// via a fakeMCPBackend with no clients).
func newWizardForIdentityTest(t *testing.T, name, email string) (*Wizard, *bytes.Buffer) {
	t.Helper()
	tmpDir := t.TempDir()

	var buf bytes.Buffer
	prompter := NewScriptedPrompter(name, email, "1", "5", "5")
	// Hermeticity: wiz.Run reaches stepVerify, whose MCP survey
	// bypasses the injected backend and probes the real PATH + home
	// (same sandboxing as newWizardForMCPTest).
	t.Setenv("PATH", "")
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(tmpDir, "data")

	wiz := New(prompter, NewWriter(&buf), &cfg, filepath.Join(tmpDir, "config.yaml"), tmpDir)
	wiz.mcpBackend = &fakeMCPBackend{}
	return wiz, &buf
}

// TestStepIdentityValuesLandInConfig covers the plain typed-in path:
// both answers end up on cfg.Author, and the saved config round-trips
// them (persistence rides stepVerify's config.Save).
func TestStepIdentityValuesLandInConfig(t *testing.T) {
	wiz, _ := newWizardForIdentityTest(t, "Ada Lovelace", "ada@example.com")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if wiz.cfg.Author.Name != "Ada Lovelace" {
		t.Errorf("Author.Name = %q, want %q", wiz.cfg.Author.Name, "Ada Lovelace")
	}
	if wiz.cfg.Author.Email != "ada@example.com" {
		t.Errorf("Author.Email = %q, want %q", wiz.cfg.Author.Email, "ada@example.com")
	}

	// The wizard persisted via stepVerify; the on-disk config must
	// carry the identity too (dirty-input-to-stored-clean seam).
	loaded, err := config.Load(wiz.cfgPath)
	if err != nil {
		t.Fatalf("Load saved config: %v", err)
	}
	if loaded.Author.Name != "Ada Lovelace" || loaded.Author.Email != "ada@example.com" {
		t.Errorf("saved config Author = %+v, want Ada Lovelace / ada@example.com", loaded.Author)
	}
}

// TestStepIdentityEnterThroughUsesOSDefault is a wiring test for the
// lazy path: a bare Enter at the name prompt accepts the OS-account
// default (the osAccountNameFrom rules are pinned hermetically in
// TestOSAccountNameFrom), and a bare Enter at the email prompt leaves
// it blank. When the OS default is "" the name assertion would pass
// vacuously (blank answer and missing default are indistinguishable),
// so the test skips rather than pretend coverage.
func TestStepIdentityEnterThroughUsesOSDefault(t *testing.T) {
	want := OSAccountName()
	if want == "" {
		t.Skip("OS account name unavailable; enter-through default is indistinguishable from a blank answer")
	}

	wiz, _ := newWizardForIdentityTest(t, "", "")
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if wiz.cfg.Author.Name != want {
		t.Errorf("Author.Name = %q, want OS default %q", wiz.cfg.Author.Name, want)
	}
	if wiz.cfg.Author.Email != "" {
		t.Errorf("Author.Email = %q, want empty", wiz.cfg.Author.Email)
	}
}

// TestStepIdentityPresetSkipsPrompts covers the --author path:
// cli/init.go parses the flag into cfg.Author before the wizard is
// constructed, so the section must consume NO prompter answers and
// report the preset identity instead. The scripted answers here
// start at Step 0 -- if the identity section prompted anyway, "1"
// would be eaten as the name and the fresh-vs-import Choice would
// abort the run.
func TestStepIdentityPresetSkipsPrompts(t *testing.T) {
	tmpDir := t.TempDir()

	var buf bytes.Buffer
	prompter := NewScriptedPrompter("1", "5", "5")
	t.Setenv("PATH", "")
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(tmpDir, "data")
	cfg.Author = config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"}

	wiz := New(prompter, NewWriter(&buf), &cfg, filepath.Join(tmpDir, "config.yaml"), tmpDir)
	wiz.mcpBackend = &fakeMCPBackend{}

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Author identity: Ada Lovelace <ada@example.com>") {
		t.Errorf("preset identity not reported:\n%s", out)
	}
	if cfg.Author.Name != "Ada Lovelace" || cfg.Author.Email != "ada@example.com" {
		t.Errorf("preset Author mutated: %+v", cfg.Author)
	}
}
