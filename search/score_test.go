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

// --- Access recency ---

func TestAccessRecencyJustAccessed(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	r := accessRecency("durable", now, now, cfg.Decay)
	if !approx(r, 1.0, 0.001) {
		t.Fatalf("just accessed: expected ~1.0, got %f", r)
	}
}

func TestAccessRecencyNeverAccessed(t *testing.T) {
	cfg := defaultCfg()
	r := accessRecency("durable", time.Time{}, time.Now().UTC(), cfg.Decay)
	if r != 1.0 {
		t.Fatalf("never accessed: expected 1.0, got %f", r)
	}
}

func TestAccessRecencyEphemeralDecaysFast(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	// 8 hours ago (2 half-lives for ephemeral at 4h half-life).
	lastAccessed := now.Add(-8 * time.Hour)
	r := accessRecency("ephemeral", lastAccessed, now, cfg.Decay)
	// e^(-0.173 * 8) = e^(-1.384) ≈ 0.25
	if !approx(r, 0.25, 0.05) {
		t.Fatalf("ephemeral after 8h: expected ~0.25, got %f", r)
	}
}

func TestAccessRecencyDurableDecaysSlow(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	// 7 days ago.
	lastAccessed := now.Add(-7 * 24 * time.Hour)
	r := accessRecency("durable", lastAccessed, now, cfg.Decay)
	// e^(-0.000321 * 168) ≈ 0.947
	if !approx(r, 0.947, 0.02) {
		t.Fatalf("durable after 7d: expected ~0.947, got %f", r)
	}
}

func TestAccessRecencyImmutableNeverDecays(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	lastAccessed := now.Add(-365 * 24 * time.Hour)
	r := accessRecency("immutable", lastAccessed, now, cfg.Decay)
	if r != 1.0 {
		t.Fatalf("immutable: expected 1.0, got %f", r)
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
	created := now.Add(-1 * time.Hour)                // recent creation
	validFrom := now.Add(-2 * 365 * 24 * time.Hour) // but knowledge is 2 years old
	f := knowledgeFreshness("durable", validFrom, created, now, cfg.Freshness)
	// Should use validFrom, not createdAt.
	// 2 years ≈ 17520 hours. 1/(1 + 17520/8760)^0.5 = 1/(3)^0.5 ≈ 0.577
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

// --- Frequency score ---

func TestFrequencyScoreZero(t *testing.T) {
	f := frequencyScore(0, 100)
	if f != 0.0 {
		t.Fatalf("expected 0.0, got %f", f)
	}
}

func TestFrequencyScoreMax(t *testing.T) {
	f := frequencyScore(100, 100)
	if !approx(f, 1.0, 0.001) {
		t.Fatalf("max access: expected ~1.0, got %f", f)
	}
}

func TestFrequencyScoreMiddle(t *testing.T) {
	f := frequencyScore(50, 100)
	// log(51) / log(101) ≈ 0.85
	if f < 0.5 || f > 1.0 {
		t.Fatalf("mid access: expected 0.5-1.0, got %f", f)
	}
}

func TestFrequencyScoreZeroMax(t *testing.T) {
	f := frequencyScore(5, 0)
	if f != 0 {
		t.Fatalf("zero max: expected 0, got %f", f)
	}
}

// --- Full scoring ---

func TestComputeScoreBasic(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	inputs := ScoreInputs{
		Similarity:     0.8,
		Temporality:    "durable",
		Confidence:     0.9,
		Importance:     0.5,
		AccessCount:    10,
		LastAccessed:   now.Add(-1 * time.Hour),
		CreatedAt:      now.Add(-24 * time.Hour),
		MaxAccessCount: 50,
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
		Similarity:     0.8,
		Temporality:    "durable",
		Confidence:     0.9,
		CreatedAt:      now.Add(-24 * time.Hour),
		MaxAccessCount: 1,
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
		Similarity:     0.0, // no vector match
		Temporality:    "durable",
		Confidence:     0.1, // low confidence
		Importance:     0.9, // but very important
		CreatedAt:      now.Add(-365 * 24 * time.Hour),
		MaxAccessCount: 1,
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
		Temporality:    "durable",
		Confidence:     0.5,
		CreatedAt:      now,
		MaxAccessCount: 1,
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
