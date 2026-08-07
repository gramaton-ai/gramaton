package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/gramaton-ai/gramaton/config"
)

// maxJSONBodySizeFallback is the safety-net cap used when no configured
// value is available (serverLimits not initialized, or Limits.MaxJSONSize
// <= 0). Production callers route through getMaxJSONSize() which honors
// config.Limits.MaxJSONSize (defaults to 2 MB).
const maxJSONBodySizeFallback int64 = 1 << 20 // 1MB

// maxIngestBodySize is the max request body size for ingest uploads.
const maxIngestBodySize = 200 << 20 // 200MB

// Server-level limits still consumed by live handlers.
const (
	maxLogLimit    = 500
	maxEdgeTypeLen = 256
)

// Per-field length limits that aren't in LimitsConfig.
const (
	maxKeywordLength   = 256
	maxSourceRefLen    = 2048
	maxContextFieldLen = 2048
)

// serverLimits holds the configurable validation limits for this
// process. Seeded with fallback defaults so tests that bypass
// Server.New() (or call package-level validators before a server is
// constructed) still get reasonable behavior. Server.New() overwrites
// it with config.LimitsConfig from the loaded config.
var (
	serverLimitsMu sync.RWMutex
	serverLimits   = config.LimitsConfig{
		MaxSummaryShort: 1000,
		MaxKeywords:     100,
	}
)

// setServerLimits overwrites the process-level validation limits.
// Called from Server.New() with cfg.Limits. Zero values are preserved
// to reveal config omissions rather than silently falling back.
func setServerLimits(lim config.LimitsConfig) {
	serverLimitsMu.Lock()
	defer serverLimitsMu.Unlock()
	serverLimits = lim
}

func getMaxSummaryShort() int {
	serverLimitsMu.RLock()
	defer serverLimitsMu.RUnlock()
	if serverLimits.MaxSummaryShort <= 0 {
		return 1000 // safety net if config omits the field
	}
	return serverLimits.MaxSummaryShort
}

func getMaxKeywords() int {
	serverLimitsMu.RLock()
	defer serverLimitsMu.RUnlock()
	if serverLimits.MaxKeywords <= 0 {
		return 100
	}
	return serverLimits.MaxKeywords
}

// getMaxJSONSize returns the configured JSON request body cap. Falls
// back to maxJSONBodySizeFallback when the limit is unset (zero or
// negative), preserving a 1 MB floor for tests that bypass Server.New.
func getMaxJSONSize() int64 {
	serverLimitsMu.RLock()
	defer serverLimitsMu.RUnlock()
	if serverLimits.MaxJSONSize <= 0 {
		return maxJSONBodySizeFallback
	}
	return int64(serverLimits.MaxJSONSize)
}

// validateKeywords checks keyword count and per-keyword length.
func validateKeywords(keywords []string) error {
	max := getMaxKeywords()
	if len(keywords) > max {
		return fmt.Errorf("maximum %d keywords allowed", max)
	}
	for _, kw := range keywords {
		if len(kw) > maxKeywordLength {
			return fmt.Errorf("keyword exceeds maximum length of %d characters", maxKeywordLength)
		}
	}
	return nil
}

// parseIntParam reads an integer query parameter with a default and max.
func parseIntParam(r *http.Request, name string, defaultVal, maxVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// errEmptyBody is returned by parseJSON when the request body is
// zero-length. Bindings whose body is optional can ignore this
// specific error via errors.Is, while still surfacing real parse
// failures (malformed JSON, oversized body) as 400.
var errEmptyBody = errors.New("empty request body")

// parseJSON reads and validates a JSON request body into target.
func parseJSON(r *http.Request, target any, maxSize int64) error {
	body := http.MaxBytesReader(nil, r.Body, maxSize)
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}

	if len(data) == 0 {
		return errEmptyBody
	}

	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 in request body")
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return nil
}

// validTemporalities is the set of valid temporality values.
var validTemporalities = map[string]bool{
	"immutable": true, "durable": true, "temporal": true, "ephemeral": true,
}

// validKnowledgeTypes is the set of valid knowledge_type values.
var validKnowledgeTypes = map[string]bool{
	"episodic": true, "semantic": true, "procedural": true, "conceptual": true, "reference": true, "collection": true,
}

// validEpistemicStatuses is the set of valid epistemic_status values.
var validEpistemicStatuses = map[string]bool{
	"well_established": true, "probable": true, "speculative": true, "contested": true, "refuted": true,
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

// Meta field limits.
const (
	maxMetaKeys     = 50
	maxMetaKeyLen   = 64
	maxMetaValueLen = 1024
)

// validateMeta checks that meta keys and values are within limits and
// that values are supported types (string, float64, bool, []string).
// Returns an error describing the first invalid entry found.
func validateMeta(meta map[string]any) error {
	if len(meta) > maxMetaKeys {
		return fmt.Errorf("meta exceeds maximum of %d keys", maxMetaKeys)
	}
	for k, v := range meta {
		if len(k) > maxMetaKeyLen {
			return fmt.Errorf("meta key %q exceeds maximum length of %d", k, maxMetaKeyLen)
		}
		if k == "" {
			return fmt.Errorf("meta key must not be empty")
		}
		switch val := v.(type) {
		case string:
			if len(val) > maxMetaValueLen {
				return fmt.Errorf("meta.%s value exceeds maximum length of %d", k, maxMetaValueLen)
			}
		case float64: // JSON numbers decode as float64
			// ok
		case bool:
			// ok
		case []any:
			// JSON arrays -- verify all elements are strings.
			for i, elem := range val {
				s, ok := elem.(string)
				if !ok {
					return fmt.Errorf("meta.%s[%d] must be a string, got %T", k, i, elem)
				}
				if len(s) > maxMetaValueLen {
					return fmt.Errorf("meta.%s[%d] exceeds maximum length of %d", k, i, maxMetaValueLen)
				}
			}
		default:
			return fmt.Errorf("meta.%s has unsupported type %T (allowed: string, number, bool, string array)", k, v)
		}
	}
	return nil
}

// validateEnum checks that a string is in the allowed set.
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
