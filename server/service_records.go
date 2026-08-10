package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/similarity"
)

// setMetaProps stores meta.* properties on a node from a meta map.
// Values are converted to typed graph properties. Caller must hold
// the write lock.
func (s *Server) setMetaProps(nodeID string, meta map[string]any) {
	for k, v := range meta {
		propKey := "meta." + k
		switch val := v.(type) {
		case string:
			s.engine.SetProp(nodeID, propKey, graph.StringProperty(val))
		case float64:
			s.engine.SetProp(nodeID, propKey, graph.Float64Property(val))
		case bool:
			s.engine.SetProp(nodeID, propKey, graph.BoolProperty(val))
		case []any:
			ss := make([]string, len(val))
			for i, elem := range val {
				ss[i] = elem.(string) // validated by validateMeta
			}
			s.engine.SetProp(nodeID, propKey, graph.StringListProperty(ss))
		}
	}
}

// metaBM25Text builds a string from meta values for BM25 indexing.
// Format: "key:value key:value ..." so keyword search matches meta fields.
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

// serviceSave creates a new knowledge record. Handles pre-embedding,
// deduplication, supersession, and chunking.
func (s *Server) serviceSave(ctx context.Context, req *saveRequest) (map[string]any, *serviceError) {
	// Store-level read-only guard, mirroring api.rejectIfReadOnly:
	// this legacy service path (intake) mutates the in-memory graph
	// before Engine.Save, so it must reject up front rather than rely
	// on the engine backstop.
	if s.engine.ReadOnly() {
		return nil, errForbidden("store is read-only: save is not permitted")
	}
	saveStart := time.Now()

	if req.Content == "" {
		return nil, errMissing("content is required")
	}
	if len(req.Content) > s.engine.Config().Limits.MaxContentLength {
		return nil, errInvalid("content exceeds maximum length")
	}
	if err := validateSaveRequest(req); err != nil {
		return nil, errInvalid(err.Error())
	}
	if err := validateMeta(req.Meta); err != nil {
		return nil, errInvalid(err.Error())
	}

	// Pre-embed outside the lock. Observation extraction (D18/D23)
	// happens asynchronously in the curation cycle, not during capture.
	embedStart := time.Now()
	preEmbedded := s.preEmbedContent(ctx, req)
	embedDur := time.Since(embedStart)

	// Save-guard scan against a read snapshot, before the write lock
	// (see api/save.go for the full contract; this legacy intake path
	// mirrors it).
	var scanVec []float32
	var scanSeq uint64
	var outcome similarity.Outcome
	if preEmbedded != nil && preEmbedded.err == nil {
		if vec, ok := preEmbedded.vectors["embedding_full"]; ok {
			scanVec = vec
			s.engine.RLock()
			scanSeq = s.engine.WriteSeq()
			outcome = s.engine.ScanSimilarVec(vec, req.Content)
			s.engine.RUnlock()
		}
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	// An acknowledged scan hold is already judged -- clear it before
	// the delta merge so a hold-grade record that committed in the
	// scan-to-lock window still surfaces instead of being shadowed by
	// the acked (and typically more similar) match. Mirrors api.Save.
	if outcome.Hold != nil && intakeAckContains(req.AllowSimilar, outcome.Hold.NodeID) {
		outcome.Hold = nil
	}

	// Delta re-scan + hold, pre-insert.
	if scanVec != nil {
		if m, found, _ := s.engine.SimilarInDelta(scanSeq, scanVec, req.Content); found {
			if outcome.Hold == nil || m.Similarity > outcome.Hold.Similarity {
				held := m
				outcome.Hold = &held
				outcome.Advisory = nil
			}
		}
	}
	if outcome.Hold != nil && !intakeAckContains(req.AllowSimilar, outcome.Hold.NodeID) {
		if heldResp := s.heldSimilarMap(outcome.Hold); heldResp != nil {
			s.log.Info("intake held: similar record", "component", "save",
				"similar_to", outcome.Hold.NodeID,
				"similarity", fmt.Sprintf("%.3f", outcome.Hold.Similarity))
			return map[string]any{"held": heldResp}, nil
		}
	}

	props := graph.Properties{
		"content_full": graph.StringProperty(req.Content),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
		"access_count": graph.Int64Property(0),
	}
	// Set-once author attribution (bare `author` key), composed from
	// the effective engine config. Stamped before AddNode so it lands
	// in the property index with the rest of the base props. An empty
	// composed identity stamps nothing: the property is absent, not
	// empty-string.
	if author := s.engine.Config().Author.String(); author != "" {
		props["author"] = graph.StringProperty(author)
	}

	hasClassification := req.Temporality != "" || req.Confidence != nil
	if hasClassification {
		props["processing_status"] = graph.StringProperty("processed")
	} else {
		props["processing_status"] = graph.StringProperty("captured")
	}

	setOptionalProps(props, req)

	n := s.engine.Graph().AddNode(props)

	// Index content for BM25, with the caller's summary, keywords, and
	// meta values appended -- the same four sources the api save path
	// and the rebuild union (RecordIndexText) use.
	bm25Text := req.Content
	if sum := graph.LexicalSummaryText(req.Content, req.SummaryShort); sum != "" {
		bm25Text += " " + sum
	}
	if len(req.Keywords) > 0 {
		bm25Text += " " + strings.Join(req.Keywords, " ")
	}
	if metaText := metaBM25Text(req.Meta); metaText != "" {
		bm25Text += " " + metaText
	}
	s.engine.IndexNode(n.ID, bm25Text, nil)

	// Store meta.* properties after node creation.
	if len(req.Meta) > 0 {
		s.setMetaProps(n.ID, req.Meta)
	}

	var warnings []string
	if err := s.applyPreEmbedded(n.ID, preEmbedded); err != nil {
		warnings = append(warnings, fmt.Sprintf("embedding failed: %s", err))
		// The save-guard scan never ran; mark the record so reembed
		// re-runs it when the deferred embedding arrives.
		s.engine.SetProp(n.ID, "similar_check_pending", graph.BoolProperty(true))
	}

	// Advisory: attach the non-blocking notice when the best
	// candidate landed in the advisory band.
	var advisory map[string]any
	if outcome.Advisory != nil && !intakeAckContains(req.AllowSimilar, outcome.Advisory.NodeID) {
		if adv, ok := s.engine.Graph().GetNode(outcome.Advisory.NodeID); ok {
			summary, _ := adv.Properties.GetString("content_short")
			advisory = map[string]any{
				"id":         adv.ID,
				"summary":    summary,
				"similarity": outcome.Advisory.Similarity,
				"note":       "Saved, but this resembles the record above. If it is a revision of that knowledge, prefer gramaton_update on it (inspect first) and consider resolving this record.",
			}
		}
	}

	if _, err := s.engine.Save("save", graph.CommitAction{
		Kind: graph.ActionSave, RecordID: n.ID,
	}); err != nil {
		return nil, errInternal("failed to save")
	}

	s.log.Info("capture complete",
		"component", "save",
		"node", n.ID,
		"content_len", len(req.Content),
		"embed_ms", embedDur.Milliseconds(),
		"total_ms", time.Since(saveStart).Milliseconds(),
		"advisory", advisory != nil)

	resp := map[string]any{
		"id":       n.ID,
		"warnings": warnings,
	}
	if advisory != nil {
		resp["advisory"] = advisory
	}
	return resp, nil
}

// intakeAckContains reports whether the caller's allow_similar
// acknowledgment list names the candidate record.
func intakeAckContains(acks []string, id string) bool {
	for _, a := range acks {
		if a == id {
			return true
		}
	}
	return false
}

// heldSimilarMap assembles the hold response material (the map-based
// twin of api.HeldSimilar) for the legacy intake path. Returns nil
// when the candidate no longer exists. Caller must hold the engine
// lock.
func (s *Server) heldSimilarMap(m *similarity.Match) map[string]any {
	n, ok := s.engine.Graph().GetNode(m.NodeID)
	if !ok {
		return nil
	}
	content, _ := n.Properties.GetString("content_full")
	summary, _ := n.Properties.GetString("content_short")
	created := ""
	if ts, ok := n.Properties.GetTimestamp("created_at"); ok {
		created = ts.UTC().Format(time.RFC3339)
	}
	historical := false
	if vu, ok := n.Properties.GetTimestamp("valid_until"); ok && vu.Before(time.Now().UTC()) {
		historical = true
	}
	resolution, _ := n.Properties.GetString("resolution")
	out := map[string]any{
		"id":           n.ID,
		"content_full": content,
		"summary":      summary,
		"similarity":   m.Similarity,
		"created_at":   created,
		"version":      api.RecordVersionToken(n),
		"note": "Save held; nothing was created. This closely matches the record above. " +
			"If this REVISES it: gramaton_update(id, content=..., expected_version=version) composed from its full content. " +
			"If genuinely distinct: re-send with allow_similar=[\"" + n.ID + "\"].",
	}
	if historical {
		out["historical"] = true
	}
	if resolution != "" {
		out["resolution"] = resolution
	}
	return out
}
