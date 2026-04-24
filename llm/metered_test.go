package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// stubProvider implements Provider with deterministic responses and a
// counter so tests can verify whether the inner provider was reached.
type stubProvider struct {
	calls    int
	response string
	err      error
}

func (s *stubProvider) Complete(_ context.Context, _ string) (string, error) {
	s.calls++
	return s.response, s.err
}

func (s *stubProvider) CompleteWithModel(_ context.Context, _, _ string) (string, error) {
	s.calls++
	return s.response, s.err
}

func (s *stubProvider) ModelID() string      { return "stub-model" }
func (s *stubProvider) ProviderName() string { return "stub" }

// TestMeteredRefusesWhenCapped is a regression test: once a
// UsageTracker reports paused=true, Metered must short-circuit
// before invoking the inner provider. The previous behaviour
// (post-call cap check) burned tokens on the call that flipped the
// cap and on every subsequent call until the cap was manually
// inspected.
func TestMeteredRefusesWhenCapped(t *testing.T) {
	tracker := NewUsageTracker(t.TempDir(), 0, 1, 0) // 1 call/session cap.
	stub := &stubProvider{response: "ok"}
	m := NewMetered(stub, tracker, nil)

	// First call lands and trips the cap.
	if _, err := m.Complete(context.Background(), "ping"); err != nil {
		t.Fatalf("first call: unexpected error %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("first call: stub.calls = %d, want 1", stub.calls)
	}

	// Second call must return ErrCapped without reaching the inner.
	_, err := m.Complete(context.Background(), "ping")
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("second call: err = %v, want ErrCapped", err)
	}
	if stub.calls != 1 {
		t.Fatalf("second call: stub.calls = %d (must NOT have called inner)", stub.calls)
	}

	// Same for CompleteWithModel.
	_, err = m.CompleteWithModel(context.Background(), "alt-model", "ping")
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("third call (with model): err = %v, want ErrCapped", err)
	}
	if stub.calls != 1 {
		t.Fatalf("third call: stub.calls = %d (must NOT have called inner)", stub.calls)
	}

	// Unpause and verify the inner is reachable again.
	tracker.Unpause()
	if _, err := m.Complete(context.Background(), "ping"); err != nil {
		t.Fatalf("post-unpause call: unexpected error %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("post-unpause call: stub.calls = %d, want 2", stub.calls)
	}
}

// TestMeteredPropagatesProviderName verifies that CallMetrics.Provider
// reflects the inner provider's ProviderName() instead of the hardcoded
// "metered" string. This is what lets per-provider accounting work when
// multiple backends are in play.
func TestMeteredPropagatesProviderName(t *testing.T) {
	tracker := NewUsageTracker(t.TempDir(), 0, 0, 0)
	stub := &stubProvider{response: "ok"}
	m := NewMetered(stub, tracker, nil)

	if _, err := m.Complete(context.Background(), "ping"); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	summary := tracker.Summary()
	if got := summary.Session.Calls; got != 1 {
		t.Fatalf("Session.Calls = %d, want 1", got)
	}
	// Records are aggregated by model, not provider, in UsageStats.
	// But ProviderName() is the contract we expose -- check it directly.
	if got := m.ProviderName(); got != "stub" {
		t.Errorf("Metered.ProviderName() = %q, want %q (inner's name, not 'metered')", got, "stub")
	}
}

// TestMeteredRecordCallAccountsForBypassPath verifies RecordCall
// feeds the tracker identically to the Complete path. Regression
// test for the classification Message Batches API which goes
// around Complete entirely -- without RecordCall its token usage
// was invisible to llm_usage.json and evaded max_calls_per_day.
func TestMeteredRecordCallAccountsForBypassPath(t *testing.T) {
	tracker := NewUsageTracker(t.TempDir(), 0, 0, 0)
	stub := &stubProvider{response: "ok"}
	m := NewMetered(stub, tracker, nil)

	m.RecordCall("claude-sonnet-4-6", "classification_long",
		telemetry.CallUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 0, CacheWriteTokens: 0},
		0, nil)

	summary := tracker.Summary()
	if got := summary.Session.Calls; got != 1 {
		t.Fatalf("Session.Calls = %d, want 1", got)
	}
	if got := summary.Session.InputTokens; got != 100 {
		t.Errorf("Session.InputTokens = %d, want 100", got)
	}
	if got := summary.Session.OutputTokens; got != 50 {
		t.Errorf("Session.OutputTokens = %d, want 50", got)
	}
	if got, want := summary.Session.ByTask["classification_long"], 1; got != want {
		t.Errorf("ByTask[classification_long] = %d, want %d", got, want)
	}
	// Inner provider MUST NOT have been touched -- RecordCall is for
	// paths that already made the call through another channel.
	if stub.calls != 0 {
		t.Errorf("stub.calls = %d, want 0 (RecordCall should not invoke inner)", stub.calls)
	}
}

// TestMeteredRecordCallErrorPath verifies RecordCall flagged with
// a non-nil error still records usage + counts toward caps (the
// tokens were burned whether or not the caller marked the outcome
// as successful). Regression against a future change that tries
// to skip recording on error.
func TestMeteredRecordCallErrorPath(t *testing.T) {
	tracker := NewUsageTracker(t.TempDir(), 0, 0, 0)
	stub := &stubProvider{response: "ok"}
	m := NewMetered(stub, tracker, nil)

	m.RecordCall("claude-haiku-4-5", "classification_short",
		telemetry.CallUsage{InputTokens: 42, OutputTokens: 7},
		0, errors.New("timeout"))

	summary := tracker.Summary()
	if got := summary.Session.Calls; got != 1 {
		t.Errorf("Session.Calls = %d, want 1 (error path must still count)", got)
	}
	if got := summary.Session.InputTokens; got != 42 {
		t.Errorf("Session.InputTokens = %d, want 42", got)
	}
}

// TestMeteredAllowsWithoutTracker confirms a nil tracker disables
// cap enforcement entirely (used in tests and benchmarks).
func TestMeteredAllowsWithoutTracker(t *testing.T) {
	stub := &stubProvider{response: "ok"}
	m := NewMetered(stub, nil, nil)

	for i := 0; i < 5; i++ {
		if _, err := m.Complete(context.Background(), "ping"); err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
	}
	if stub.calls != 5 {
		t.Fatalf("stub.calls = %d, want 5", stub.calls)
	}
}
