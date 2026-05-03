package api

import (
	"encoding/json"
	"math"
	"time"
)

// canonicalizeRequest produces a deterministic byte representation of
// a CaptureBatchRequest for hashing and idempotency comparison.
//
// Stripped: Wait (transport-level), ClientToken (idempotency key),
// SourceCredibility's marshaled NaN form (NaN-rejection lives in
// validation, not here -- if a NaN slipped through, the hash matters
// less than the validation error).
//
// Normalized: every RFC3339 timestamp (ValidFrom, ValidUntil,
// AssertedAsOf) to UTC, truncated to seconds, with a trailing `Z` so
// `+00:00` and `Z` callers produce identical hashes.
//
// Preserved: slice ordering (Items, Keywords, Edges in Layer 4) --
// the caller may rely on the order, so reordering would change
// semantics.
//
// Map ordering: encoding/json sorts map[string]any keys
// alphabetically (Go 1.12+). Meta maps thus serialize identically
// regardless of insertion order.
func canonicalizeRequest(req CaptureBatchRequest) ([]byte, error) {
	c := canonicalRequest{
		Items:            make([]canonicalItem, len(req.Items)),
		SkipSupersession: req.SkipSupersession,
	}
	for i, item := range req.Items {
		c.Items[i] = canonicalizeItem(item)
	}
	if len(req.Edges) > 0 {
		c.Edges = make([]EdgeSpec, len(req.Edges))
		for i, e := range req.Edges {
			c.Edges[i] = EdgeSpec{
				SourceID:        e.SourceID,
				SourceClientRef: e.SourceClientRef,
				TargetID:        e.TargetID,
				TargetClientRef: e.TargetClientRef,
				Type:            e.Type,
				Weight:          defaultedEdgeWeight(e.Weight),
			}
		}
	}
	return json.Marshal(c)
}

// defaultedEdgeWeight normalizes EdgeSpec.Weight for canonical hashing.
// Weight=nil and Weight=&0.5 produce identical edges in production
// (the default kicks in at AddEdge time), so they must produce
// identical canonical bytes too — otherwise a retry that serializes
// the default explicitly vs implicitly would be rejected for
// ClientToken reuse with a different request body.
//
// NaN/Inf are dropped to the default rather than nil so the
// canonical bytes are always JSON-marshalable; per-edge validation
// rejects NaN/Inf separately on the per-edge failure path.
func defaultedEdgeWeight(p *float64) *float64 {
	const defaultWeight = 0.5
	if p == nil {
		w := defaultWeight
		return &w
	}
	if math.IsNaN(*p) || math.IsInf(*p, 0) {
		w := defaultWeight
		return &w
	}
	return p
}

// canonicalRequest is the wire form fed into the hash. It mirrors
// CaptureBatchRequest with Wait + ClientToken stripped and timestamps
// normalized. Edges affect semantics so they hash; their order is
// preserved (caller-meaningful).
type canonicalRequest struct {
	Items            []canonicalItem `json:"items"`
	Edges            []EdgeSpec      `json:"edges,omitempty"`
	SkipSupersession bool            `json:"skip_supersession,omitempty"`
}

// canonicalItem is the per-item canonical form.
type canonicalItem struct {
	Content                string         `json:"content"`
	Temporality            string         `json:"temporality,omitempty"`
	Confidence             *float64       `json:"confidence,omitempty"`
	KnowledgeType          string         `json:"knowledge_type,omitempty"`
	EpistemicStatus        string         `json:"epistemic_status,omitempty"`
	Importance             *float64       `json:"importance,omitempty"`
	Keywords               []string       `json:"keywords,omitempty"`
	SummaryShort           string         `json:"summary_short,omitempty"`
	SourceRef              string         `json:"source_ref,omitempty"`
	SourceCredibility      *float64       `json:"source_credibility,omitempty"`
	TestimonyHops          *int64         `json:"testimony_hops,omitempty"`
	ContextAbout           string         `json:"context_about,omitempty"`
	ContextWho             string         `json:"context_who,omitempty"`
	ContextPrompted        string         `json:"context_prompted,omitempty"`
	ContextFindable        string         `json:"context_findable_by,omitempty"`
	ContextRelated         string         `json:"context_related,omitempty"`
	ContextSourceType      string         `json:"context_source_type,omitempty"`
	ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty"`
	ContextReliability     string         `json:"context_reliability,omitempty"`
	ContextCaptureReason   string         `json:"context_capture_reason,omitempty"`
	ValidFrom              string         `json:"valid_from,omitempty"`
	ValidUntil             string         `json:"valid_until,omitempty"`
	AssertedAsOf           string         `json:"asserted_as_of,omitempty"`
	Meta                   map[string]any `json:"meta,omitempty"`
	ClientRef              string         `json:"client_ref,omitempty"`
}

func canonicalizeItem(item CaptureBatchItem) canonicalItem {
	r := item.CaptureRequest
	return canonicalItem{
		Content:                r.Content,
		Temporality:            r.Temporality,
		Confidence:             finiteOrNil(r.Confidence),
		KnowledgeType:          r.KnowledgeType,
		EpistemicStatus:        r.EpistemicStatus,
		Importance:             finiteOrNil(r.Importance),
		Keywords:               r.Keywords,
		SummaryShort:           r.SummaryShort,
		SourceRef:              r.SourceRef,
		SourceCredibility:      finiteOrNil(r.SourceCredibility),
		TestimonyHops:          r.TestimonyHops,
		ContextAbout:           r.ContextAbout,
		ContextWho:             r.ContextWho,
		ContextPrompted:        r.ContextPrompted,
		ContextFindable:        r.ContextFindable,
		ContextRelated:         r.ContextRelated,
		ContextSourceType:      r.ContextSourceType,
		ContextTimeSensitivity: r.ContextTimeSensitivity,
		ContextReliability:     r.ContextReliability,
		ContextCaptureReason:   r.ContextCaptureReason,
		ValidFrom:              normalizeRFC3339(r.ValidFrom),
		ValidUntil:             normalizeRFC3339(r.ValidUntil),
		AssertedAsOf:           normalizeRFC3339(r.AssertedAsOf),
		Meta:                   normalizeMeta(r.Meta),
		ClientRef:              item.ClientRef,
	}
}

// finiteOrNil drops NaN/Inf values from canonical bytes so json.Marshal
// doesn't fail on otherwise-canonicalizable requests. Per-item
// validation rejects NaN/Inf on the failure path; canonicalization
// runs first because it gates idempotency, so the canonical form must
// stay marshalable for any input the JSON parser accepted.
func finiteOrNil(p *float64) *float64 {
	if p == nil {
		return nil
	}
	if math.IsNaN(*p) || math.IsInf(*p, 0) {
		return nil
	}
	return p
}

// normalizeRFC3339 parses, converts to UTC, truncates to seconds, and
// re-renders. Empty input is preserved. Unparseable input is preserved
// verbatim so the upstream validator (which rejects malformed
// timestamps) still sees the original failure.
func normalizeRFC3339(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// normalizeMeta returns a copy of the meta map with []any values
// flattened to []string when every element is a string. encoding/json
// already sorts the top-level keys; this normalizer keeps array-typed
// values stable across casts ([]string vs []any-of-strings).
func normalizeMeta(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case []any:
			ss := make([]string, 0, len(val))
			allStrings := true
			for _, e := range val {
				s, ok := e.(string)
				if !ok {
					allStrings = false
					break
				}
				ss = append(ss, s)
			}
			if allStrings {
				out[k] = ss
			} else {
				out[k] = val
			}
		default:
			out[k] = v
		}
	}
	return out
}

