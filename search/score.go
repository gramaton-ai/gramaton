package search

import (
	"math"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

// ScoreInputs holds the values needed to compute a node's effective score.
// All values are read from the node's properties at query time.
type ScoreInputs struct {
	Similarity      float64 // cosine similarity to query (0.0-1.0)
	Temporality     string  // immutable, durable, temporal, ephemeral
	Confidence      float64 // 0.0-1.0
	Importance      float64 // 0.0-1.0
	AccessCount     int64
	ActivationBoost float64 // spreading activation from neighbors
	ValidFrom       time.Time // zero if unset
	ValidUntil      time.Time // zero if unset
	CreatedAt       time.Time
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
	freshness := knowledgeFreshness(inputs.Temporality, inputs.ValidFrom, inputs.CreatedAt, now, cfg.Freshness)
	activation := actrActivation(inputs.AccessCount, inputs.CreatedAt, inputs.ActivationBoost, now)

	score := validityMult * (sc.WeightSimilarity*inputs.Similarity +
		sc.WeightFreshness*freshness +
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

	scale := cfg.Scale
	if scale <= 0 {
		scale = 8760 // default: 1 year in hours
	}
	return math.Pow(1.0+hours/scale, -exp)
}

// actrActivation computes a unified usage signal based on Anderson's
// ACT-R base-level activation equation. This replaces the separate
// frequency and recency signals with a single theoretically-grounded
// formula that naturally combines both.
//
// Approximation: B = ln(n / (1-d)) - d * ln(L)
// where n = access count (min 1, treating creation as first access),
// L = lifetime in hours, d = 0.5 (canonical decay parameter).
//
// The spreading activation boost from neighbors is added after the
// base-level activation, matching ACT-R's full equation: A = B + S.
//
// Output is normalized to [0, 1] via a sigmoid for compatibility with
// the weighted scoring model.
//
// Reference: Anderson & Schooler 1991, "Reflections of the Environment
// in Memory." The activation equation is the optimal predictor of
// information need given past access patterns.
func actrActivation(accessCount int64, createdAt time.Time, spreadingBoost float64, now time.Time) float64 {
	const d = 0.5 // canonical ACT-R decay parameter

	// Treat creation as first access: n is always >= 1.
	n := float64(accessCount)
	if n < 1 {
		n = 1
	}

	// Lifetime in hours (minimum 1 hour to avoid log(0)).
	L := now.Sub(createdAt).Hours()
	if L < 1 {
		L = 1
	}

	// ACT-R base-level activation approximation.
	B := math.Log(n/(1-d)) - d*math.Log(L)

	// Add spreading activation from neighbors (ACT-R: A = B + S).
	A := B + spreadingBoost

	// Normalize to [0, 1] via sigmoid. The raw activation ranges
	// roughly from -5 (old, never accessed) to +10 (heavily used,
	// recently created). Sigmoid with scale=2 maps this to a usable
	// range where most values fall between 0.1 and 0.9.
	return 1.0 / (1.0 + math.Exp(-A/2.0))
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
