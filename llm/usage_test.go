package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsageTrackerRecord(t *testing.T) {
	tracker := NewUsageTracker("", 0, 0, 0)

	tracker.Record(CallMetrics{
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-6",
		Task:         "classify",
		InputTokens:  1000,
		OutputTokens: 200,
		CostUSD:      0.01,
		LatencyMs:    500,
		Success:      true,
	})
	tracker.Record(CallMetrics{
		Provider:     "anthropic",
		Model:        "claude-haiku-4-5",
		Task:         "classify",
		InputTokens:  500,
		OutputTokens: 100,
		CostUSD:      0.003,
		LatencyMs:    200,
		Success:      true,
	})
	tracker.Record(CallMetrics{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Task:      "contradiction",
		LatencyMs: 800,
		Success:   false,
		ErrorType: "timeout",
	})

	s := tracker.Summary()

	if s.Session.Calls != 3 {
		t.Fatalf("expected 3 calls, got %d", s.Session.Calls)
	}
	if s.Session.ByTask["classify"] != 2 {
		t.Fatalf("expected 2 classify calls, got %d", s.Session.ByTask["classify"])
	}
	if s.Session.ByModel["claude-sonnet-4-6"] != 2 {
		t.Fatalf("expected 2 sonnet calls, got %d", s.Session.ByModel["claude-sonnet-4-6"])
	}
	if s.Session.ByModel["claude-haiku-4-5"] != 1 {
		t.Fatalf("expected 1 haiku call, got %d", s.Session.ByModel["claude-haiku-4-5"])
	}
	if s.Session.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", s.Session.Errors)
	}
	if s.Session.InputTokens != 1500 {
		t.Fatalf("expected 1500 input tokens, got %d", s.Session.InputTokens)
	}

	// Today and lifetime should match session for a fresh tracker.
	if s.Today.Calls != 3 {
		t.Fatalf("today calls should match session: got %d", s.Today.Calls)
	}
	if s.Lifetime.Calls != 3 {
		t.Fatalf("lifetime calls should match session: got %d", s.Lifetime.Calls)
	}
}

func TestUsageTrackerDailyCap(t *testing.T) {
	tracker := NewUsageTracker("", 3, 0, 0) // 3 calls/day cap

	for i := 0; i < 3; i++ {
		paused, _ := tracker.IsPaused()
		if paused {
			t.Fatalf("should not be paused after %d calls", i)
		}
		tracker.Record(CallMetrics{Task: "classify", Success: true})
	}

	paused, reason := tracker.IsPaused()
	if !paused {
		t.Fatal("should be paused after hitting daily cap")
	}
	if reason == "" {
		t.Fatal("pause reason should be set")
	}

	// Unpause for manual trigger.
	tracker.Unpause()
	paused, _ = tracker.IsPaused()
	if paused {
		t.Fatal("should be unpaused after Unpause()")
	}
}

// TestUsageTrackerDailyCostCap verifies the USD cost cap pauses when
// accumulated today.CostUSD exceeds the threshold. Count caps still
// serve as backstop for unknown-model cases where cost reads as 0.
func TestUsageTrackerDailyCostCap(t *testing.T) {
	// $1/day cap, no count caps.
	tracker := NewUsageTracker("", 0, 0, 1.0)

	// First call: $0.60 -- under cap.
	tracker.Record(CallMetrics{Task: "classify", Success: true, CostUSD: 0.60})
	if paused, _ := tracker.IsPaused(); paused {
		t.Fatal("should not pause at $0.60 with $1 cap")
	}

	// Second call: cumulative $1.20 -- over cap.
	tracker.Record(CallMetrics{Task: "classify", Success: true, CostUSD: 0.60})
	paused, reason := tracker.IsPaused()
	if !paused {
		t.Fatal("should pause when today.CostUSD exceeds cap")
	}
	if reason == "" {
		t.Fatal("pause reason should describe the cost cap")
	}
}

// TestUsageTrackerCostCapZeroDisabled verifies maxCostUSDPerDay=0
// leaves the cost cap disabled regardless of spend.
func TestUsageTrackerCostCapZeroDisabled(t *testing.T) {
	tracker := NewUsageTracker("", 0, 0, 0) // all caps off

	for i := 0; i < 5; i++ {
		tracker.Record(CallMetrics{Task: "classify", Success: true, CostUSD: 100.0})
	}
	if paused, _ := tracker.IsPaused(); paused {
		t.Fatal("no caps set should never pause")
	}
}

func TestUsageTrackerSessionCap(t *testing.T) {
	tracker := NewUsageTracker("", 0, 2, 0) // 2 calls/session cap

	tracker.Record(CallMetrics{Task: "classify", Success: true})
	paused, _ := tracker.IsPaused()
	if paused {
		t.Fatal("should not be paused after 1 call")
	}

	tracker.Record(CallMetrics{Task: "classify", Success: true})
	paused, reason := tracker.IsPaused()
	if !paused {
		t.Fatal("should be paused after hitting session cap")
	}
	if reason == "" {
		t.Fatal("pause reason should be set")
	}
}

func TestUsageTrackerPersistence(t *testing.T) {
	dir := t.TempDir()

	// Create tracker, record some usage, persist.
	t1 := NewUsageTracker(dir, 0, 0, 0)
	t1.Record(CallMetrics{
		Model:   "sonnet",
		Task:    "classify",
		CostUSD: 0.05,
		Credits: 1.2,
		Success: true,
	})
	t1.Record(CallMetrics{
		Model:   "haiku",
		Task:    "summarize",
		CostUSD: 0.01,
		Credits: 0.4,
		Success: true,
	})
	t1.Persist()

	// Verify file exists.
	path := filepath.Join(dir, "llm_usage.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("usage file not created: %v", err)
	}

	// Create new tracker from same dir -- should load persisted data.
	t2 := NewUsageTracker(dir, 0, 0, 0)
	s := t2.Summary()

	// Session should be fresh (0 calls).
	if s.Session.Calls != 0 {
		t.Fatalf("new session should have 0 calls, got %d", s.Session.Calls)
	}
	// Today should carry over.
	if s.Today.Calls != 2 {
		t.Fatalf("today should have 2 calls from previous session, got %d", s.Today.Calls)
	}
	// Lifetime should carry over.
	if s.Lifetime.Calls != 2 {
		t.Fatalf("lifetime should have 2 calls, got %d", s.Lifetime.Calls)
	}
	if s.Lifetime.ByModel["sonnet"] != 1 {
		t.Fatalf("expected 1 sonnet call in lifetime, got %d", s.Lifetime.ByModel["sonnet"])
	}
}

func TestUsageTrackerDailyCapPct(t *testing.T) {
	tracker := NewUsageTracker("", 100, 0, 0)

	if tracker.DailyCapPct() != 0 {
		t.Fatal("should be 0% with no calls")
	}

	for i := 0; i < 50; i++ {
		tracker.Record(CallMetrics{Task: "classify", Success: true})
	}
	if tracker.DailyCapPct() != 50 {
		t.Fatalf("expected 50%%, got %d%%", tracker.DailyCapPct())
	}
}

func TestUsageTrackerNoCap(t *testing.T) {
	tracker := NewUsageTracker("", 0, 0, 0) // no caps

	for i := 0; i < 1000; i++ {
		tracker.Record(CallMetrics{Task: "classify", Success: true})
	}

	paused, _ := tracker.IsPaused()
	if paused {
		t.Fatal("should never pause with no caps")
	}
	if tracker.DailyCapPct() != 0 {
		t.Fatal("cap pct should be 0 when no cap configured")
	}
}
