package search

import (
	"math"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

// ScoreInputs holds the values needed to compute a node's effective score.
// All values are read from the node's properties at query time.
type ScoreInputs struct {
	Similarity   float64 // cosine similarity to query (0.0-1.0)
	HasTextQuery bool    // true when the search includes a text query
	Temporality  string  // immutable, durable, temporal, ephemeral
	Confidence   float64 // 0.0-1.0
	Importance   float64 // 0.0-1.0
	// AccessCount is bookkeeping only -- it plays no scoring role.
	// The activation term was removed deliberately: a popularity
	// prior that similarity plus trust metadata outperform, that
	// never decays, and that entrenched stale incumbents against
	// their own corrections.
	AccessCount int64
	ValidFrom   time.Time // zero if unset
	ValidUntil  time.Time // zero if unset
	CreatedAt   time.Time
}

// ComputeScore calculates the effective_score for a node using the
// scoring model defined in retrieval.md. All scoring is computed at
// query time -- no scoring values are stored.
//
// When a text query is present (HasTextQuery=true), similarity acts
// as a gate: low similarity drags down the entire score. This prevents
// freshness and confidence from propping up irrelevant results. When
// no text query is present (filter-only searches), the score is
// purely metadata-based.
func ComputeScore(inputs ScoreInputs, now time.Time, cfg config.Config) float64 {
	sc := cfg.Scoring

	// Step 1: Validity multiplier.
	validityMult := 1.0
	if !inputs.ValidUntil.IsZero() && inputs.ValidUntil.Before(now) {
		validityMult = sc.HistoricalPenalty
	}

	// Step 2: Component scores.
	freshness := knowledgeFreshness(inputs.Temporality, inputs.ValidFrom, inputs.CreatedAt, now, cfg.Freshness)

	if inputs.HasTextQuery {
		// Text query present: similarity gates the metadata signals.
		//
		// The score is similarity * (weighted metadata blend). When
		// similarity is near 0, the whole score approaches 0 regardless
		// of how fresh or confident the record is. When similarity is
		// high, metadata signals boost the best matches.
		//
		// The metadata blend is normalized to [0, 1]: freshness and
		// confidence each contribute proportionally.
		metaWeight := sc.WeightFreshness + sc.WeightConfidence
		metaScore := 0.0
		if metaWeight > 0 {
			metaScore = (sc.WeightFreshness*freshness +
				sc.WeightConfidence*inputs.Confidence) / metaWeight
		}

		// Blend: similarity dominates, metadata adjusts within the
		// relevance band. At sim=1.0, full metadata boost. At sim=0.0,
		// score is 0 regardless of metadata.
		score := validityMult * inputs.Similarity * (sc.WeightSimilarity + (1-sc.WeightSimilarity)*metaScore)

		// Step 3: Importance floor.
		if inputs.Importance > sc.ImportanceThreshold {
			floor := inputs.Importance * sc.ImportanceFloor
			if floor > score {
				score = floor
			}
		}
		return score
	}

	// No text query (filter-only): purely metadata-based score.
	score := validityMult * (sc.WeightFreshness*freshness +
		sc.WeightConfidence*inputs.Confidence)

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
