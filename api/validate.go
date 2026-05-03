package api

import (
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

// clientSessionIDRe bounds client_session_id to characters safe for
// filesystem paths. Rejects path-separator segments (`.` `..`, `/`, `\`)
// and every other shell/glob metacharacter. Keeps ULID/UUID and hyphen/
// underscore-delimited identifiers while denying traversal primitives.
var clientSessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// clientTokenRe bounds capture_batch client_token to UUID v1-v5 shape.
// Idempotency relies on collision-resistance, not strict version: any
// 8-4-4-4-12 hex string is accepted.
var clientTokenRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// clientRefRe bounds the per-item ClientRef label. Letters, digits,
// hyphen, underscore, dot. Used as a map key for intra-batch edge
// resolution (Layer 4) and echoed in responses; restricting the
// charset keeps it safe against meta-key injection attempts.
var clientRefRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// reservedMetaPrefix and reservedMetaInfix reserve the `_gramaton.`
// namespace inside meta maps. CaptureBatch stamps
// `meta._gramaton.import.job_id` for orphan recovery; rejecting these
// at validation time prevents callers from shadowing the stamp.
const (
	reservedMetaPrefix = "_gramaton."
	reservedMetaInfix  = "._gramaton."
)

// validateClientToken enforces UUID shape on a capture_batch
// client_token. Idempotency hinges on this being a stable,
// collision-resistant identifier per request.
func validateClientToken(token string) error {
	if !clientTokenRe.MatchString(token) {
		return fmt.Errorf("client_token must be a UUID")
	}
	return nil
}

// validateClientRef enforces the per-item ClientRef shape (length cap
// + restricted charset). Empty is acceptable to callers (the ref is
// optional); empty-input is a no-op.
func validateClientRef(ref string) error {
	if ref == "" {
		return nil
	}
	if len(ref) > MaxClientRefLen {
		return fmt.Errorf("client_ref exceeds %d characters", MaxClientRefLen)
	}
	if !clientRefRe.MatchString(ref) {
		return fmt.Errorf("client_ref may only contain letters, digits, dot, hyphen, and underscore")
	}
	return nil
}

// MaxClientSessionIDLen caps how long a client_session_id can be. Keeps
// pathological callers from blowing up hook-state file paths.
const MaxClientSessionIDLen = 256

// validateClientSessionID enforces the character set + length cap used
// anywhere the ID flows into a filesystem path (hook-state flags, etc.).
func validateClientSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("client_session_id is required")
	}
	if len(id) > MaxClientSessionIDLen {
		return fmt.Errorf("client_session_id exceeds %d characters", MaxClientSessionIDLen)
	}
	if !clientSessionIDRe.MatchString(id) {
		return fmt.Errorf("client_session_id may only contain letters, digits, hyphen, and underscore")
	}
	return nil
}

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
	// MaxCollectionBatchSize caps how many items a single bulk add
	// can commit. Bigger batches would tie up the engine write lock
	// for an extended period and potentially exceed provider
	// batch-embed limits. 500 is a starting point -- raise it only
	// after profiling.
	MaxCollectionBatchSize = 500
	// MaxSyncBatchSize caps the synchronous capture_batch path. Beyond
	// this, the wall-clock and lock-hold cost outweigh the latency
	// savings vs a series of single captures; the async path (Layer 5)
	// raises the cap to MaxAsyncBatchSize.
	MaxSyncBatchSize = 500
	// MaxBatchBytes is the per-request total content-byte ceiling for
	// the capture_batch path. Defends against a 1000-item × 100MB-each
	// memory blowup; Phase 0 validation rejects oversize before any
	// allocation.
	MaxBatchBytes = 256 * 1024 * 1024
	// MaxClientRefLen caps the per-item ClientRef label.
	MaxClientRefLen = 128
	// MaxBatchEdgeMultiplier caps how many edges a batch may carry
	// relative to its item count. The cap is len(Items) *
	// MaxBatchEdgeMultiplier; rejecting a 10000-edge request against
	// a 5-item batch keeps Phase 0 from chewing up unbounded
	// validation work for an obviously-malformed request.
	MaxBatchEdgeMultiplier = 10
	// MaxAsyncBatchSize caps the async capture_batch path. Larger
	// than MaxSyncBatchSize because the async runner can split work
	// across chunks and commit per-chunk progress. Operator override
	// via cfg.Jobs.MaxAsyncBatchSize.
	MaxAsyncBatchSize = 1000
	// MaxJobsListLimit caps gramaton_jobs_list pagination. Above
	// this an operator should narrow filters (status, kind, time
	// range) rather than scroll a single huge response.
	MaxJobsListLimit = 200
	// DefaultJobsListLimit is the limit applied when the request
	// omits one.
	DefaultJobsListLimit = 50
	// MaxJobsListOffset caps the pagination offset. Once a caller
	// is scrolling past 100k jobs, narrowing by status / kind / time
	// is the right tool; allowing arbitrary offset turns a single
	// request into an unbounded bbolt scan.
	MaxJobsListOffset = 100_000
	// MaxKindLen caps the Job kind filter string. The kind set is
	// closed (capture_batch and future ops); 64 is generous.
	MaxKindLen = 64
	// MaxRFC3339Len caps the wire length of an RFC3339 timestamp
	// argument. Real RFC3339 maxes around 30 characters; 64 leaves
	// headroom for nanoseconds + offset and rejects pathological
	// 10MB inputs.
	MaxRFC3339Len = 64
	// MaxResultTimeoutMS caps CaptureBatchResult's blocking timeout.
	// Holding a connection for longer than this is a footgun; the
	// caller should poll Status instead.
	MaxResultTimeoutMS = 30 * 60 * 1000 // 30 minutes
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
	// MaxLogActionsFilter bounds the size of the Actions filter
	// array on gramaton_log. A caller passing thousands of Kinds
	// would otherwise inflate the in-memory set and force a scan
	// of every commit's Actions slice against all of them. 64 is
	// generous -- the Kind set today is under 20.
	MaxLogActionsFilter = 64
)

