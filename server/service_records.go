package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
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

// serviceCapture creates a new knowledge record. Handles pre-embedding,
// deduplication, supersession, and chunking.
func (s *Server) serviceCapture(ctx context.Context, req *captureRequest) (map[string]any, *serviceError) {
	captureStart := time.Now()

	if req.Content == "" {
		return nil, errMissing("content is required")
	}
	if len(req.Content) > s.engine.Config().Limits.MaxContentLength {
		return nil, errInvalid("content exceeds maximum length")
	}
	if err := validateCaptureRequest(req); err != nil {
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

	s.engine.Lock()
	defer s.engine.Unlock()

	props := graph.Properties{
		"content_full": graph.StringProperty(req.Content),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
		"access_count": graph.Int64Property(0),
	}

	hasClassification := req.Temporality != "" || req.Confidence != nil
	if hasClassification {
		props["processing_status"] = graph.StringProperty("processed")
	} else {
		props["processing_status"] = graph.StringProperty("captured")
	}

	setOptionalProps(props, req)

	n := s.engine.Graph().AddNode(props)

	// Index content for BM25. Append meta values so keyword search
	// matches structured metadata fields.
	bm25Text := req.Content
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
	}

	var superseded []map[string]any
	if dupID, sim := s.engine.CheckDedup(n.ID); dupID != "" {
		cfg := s.engine.Config()
		if cfg.Dedup.Action == "reject" {
			msg := fmt.Sprintf("potential duplicate of %s (similarity %.3f)", dupID, sim)
			s.engine.PropIdx().RemoveNode(n.ID, n.Properties)
			s.engine.VecIdx().Remove(n.ID)
			s.engine.Graph().DeleteNode(n.ID)
			return nil, errConflict(msg)
		}

		// Default "supersede" semantics (see design-decisions.md D37):
		// mark the older record historical and link via a supersedes edge.
		now := time.Now().UTC()
		oldNode, _ := s.engine.Graph().GetNode(dupID)
		if oldNode != nil {
			_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
			if !alreadyHistorical {
				s.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
				s.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
				s.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
				if e, err := s.engine.Graph().AddEdge(n.ID, dupID, "supersedes", sim, nil); err == nil {
					summary := ""
					if v, ok := oldNode.Properties.GetString("content_short"); ok {
						summary = v
					}
					superseded = append(superseded, map[string]any{
						"id":         dupID,
						"summary":    summary,
						"similarity": sim,
						"edge_id":    e.ID,
					})
				}
			}
		}
	}

	if _, err := s.engine.Save("capture"); err != nil {
		return nil, errInternal("failed to save")
	}

	s.log.Info("capture complete",
		"component", "capture",
		"node", n.ID,
		"content_len", len(req.Content),
		"embed_ms", embedDur.Milliseconds(),
		"total_ms", time.Since(captureStart).Milliseconds(),
		"superseded", len(superseded) > 0)

	resp := map[string]any{
		"id":       n.ID,
		"warnings": warnings,
	}
	if len(superseded) > 0 {
		resp["superseded"] = superseded
	}
	return resp, nil
}
