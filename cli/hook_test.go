package cli

import (
	"strings"
	"testing"
)

func TestHookDispatchCoversAllEvents(t *testing.T) {
	want := []string{
		"session-start",
		"stop",
		"pre-compact",
		"post-compact",
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