// Collection item listing limits. Bounds on the new projection and
// filter knobs keep CollectionItems from being a DoS amplifier --
// 10k field names or a filter map with 1000 keys * 1000 values each
// would otherwise be valid inputs.
const (
	MaxProjectionFields   = 64
	MaxFilterKeys         = 20
	MaxFilterValuesPerKey = 100
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

// validateAsOf parses an optional as_of date string. Empty input
// returns the zero time. Future dates are rejected -- by definition
// there's no committed state after now to snapshot. Accepts a clock
// function so tests can inject a deterministic "now" when exercising
// the near-future rejection path.
func validateAsOf(asOf string, now func() time.Time) (time.Time, error) {
	if asOf == "" {
		return time.Time{}, nil
	}
	t, err := parseDateArg(asOf)
	if err != nil {
		return time.Time{}, fmt.Errorf("as_of: %s", err)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if t.After(now()) {
		return time.Time{}, fmt.Errorf("as_of %s is in the future", t.Format(time.RFC3339))
	}
	return t, nil
}

// validateSinceUntil parses optional Since/Until date strings and
// rejects since > until. Empty inputs are fine -- the returned time
// is the zero value in that case, and callers check .IsZero() to
// know whether the bound is set. Errors are shaped for wrapping in
// `ErrInvalid(err.Error())`.
func validateSinceUntil(since, until string) (time.Time, time.Time, error) {
	var sinceT, untilT time.Time
	var err error
	if since != "" {
		sinceT, err = parseDateArg(since)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("since: %s", err)
		}
	}
	if until != "" {
		untilT, err = parseDateArg(until)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("until: %s", err)
		}
	}
	if !sinceT.IsZero() && !untilT.IsZero() && sinceT.After(untilT) {
		return time.Time{}, time.Time{}, fmt.Errorf("since (%s) must not be after until (%s)",
			sinceT.Format(time.RFC3339), untilT.Format(time.RFC3339))
	}
	return sinceT, untilT, nil
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
