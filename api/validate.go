package api

import (
	"fmt"
	"math"
	"sync"

	"github.com/gramaton-ai/gramaton/config"
)

// Per-field length + cardinality limits used by api method validation.
// Keep these in sync with anything the HTTP layer enforces separately
// (e.g. JSON body size, ingest size) -- those limits are at a coarser
// layer and stay with the transport.
const (
	MaxKeywordLength   = 256
	MaxSourceRefLen    = 2048
	MaxContextFieldLen = 2048
	MaxEdgeTypeLen     = 256
	MaxMatchLength     = 1024
	MaxTopicLength     = 1024
	MaxFactLen         = 10000
	MaxMetaKeys        = 50
	MaxMetaKeyLen      = 64
	MaxMetaValueLen    = 1024
	MaxReembedBatch    = 500
)

// Search input cardinality limits.
const (
	MaxSearchTop      = 1000
	MaxMissingFields  = 50
	MaxSearchHops     = 10
	MaxExploreDepth   = 10
	MaxExploreNodes   = 10000
	MaxEdgeTypes      = 50
	MaxDuplicatePairs = 1000
	MaxLogLimit       = 500
	MaxLogTraversal   = 5000
)

// Enum sets. Exposed so transport-level validators (e.g. MCP schema
// hints) can reference the same source of truth.
var (
	ValidTemporalities = map[string]bool{
		"immutable": true, "durable": true, "temporal": true, "ephemeral": true,
	}
	ValidKnowledgeTypes = map[string]bool{
		"episodic": true, "semantic": true, "procedural": true,
		"conceptual": true, "reference": true, "collection": true,
	}
	ValidEpistemicStatuses = map[string]bool{
		"well_established": true, "probable": true, "speculative": true,
		"contested": true, "refuted": true,
	}
	ValidResolutions = map[string]bool{
		"completed": true, "superseded": true, "abandoned": true, "obsolete": true,
	}
)

// Process-level configurable limits. Seeded with safe defaults so
// tests and early-start code get reasonable behaviour before the
// server wires config in. API.SetLimits() overwrites them at server
// startup.
var (
	limitsMu sync.RWMutex
	limits   = config.LimitsConfig{
		MaxSummaryShort: 1000,
		MaxKeywords:     100,
	}
)

// SetLimits installs the configured limits. Zero values are preserved
// verbatim (no silent fallback) so config omissions surface clearly.
func SetLimits(lim config.LimitsConfig) {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	limits = lim
}

// MaxSummaryShort returns the configured cap on summary_short length
// in characters. Falls back to 1000 when unset.
func MaxSummaryShort() int {
	limitsMu.RLock()
	defer limitsMu.RUnlock()
	if limits.MaxSummaryShort <= 0 {
		return 1000
	}
	return limits.MaxSummaryShort
}

// MaxKeywords returns the configured cap on keyword list length.
// Falls back to 100 when unset.
func MaxKeywords() int {
	limitsMu.RLock()
	defer limitsMu.RUnlock()
	if limits.MaxKeywords <= 0 {
		return 100
	}
	return limits.MaxKeywords
}

// validateKeywords checks keyword count and per-keyword length.
func validateKeywords(keywords []string) error {
	max := MaxKeywords()
	if len(keywords) > max {
		return fmt.Errorf("maximum %d keywords allowed", max)
	}
	for _, kw := range keywords {
		if len(kw) > MaxKeywordLength {
			return fmt.Errorf("keyword exceeds maximum length of %d characters", MaxKeywordLength)
		}
	}
	return nil
}

// validateFloat64Range checks that a float64 pointer is in [min, max].
// Rejects NaN and Inf which would bypass comparison checks.
func validateFloat64Range(name string, val *float64, min, max float64) error {
	if val == nil {
		return nil
	}
	if math.IsNaN(*val) || math.IsInf(*val, 0) {
		return fmt.Errorf("%s must be a finite number", name)
	}
	if *val < min || *val > max {
		return fmt.Errorf("%s must be between %g and %g, got %g", name, min, max, *val)
	}
	return nil
}

// validateEnum checks that a string is in the allowed set. Empty
// strings are always accepted (treat optional fields as unset).
func validateEnum(name, val string, allowed map[string]bool) error {
	if val == "" {
		return nil
	}
	if !allowed[val] {
		keys := make([]string, 0, len(allowed))
		for k := range allowed {
			keys = append(keys, k)
		}
		return fmt.Errorf("%s must be one of %v, got %q", name, keys, val)
	}
	return nil
}

// validateMeta checks that meta keys and values are within limits and
// that values are supported types (string, float64, bool, []string
// coming through as []any from JSON).
func validateMeta(meta map[string]any) error {
	if len(meta) > MaxMetaKeys {
		return fmt.Errorf("meta exceeds maximum of %d keys", MaxMetaKeys)
	}
	for k, v := range meta {
		if len(k) > MaxMetaKeyLen {
			return fmt.Errorf("meta key %q exceeds maximum length of %d", k, MaxMetaKeyLen)
		}
		if k == "" {
			return fmt.Errorf("meta key must not be empty")
		}
		switch val := v.(type) {
		case string:
			if len(val) > MaxMetaValueLen {
				return fmt.Errorf("meta.%s value exceeds maximum length of %d", k, MaxMetaValueLen)
			}
		case float64, bool:
			// ok
		case []any:
			for i, elem := range val {
				s, ok := elem.(string)
				if !ok {
					return fmt.Errorf("meta.%s[%d] must be a string, got %T", k, i, elem)
				}
				if len(s) > MaxMetaValueLen {
					return fmt.Errorf("meta.%s[%d] exceeds maximum length of %d", k, i, MaxMetaValueLen)
				}
			}
		default:
			return fmt.Errorf("meta.%s has unsupported type %T (allowed: string, number, bool, string array)", k, v)
		}
	}
	return nil
}
