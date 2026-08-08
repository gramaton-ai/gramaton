package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/setup"
)

// TestHookDispatchCoversAllEvents pins the dispatch table's KEYS.
// It deliberately does not verify which handler each key maps to —
// the closure wrappers make that unobservable here; the per-handler
// behavior is pinned in the hooks package's own tests.
func TestHookDispatchCoversAllEvents(t *testing.T) {
	want := []string{
		"session-start",
		"stop",
		"pre-compact",
		"post-compact",
		"user-prompt-submit",
		"kiro-agent-spawn",
		"kiro-user-prompt-submit",
		"kiro-stop",
		"cursor-session-start",
		"cursor-stop",
		"cursor-pre-compact",
	}
	for _, ev := range want {
		if _, ok := hookEvents[ev]; !ok {
			t.Errorf("hookEvents table missing event %q", ev)
		}
	}
	if len(hookEvents) != len(want) {
		t.Errorf("hookEvents has %d entries, want %d (extra entries?)", len(hookEvents), len(want))
	}
}

// TestHookProxyEventsAreDispatchable materializes every harness's
// proxy scripts and asserts each script's `gramaton hook <event>`
// token resolves in the hookEvents dispatch table. Without this, a
// typo in a registry cliEvent ships proxy scripts that invoke an
// unknown event — failing only when a user's harness fires the hook.
func TestHookProxyEventsAreDispatchable(t *testing.T) {
	tmp := t.TempDir()
	re := regexp.MustCompile(`gramaton hook ([a-z-]+)`)
	// Embed-dir names mirror the setup harness registry's
	// HookEmbedDir values; when a harness is added there, add it
	// here (the registry itself is unexported by design).
	for _, client := range []string{"claude-code", "kiro", "codex", "cursor"} {
		paths, err := (setup.DefaultHookBackend{}).Materialize(client, tmp)
		if err != nil {
			t.Fatalf("%s: materialize: %v", client, err)
		}
		if len(paths) == 0 {
			t.Fatalf("%s: no proxy scripts materialized", client)
		}
		for _, p := range paths {
			body, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			m := re.FindSubmatch(body)
			if m == nil {
				t.Errorf("%s: no `gramaton hook <event>` token in %s", client, filepath.Base(p))
				continue
			}
			if ev := string(m[1]); hookEvents[ev] == nil {
				t.Errorf("%s: proxy %s invokes unknown hook event %q", client, filepath.Base(p), ev)
			}
		}
	}
}

func TestHookUnknownEventReturnsError(t *testing.T) {
	err := runHook(nil, []string{"nonexistent-event"})
	if err == nil {
		t.Fatal("unknown event should return error")
	}
	if !strings.Contains(err.Error(), "unknown hook event") {
		t.Errorf("error message missing context: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-event") {
		t.Errorf("error should quote the bad event name: %v", err)
	}
}
