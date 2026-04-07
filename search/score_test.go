package search

import (
	"math"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/config"
)

func defaultCfg() config.Config {
	return config.Defaults()
}

func approx(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

// --- ACT-R activation ---

func TestACTRActivationNewRecord(t *testing.T) {
	now := time.Now().UTC()
	// Brand new record, just created, 1 access (birth).
	a := actrActivation(0, now, 0, now)
	// Should be moderate -- new record, not yet proven useful.
	if a < 0.3 || a > 0.8 {
		t.Fatalf("new record activation: expected 0.3-0.8, got %f", a)
	}
}

func TestACTRActivationFrequentRecent(t *testing.T) {
	now := time.Now().UTC()
	// 50 accesses, created 1 day ago.
	a := actrActivation(50, now.Add(-24*time.Hour), 0, now)
	// Should be high -- heavily used and recent.
	if a < 0.7 {
		t.Fatalf("frequent recent: expected > 0.7, got %f", a)
	}
}

func TestACTRActivationOldUnused(t *testing.T) {
	now := time.Now().UTC()
	// 0 accesses, created 1 year ago.
	a := actrActivation(0, now.Add(-365*24*time.Hour), 0, now)
	// Should be low -- old and never used.
	if a > 0.4 {
		t.Fatalf("old unused: expected < 0.4, got %f", a)
	}
}

func TestACTRActivationOldHeavilyUsed(t *testing.T) {
	now := time.Now().UTC()
	// 500 accesses, created 1 year ago.
	a := actrActivation(500, now.Add(-365*24*time.Hour), 0, now)
	// Should be moderate-to-high -- many accesses offset the age.
	if a < 0.5 {
		t.Fatalf("old heavily used: expected > 0.5, got %f", a)
	}
}

func TestACTRActivationSpreadingBoost(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-24 * time.Hour)
	base := actrActivation(5, created, 0, now)
	boosted := actrActivation(5, created, 2.0, now)
	if boosted <= base {
		t.Fatalf("spreading boost should increase activation: base=%f, boosted=%f", base, boosted)
	}
}

func TestACTRActivationMonotonic(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-7 * 24 * time.Hour)
	// More accesses should give higher activation.
	a1 := actrActivation(1, created, 0, now)
	a10 := actrActivation(10, created, 0, now)
	a100 := actrActivation(100, created, 0, now)
	if a1 >= a10 || a10 >= a100 {
		t.Fatalf("activation should increase with access count: a1=%f, a10=%f, a100=%f", a1, a10, a100)
	}
}

func TestACTRActivationBounded(t *testing.T) {
	now := time.Now().UTC()
	// Extreme cases should stay in [0, 1].
	aMin := actrActivation(0, now.Add(-10*365*24*time.Hour), 0, now)
	aMax := actrActivation(10000, now, 5.0, now)
	if aMin < 0 || aMin > 1 {
		t.Fatalf("min case out of bounds: %f", aMin)
	}
	if aMax < 0 || aMax > 1 {
		t.Fatalf("max case out of bounds: %f", aMax)
	}
}

// --- Knowledge freshness ---

func TestFreshnessImmutableAlwaysOne(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	created := now.Add(-10 * 365 * 24 * time.Hour) // 10 years ago
	f := knowledgeFreshness("immutable", time.Time{}, created, now, cfg.Freshness)
	if f != 1.0 {
		t.Fatalf("immutable freshness: expected 1.0, got %f", f)
	}
}

func TestFreshnessDurableSixMonths(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	created := now.Add(-6 * 30 * 24 * time.Hour) // ~6 months
	f := knowledgeFreshness("durable", time.Time{}, created, now, cfg.Freshness)
	// ~0.82 per design doc table
	if !approx(f, 0.82, 0.05) {
		t.Fatalf("durable 6mo: expected ~0.82, got %f", f)
	}
}

func TestFreshnessUsesValidFrom(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	created := now.Add(-1 * time.Hour)              // recent creation
	validFrom := now.Add(-2 * 365 * 24 * time.Hour) // but knowledge is 2 years old
	f := knowledgeFreshness("durable", validFrom, created, now, cfg.Freshness)
	// Should use validFrom, not createdAt.
	if !approx(f, 0.577, 0.05) {
		t.Fatalf("durable 2yr via valid_from: expected ~0.577, got %f", f)
	}
}

func TestFreshnessNoTimestamp(t *testing.T) {
	cfg := defaultCfg()
	f := knowledgeFreshness("durable", time.Time{}, time.Time{}, time.Now().UTC(), cfg.Freshness)
	if f != 1.0 {
		t.Fatalf("no timestamp: expected 1.0, got %f", f)
	}
}

// --- Full scoring ---

func TestComputeScoreBasic(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	inputs := ScoreInputs{
		Similarity:  0.8,
		Temporality: "durable",
		Confidence:  0.9,
		Importance:  0.5,
		AccessCount: 10,
		CreatedAt:   now.Add(-24 * time.Hour),
	}

	score := ComputeScore(inputs, now, cfg)
	if score <= 0 || score > 1.0 {
		t.Fatalf("expected score in (0, 1], got %f", score)
	}
}

func TestComputeScoreHistoricalPenalty(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	base := ScoreInputs{
		Similarity:  0.8,
		Temporality: "durable",
		Confidence:  0.9,
		CreatedAt:   now.Add(-24 * time.Hour),
	}

	// Without valid_until (current).
	scoreCurrent := ComputeScore(base, now, cfg)

	// With valid_until in the past (historical).
	historical := base
	historical.ValidUntil = now.Add(-1 * time.Hour)
	scoreHistorical := ComputeScore(historical, now, cfg)

	if scoreHistorical >= scoreCurrent {
		t.Fatalf("historical should score lower: current=%f, historical=%f", scoreCurrent, scoreHistorical)
	}
	// Should be approximately half (penalty is 0.5).
	ratio := scoreHistorical / scoreCurrent
	if !approx(ratio, 0.5, 0.01) {
		t.Fatalf("expected penalty ratio ~0.5, got %f", ratio)
	}
}

func TestComputeScoreImportanceFloor(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	inputs := ScoreInputs{
		Similarity:  0.0, // no vector match
		Temporality: "durable",
		Confidence:  0.1, // low confidence
		Importance:  0.9, // but very important
		CreatedAt:   now.Add(-365 * 24 * time.Hour),
	}

	score := ComputeScore(inputs, now, cfg)
	floor := 0.9 * cfg.Scoring.ImportanceFloor // 0.45
	if score < floor {
		t.Fatalf("importance floor not applied: score=%f, expected >= %f", score, floor)
	}
}

func TestComputeScoreHighSimilarityWins(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	base := ScoreInputs{
		Temporality: "durable",
		Confidence:  0.5,
		CreatedAt:   now,
	}

	highSim := base
	highSim.Similarity = 0.95

	lowSim := base
	lowSim.Similarity = 0.1

	scoreHigh := ComputeScore(highSim, now, cfg)
	scoreLow := ComputeScore(lowSim, now, cfg)

	if scoreHigh <= scoreLow {
		t.Fatalf("higher similarity should score higher: high=%f, low=%f", scoreHigh, scoreLow)
	}
}
