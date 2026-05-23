package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// loadCommitMeta reads a commit's metadata without loading the full
// graph state. Used by log/diff/history walks. Kept close to the
// other internal helpers that access the content-addressed store
// directly.
func loadCommitMeta(store interface{ Read(string) ([]byte, error) }, hash string) (*graph.Commit, error) {
	data, err := store.Read(hash)
	if err != nil {
		return nil, err
	}
	var commit graph.Commit
	if err := json.Unmarshal(data, &commit); err != nil {
		return nil, err
	}
	commit.Hash = hash
	return &commit, nil
}

// preEmbeddedVectors captures embeddings computed before acquiring the
// engine write lock. Caller runs the embed outside the lock, then
// applies the resulting vectors inside the lock. Keeps the lock hold
// short for capture operations.
type preEmbeddedVectors struct {
	vectors map[string][]float32 // embedKey -> vector
	model   string
	err     error
}

// preEmbedContent generates embeddings for the captured content before
// the engine write lock is held. Returns nil when there's nothing to
// embed (no embedder configured, empty content). Errors travel in the
// returned struct so the caller can still create the node and attach
// a warning rather than failing the whole capture.
func (a *API) preEmbedContent(ctx context.Context, r SaveRequest) *preEmbeddedVectors {
	if a.engine.Embedder() == nil {
		return nil
	}

	// content_short is the primary vector. Derive from content if the
	// caller didn't pass a summary; capped at MaxSummaryShort so the
	// embedding geometry stays consistent with later curation rewrites.
	embedText := r.SummaryShort
	if embedText == "" {
		embedText = r.Content
		summaryCap := MaxSummaryShort()
		if len(embedText) > summaryCap {
			embedText = embedText[:summaryCap]
		}
	}
	if embedText == "" {
		return nil
	}

	vecs, err := a.engine.Embedder().Embed(ctx, []string{embedText})
	if err != nil {
		return &preEmbeddedVectors{err: err}
	}

	return &preEmbeddedVectors{
		vectors: map[string][]float32{"embedding_full": vecs[0]},
		model:   a.engine.Embedder().ModelID(),
	}
}

// applyPreEmbedded stores pre-computed vectors on a node. Caller must
// hold the engine write lock.
func (a *API) applyPreEmbedded(nodeID string, pre *preEmbeddedVectors) error {
	if pre == nil {
		return nil
	}
	if pre.err != nil {
		return pre.err
	}

	for key, vec := range pre.vectors {
		prop := graph.VectorProperty(vec)
		a.engine.Graph().SetNodeProperty(nodeID, key, prop)
		a.engine.PropIdx().Add(nodeID, key, prop)
	}

	// Pick the best vector for the search index (currently only
	// embedding_full, but the preference list is kept for when more
	// vectors come back).
	for _, candidate := range []string{
		"embedding_full", "embedding_abstract",
		"embedding_short", "embedding_keywords",
	} {
		if vec, ok := pre.vectors[candidate]; ok {
			a.engine.VecIdx().Add(nodeID, vec)
			break
		}
	}

	modelProp := graph.StringProperty(pre.model)
	a.engine.Graph().SetNodeProperty(nodeID, "embedding_model", modelProp)
	a.engine.PropIdx().Add(nodeID, "embedding_model", modelProp)

	return nil
}

// setMetaProps stores meta.* properties on a node from a meta map.
// Values are converted to typed graph properties. Caller must hold
// the engine write lock.
func (a *API) setMetaProps(nodeID string, meta map[string]any) {
	for k, v := range meta {
		propKey := "meta." + k
		switch val := v.(type) {
		case string:
			a.engine.SetProp(nodeID, propKey, graph.StringProperty(val))
		case float64:
			a.engine.SetProp(nodeID, propKey, graph.Float64Property(val))
		case bool:
			a.engine.SetProp(nodeID, propKey, graph.BoolProperty(val))
		case []any:
			ss := make([]string, len(val))
			for i, elem := range val {
				// validateMeta enforces string elements; a checked assertion
			// keeps a future validator drift from turning an internal
			// type error into a panic inside the engine write lock.
			if s, ok := elem.(string); ok {
				ss[i] = s
			}
			}
			a.engine.SetProp(nodeID, propKey, graph.StringListProperty(ss))
		}
	}
}

// metaBM25Text builds a "key:value key:value" string from meta values
// so BM25 keyword search matches structured metadata fields.
func metaBM25Text(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	var parts []string
	for k, v := range meta {
		switch val := v.(type) {
		case string:
			parts = append(parts, k+":"+val)
		case float64:
			parts = append(parts, fmt.Sprintf("%s:%g", k, val))
		case bool:
			if val {
				parts = append(parts, k+":true")
			} else {
				parts = append(parts, k+":false")
			}
		case []any:
			for _, elem := range val {
				if s, ok := elem.(string); ok {
					parts = append(parts, k+":"+s)
				}
			}
		}
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " "
		}
		result += p
	}
	return result
}

