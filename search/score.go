package search

import (
	"math"
	"time"

	"github.com/brandonlattin/gramaton/config"
)

// ScoreInputs holds the values needed to compute a node's effective score.
// All values are read from the node's properties at query time.
type ScoreInputs struct {
	Similarity      float64 // cosine similarity to query (0.0-1.0)
	Temporality     string  // immutable, durable, temporal, ephemeral
	Confidence      float64 // 0.0-1.0
	Importance      float64 // 0.0-1.0
	AccessCount     int64
	LastAccessed    time.Time
	ActivationBoost float64
	ValidFrom       time.Time // zero if unset
	ValidUntil      time.Time // zero if unset
	CreatedAt       time.Time
	MaxAccessCount  int64 // max across the store, for frequency normalization
}

// ComputeScore calculates the effective_score for a node using the
// scoring model defined in retrieval.md. All scoring is computed at
// query time -- no scoring values are stored.
func ComputeScore(inputs ScoreInputs, now time.Time, cfg config.Config) float64 {
	sc := cfg.Scoring

	// Step 1: Validity multiplier.
	validityMult := 1.0
	if !inputs.ValidUntil.IsZero() && inputs.ValidUntil.Before(now) {
		validityMult = sc.HistoricalPenalty
	}

	// Step 2: Component scores.
	recency := accessRecency(inputs.Temporality, inputs.LastAccessed, now, cfg.Decay)
	freshness := knowledgeFreshness(inputs.Temporality, inputs.ValidFrom, inputs.CreatedAt, now, cfg.Freshness)
	frequency := frequencyScore(inputs.AccessCount, inputs.MaxAccessCount)
	activation := activationScore(inputs.ActivationBoost, cfg.Activation)

	score := validityMult * (sc.WeightSimilarity*inputs.Similarity +
		sc.WeightRecency*recency +
		sc.WeightFreshness*freshness +
		sc.WeightFrequency*frequency +
		sc.WeightActivation*activation +
		sc.WeightConfidence*inputs.Confidence)

	// Step 3: Importance floor.
	if inputs.Importance > sc.ImportanceThreshold {
		floor := inputs.Importance * sc.ImportanceFloor
		if floor > score {
			score = floor
		}
	}

	return score
}

// accessRecency computes e^(-decay_rate × hours_since_last_access).
// Returns 1.0 if never accessed (treat as fresh).
func accessRecency(temporality string, lastAccessed, now time.Time, cfg config.DecayConfig) float64 {
	if lastAccessed.IsZero() {
		return 1.0
	}
	hours := now.Sub(lastAccessed).Hours()
	if hours < 0 {
		hours = 0
	}
	rate := decayRate(temporality, cfg)
	return math.Exp(-rate * hours)
}

// knowledgeFreshness computes 1 / (1 + hours/scale)^exponent.
// Uses valid_from if set, otherwise created_at.
func knowledgeFreshness(temporality string, validFrom, createdAt, now time.Time, cfg config.FreshnessConfig) float64 {
	knowledgeTime := createdAt
	if !validFrom.IsZero() {
		knowledgeTime = validFrom
	}
	if knowledgeTime.IsZero() {
		return 1.0
	}

	hours := now.Sub(knowledgeTime).Hours()
	if hours < 0 {
		hours = 0
	}

	exp := freshnessExponent(temporality, cfg)
	if exp == 0 {
		return 1.0 // immutable: freshness is always 1.0
	}

	return math.Pow(1.0+hours/cfg.Scale, -exp)
}

// frequencyScore computes log(1 + access_count) / log(1 + max_access_count).
// Normalized to 0.0-1.0 across the store.
func frequencyScore(accessCount, maxAccessCount int64) float64 {
	if maxAccessCount <= 0 {
		return 0
	}
	return math.Log(1+float64(accessCount)) / math.Log(1+float64(maxAccessCount))
}

// activationScore normalizes activation_boost to [0, 1]. Full temporal
// decay of activation_boost (per retrieval.md) requires tracking when
// each boost was applied, which is deferred to v0.2. For v0.1, the raw
// accumulated boost is clamped.
func activationScore(boost float64, _ config.ActivationConfig) float64 {
	if boost <= 0 {
		return 0
	}
	if boost > 1.0 {
		return 1.0
	}
	return boost
}

func decayRate(temporality string, cfg config.DecayConfig) float64 {
	switch temporality {
	case "ephemeral":
		return cfg.Rates.Ephemeral
	case "temporal":
		return cfg.Rates.Temporal
	case "durable":
		return cfg.Rates.Durable
	case "immutable":
		return cfg.Rates.Immutable
	default:
		return cfg.Rates.Durable // conservative default
	}
}

func freshnessExponent(temporality string, cfg config.FreshnessConfig) float64 {
	switch temporality {
	case "immutable":
		return cfg.Exponents.Immutable
	case "durable":
		return cfg.Exponents.Durable
	case "temporal":
		return cfg.Exponents.Temporal
	case "ephemeral":
		return cfg.Exponents.Ephemeral
	default:
		return cfg.Exponents.Durable
	}
}
