package search

import (
	"math"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

func defaultCfg() config.Config {
	return config.Defaults()
}

func approx(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

// --- Knowledge freshness ---

func TestFreshnessImmutableAlwaysOne(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	created := now.Add(-10 * 365 * 24 * time.Hour) // 10 years ago
	f := knowledgeFreshness("immutable", time.Time{}, time.Time{}, created, now, cfg.Freshness)
	if f != 1.0 {
		t.Fatalf("immutable freshness: expected 1.0, got %f", f)
	}
}

func TestFreshnessDurableSixMonths(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	created := now.Add(-6 * 30 * 24 * time.Hour) // ~6 months
	f := knowledgeFreshness("durable", time.Time{}, time.Time{}, created, now, cfg.Freshness)
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
	f := knowledgeFreshness("durable", validFrom, time.Time{}, created, now, cfg.Freshness)
	// Should use validFrom, not createdAt.
	if !approx(f, 0.577, 0.05) {
		t.Fatalf("durable 2yr via valid_from: expected ~0.577, got %f", f)
	}
}

func TestFreshnessNoTimestamp(t *testing.T) {
	cfg := defaultCfg()
	f := knowledgeFreshness("durable", time.Time{}, time.Time{}, time.Time{}, time.Now().UTC(), cfg.Freshness)
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
		HasTextQuery: true,
		Temporality:  "durable",
		Confidence:   0.5,
		CreatedAt:    now,
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

func TestComputeScoreSimilarityGatesMetadata(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	// A record with zero similarity but great metadata should score
	// near zero when a text query is present.
	irrelevant := ScoreInputs{
		HasTextQuery: true,
		Similarity:   0.0,
		Temporality:  "durable",
		Confidence:   0.95,
		AccessCount:  100,
		CreatedAt:    now,
	}
	scoreIrrelevant := ComputeScore(irrelevant, now, cfg)
	if scoreIrrelevant > 0.05 {
		t.Fatalf("irrelevant record (sim=0) should score near 0 with text query, got %f", scoreIrrelevant)
	}

	// Same metadata but with some similarity should score much higher.
	relevant := irrelevant
	relevant.Similarity = 0.7
	scoreRelevant := ComputeScore(relevant, now, cfg)
	if scoreRelevant <= scoreIrrelevant*5 {
		t.Fatalf("relevant record should score much higher than irrelevant: relevant=%f, irrelevant=%f", scoreRelevant, scoreIrrelevant)
	}
}

func TestComputeScoreFilterOnlyIgnoresSimilarity(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	// Without text query, similarity is not used.
	inputs := ScoreInputs{
		HasTextQuery: false,
		Similarity:   0.0,
		Temporality:  "durable",
		Confidence:   0.9,
		AccessCount:  10,
		CreatedAt:    now,
	}
	score := ComputeScore(inputs, now, cfg)
	if score < 0.1 {
		t.Fatalf("filter-only query with good metadata should score reasonably, got %f", score)
	}
}

func TestComputeScoreMetadataBoostsRelevant(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()

	// Two records with same similarity but different metadata.
	// Better metadata should rank higher.
	good := ScoreInputs{
		HasTextQuery: true,
		Similarity:   0.8,
		Temporality:  "durable",
		Confidence:   0.95,
		AccessCount:  50,
		CreatedAt:    now,
	}
	poor := ScoreInputs{
		HasTextQuery: true,
		Similarity:   0.8,
		Temporality:  "ephemeral",
		Confidence:   0.2,
		AccessCount:  0,
		CreatedAt:    now.Add(-365 * 24 * time.Hour),
	}

	scoreGood := ComputeScore(good, now, cfg)
	scorePoor := ComputeScore(poor, now, cfg)
	if scoreGood <= scorePoor {
		t.Fatalf("better metadata should boost score: good=%f, poor=%f", scoreGood, scorePoor)
	}
}

func TestFreshnessUpdatedAtRestoresFreshness(t *testing.T) {
	cfg := defaultCfg()
	now := time.Now().UTC()
	oldCreated := now.Add(-2 * 365 * 24 * time.Hour)
	// An old record freshly revised must score fresher than its
	// unrevised twin: the revision is when its knowledge is from.
	stale := knowledgeFreshness("durable", time.Time{}, time.Time{}, oldCreated, now, cfg.Freshness)
	revised := knowledgeFreshness("durable", time.Time{}, now.Add(-time.Hour), oldCreated, now, cfg.Freshness)
	if revised <= stale {
		t.Fatalf("revised freshness %f <= stale %f; updates must restore freshness", revised, stale)
	}
	// A deliberate valid_from still wins over updated_at.
	validFrom := now.Add(-3 * 365 * 24 * time.Hour)
	anchored := knowledgeFreshness("durable", validFrom, now, oldCreated, now, cfg.Freshness)
	if anchored >= revised {
		t.Fatalf("valid_from must anchor freshness even when updated_at is recent: %f >= %f", anchored, revised)
	}
}