// setOptionalProps copies non-empty fields from a SaveRequest onto
// a graph.Properties map. Used at capture time to populate a new node.
func setOptionalProps(props graph.Properties, r SaveRequest) {
	if r.Temporality != "" {
		props["temporality"] = graph.StringProperty(r.Temporality)
	}
	if r.Confidence != nil {
		props["confidence"] = graph.Float64Property(*r.Confidence)
	}
	if r.KnowledgeType != "" {
		props["knowledge_type"] = graph.StringProperty(r.KnowledgeType)
	}
	if r.EpistemicStatus != "" {
		props["epistemic_status"] = graph.StringProperty(r.EpistemicStatus)
	}
	if r.Importance != nil {
		props["importance"] = graph.Float64Property(*r.Importance)
	}
	if len(r.Keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(r.Keywords)
	}
	if r.SummaryShort != "" {
		props["content_short"] = graph.StringProperty(r.SummaryShort)
	}
	if r.SourceRef != "" {
		props["source_ref"] = graph.StringProperty(r.SourceRef)
	}
	if r.SourceCredibility != nil {
		props["source_credibility"] = graph.Float64Property(*r.SourceCredibility)
	}
	if r.TestimonyHops != nil {
		props["testimony_hops"] = graph.Int64Property(*r.TestimonyHops)
	}
	if r.ContextAbout != "" {
		props["context_about"] = graph.StringProperty(r.ContextAbout)
	}
	if r.ContextWho != "" {
		props["context_who"] = graph.StringProperty(r.ContextWho)
	}
	if r.ContextPrompted != "" {
		props["context_prompted"] = graph.StringProperty(r.ContextPrompted)
	}
	if r.ContextFindable != "" {
		props["context_findable_by"] = graph.StringProperty(r.ContextFindable)
	}
	if r.ContextRelated != "" {
		props["context_related"] = graph.StringProperty(r.ContextRelated)
	}
	if r.ContextSourceType != "" {
		props["context_source_type"] = graph.StringProperty(r.ContextSourceType)
	}
	if r.ContextTimeSensitivity != "" {
		props["context_time_sensitivity"] = graph.StringProperty(r.ContextTimeSensitivity)
	}
	if r.ContextReliability != "" {
		props["context_reliability"] = graph.StringProperty(r.ContextReliability)
	}
	if r.ContextCaptureReason != "" {
		props["context_capture_reason"] = graph.StringProperty(r.ContextCaptureReason)
	}
	// Caller must have passed validateSaveRequest, which parses all
	// three date fields up-front, so time.Parse failures here would
	// indicate a validator/apply drift. We still check err to avoid
	// writing a zero-value timestamp if that ever regresses.
	if r.ValidFrom != "" {
		if t, err := time.Parse(time.RFC3339, r.ValidFrom); err == nil {
			props["valid_from"] = graph.TimestampProperty(t)
		}
	}
	if r.ValidUntil != "" {
		if t, err := time.Parse(time.RFC3339, r.ValidUntil); err == nil {
			props["valid_until"] = graph.TimestampProperty(t)
		}
	}
	if r.AssertedAsOf != "" {
		if t, err := time.Parse(time.RFC3339, r.AssertedAsOf); err == nil {
			props["asserted_as_of"] = graph.TimestampProperty(t)
		}
	}
}

// parseDateArg accepts either RFC3339 or YYYY-MM-DD and returns UTC time.
func parseDateArg(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid date (expected RFC3339 or YYYY-MM-DD)")
}



// inspectMetadataSummary generates a human-readable metadata summary
// for an Inspect response. Moved from server/handler_records.go; same
// format as before.
func inspectMetadataSummary(props graph.Properties) string {
	now := time.Now().UTC()
	var parts []string

	if vu, ok := props.GetTimestamp("valid_until"); ok {
		if vu.Before(now) {
			days := int(now.Sub(vu).Hours() / 24)
			switch {
			case days == 0:
				parts = append(parts, "Historical (expired today).")
			case days == 1:
				parts = append(parts, "Historical (expired yesterday).")
			default:
				parts = append(parts, fmt.Sprintf("Historical (expired %d days ago).", days))
			}
		} else {
			days := int(vu.Sub(now).Hours() / 24)
			switch {
			case days == 0:
				parts = append(parts, "Current (expires today).")
			case days == 1:
				parts = append(parts, "Current (expires tomorrow).")
			default:
				parts = append(parts, fmt.Sprintf("Current (expires in %d days).", days))
			}
		}
	} else {
		parts = append(parts, "Current.")
	}

	if v, ok := props.GetString("temporality"); ok {
		parts = append(parts, v)
	}
	if c, ok := props.GetFloat64("confidence"); ok {
		parts = append(parts, fmt.Sprintf("confidence %.2f", c))
	}
	if s, ok := props.GetString("epistemic_status"); ok {
		if s == "well_established" {
			s = "well-established"
		}
		parts = append(parts, s)
	}
	if v, ok := props.GetString("resolution"); ok {
		parts = append(parts, fmt.Sprintf("resolved: %s", v))
	}

	result := ""
	for i, p := range parts {
		switch i {
		case 0:
			result = p
		case 1:
			result += " " + p
		default:
			result += ", " + p
		}
	}
	return result
}
