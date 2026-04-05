package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"
)

// maxJSONBodySize is the default max request body size for JSON endpoints.
const maxJSONBodySize = 1 << 20 // 1MB

// maxIngestBodySize is the max request body size for ingest uploads.
const maxIngestBodySize = 200 << 20 // 200MB

// Search input limits.
const (
	maxSearchTop      = 1000
	maxKeywords       = 100
	maxMissingFields  = 50
	maxMatchLength    = 1024
	maxExploreDepth   = 10
	maxEdgeTypes      = 50
	maxDuplicatePairs = 1000
	maxLogLimit       = 500
	maxLogTraversal   = 5000
	maxTopicLength    = 1024
)

// parseJSON reads and validates a JSON request body into target.
func parseJSON(r *http.Request, target any, maxSize int64) error {
	body := http.MaxBytesReader(nil, r.Body, maxSize)
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("empty request body")
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
	"episodic": true, "semantic": true, "procedural": true, "conceptual": true, "reference": true,
}

// validEpistemicStatuses is the set of valid epistemic_status values.
var validEpistemicStatuses = map[string]bool{
	"well_established": true, "probable": true, "speculative": true, "contested": true, "refuted": true,
}

// validateFloat64Range checks that a float64 pointer is in [min, max].
func validateFloat64Range(name string, val *float64, min, max float64) error {
	if val == nil {
		return nil
	}
	if *val < min || *val > max {
		return fmt.Errorf("%s must be between %g and %g, got %g", name, min, max, *val)
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
