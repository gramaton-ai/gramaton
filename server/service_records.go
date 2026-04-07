package server

import (
	"context"
	"fmt"
	"time"

	"github.com/brandonlattin/gramaton/graph"
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

	// Pre-embed and pre-chunk outside the lock.
	preEmbedded := s.preEmbedContent(req)
	preChunked := s.engine.PreChunk(ctx, req.Content, req.SummaryShort)

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

		// Auto-supersede: mark old record as historical.
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

	if numChunks := s.engine.ApplyChunks(n.ID, preChunked, n.Properties); numChunks > 0 {
		warnings = append(warnings, fmt.Sprintf("content chunked into %d segments", numChunks))
	}

	if _, err := s.engine.Save("capture"); err != nil {
		return nil, errInternal("failed to save")
	}

	resp := map[string]any{
		"id":       n.ID,
		"warnings": warnings,
	}
	if len(superseded) > 0 {
		resp["superseded"] = superseded
	}
	return resp, nil
}

// serviceInspect retrieves a record with full properties, metadata summary,
// and related edges. Records access and spread activation. Fixes Bug 2:
// includes edge_id in related entries (MCP previously omitted it).
func (s *Server) serviceInspect(id string) (map[string]any, *serviceError) {
	s.engine.Lock()
	defer s.engine.Unlock()

	n, ok := s.engine.Graph().GetNode(id)
	if !ok {
		return nil, errNotFound("record not found")
	}

	// Record access and spread activation.
	now := time.Now().UTC()
	cfg := s.engine.Config()
	s.engine.Graph().RecordAccess(id, now, graph.ActivationConfig{
		BaseAmount:        cfg.Activation.BaseAmount,
		AttenuationFactor: cfg.Activation.AttenuationFactor,
	})
	s.engine.Save("access")
	n, _ = s.engine.Graph().GetNode(id)

	props := make(map[string]any, len(n.Properties))
	for k, v := range n.Properties {
		props[k] = v.FormatValue()
	}

	out := map[string]any{
		"id":               n.ID,
		"properties":       props,
		"metadata_summary": inspectMetadataSummary(n.Properties),
	}

	var related []map[string]any
	for _, e := range s.engine.Graph().EdgesFrom(id) {
		rel := map[string]any{
			"id": e.TargetID, "edge_type": e.Type,
			"edge_id": e.ID, "edge_weight": e.Weight,
			"direction": "outbound",
		}
		if target, ok := s.engine.Graph().GetNode(e.TargetID); ok {
			if v, ok := target.Properties.GetString("content_short"); ok {
				rel["summary_short"] = v
			}
		}
		related = append(related, rel)
	}
	for _, e := range s.engine.Graph().EdgesTo(id) {
		rel := map[string]any{
			"id": e.SourceID, "edge_type": e.Type,
			"edge_id": e.ID, "edge_weight": e.Weight,
			"direction": "inbound",
		}
		if source, ok := s.engine.Graph().GetNode(e.SourceID); ok {
			if v, ok := source.Properties.GetString("content_short"); ok {
				rel["summary_short"] = v
			}
		}
		related = append(related, rel)
	}
	if related == nil {
		related = []map[string]any{}
	}
	out["related"] = related

	// Track inspected ID for observe feedback loop detection.
	s.retrieval.Track(id)

	return out, nil
}

// serviceUpdate updates metadata properties on an existing record.
func (s *Server) serviceUpdate(id string, req *updateRequest) (map[string]any, *serviceError) {
	if err := validateUpdateRequest(req); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		return nil, errNotFound("record not found")
	}

	updated := false
	if req.Confidence != nil {
		s.engine.SetProp(id, "confidence", graph.Float64Property(*req.Confidence))
		updated = true
	}
	if req.Temporality != "" {
		s.engine.SetProp(id, "temporality", graph.StringProperty(req.Temporality))
		updated = true
	}
	if req.KnowledgeType != "" {
		s.engine.SetProp(id, "knowledge_type", graph.StringProperty(req.KnowledgeType))
		updated = true
	}
	if req.EpistemicStatus != "" {
		s.engine.SetProp(id, "epistemic_status", graph.StringProperty(req.EpistemicStatus))
		updated = true
	}
	if req.Importance != nil {
		s.engine.SetProp(id, "importance", graph.Float64Property(*req.Importance))
		updated = true
	}
	if len(req.Keywords) > 0 {
		s.engine.SetProp(id, "content_keywords", graph.StringListProperty(req.Keywords))
		updated = true
	}
	if req.SummaryShort != "" {
		s.engine.SetProp(id, "content_short", graph.StringProperty(req.SummaryShort))
		updated = true
	}
	if req.ValidUntil != "" {
		if req.ValidUntil == "clear" {
			n, _ := s.engine.Graph().GetNode(id)
			for _, key := range []string{"valid_until", "resolution", "resolved_at"} {
				if old, has := n.Properties[key]; has {
					s.engine.PropIdx().Remove(id, key, old)
					s.engine.Graph().RemoveNodeProperty(id, key)
				}
			}
			updated = true
		} else {
			t, err := parseDateArg(req.ValidUntil)
			if err != nil {
				return nil, errInvalid("invalid valid_until date")
			}
			s.engine.SetProp(id, "valid_until", graph.TimestampProperty(t))
			updated = true
		}
	}
	if req.AssertedAsOf != "" {
		t, err := parseDateArg(req.AssertedAsOf)
		if err != nil {
			return nil, errInvalid("invalid asserted_as_of date")
		}
		s.engine.SetProp(id, "asserted_as_of", graph.TimestampProperty(t))
		updated = true
	}
	if len(req.Meta) > 0 {
		if err := validateMeta(req.Meta); err != nil {
			return nil, errInvalid(err.Error())
		}
		s.setMetaProps(id, req.Meta)
		updated = true
	}

	if updated {
		if _, err := s.engine.Save("update"); err != nil {
			return nil, errInternal("failed to save")
		}
	}

	return map[string]any{"id": id, "updated": updated}, nil
}

// serviceClassify classifies a pending record with metadata and sets
// processing_status to "processed".
func (s *Server) serviceClassify(id string, req *classifyRequest) (map[string]any, *serviceError) {
	if err := validateClassifyRequest(req); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		return nil, errNotFound("record not found")
	}

	if req.Temporality != "" {
		s.engine.SetProp(id, "temporality", graph.StringProperty(req.Temporality))
	}
	if req.Confidence != nil {
		s.engine.SetProp(id, "confidence", graph.Float64Property(*req.Confidence))
	}
	if req.KnowledgeType != "" {
		s.engine.SetProp(id, "knowledge_type", graph.StringProperty(req.KnowledgeType))
	}
	if req.EpistemicStatus != "" {
		s.engine.SetProp(id, "epistemic_status", graph.StringProperty(req.EpistemicStatus))
	}
	if req.Importance != nil {
		s.engine.SetProp(id, "importance", graph.Float64Property(*req.Importance))
	}
	if len(req.Keywords) > 0 {
		s.engine.SetProp(id, "content_keywords", graph.StringListProperty(req.Keywords))
	}
	if req.SummaryShort != "" {
		s.engine.SetProp(id, "content_short", graph.StringProperty(req.SummaryShort))
	}
	if req.SummaryAbstract != "" {
		s.engine.SetProp(id, "content_abstract", graph.StringProperty(req.SummaryAbstract))
	}

	s.engine.SetProp(id, "processing_status", graph.StringProperty("processed"))

	if _, err := s.engine.Save("classify"); err != nil {
		return nil, errInternal("failed to save")
	}

	return map[string]any{"id": id, "updated": true}, nil
}

// serviceResolve marks a record as resolved and auto-sets valid_until.
func (s *Server) serviceResolve(id string, req *resolveRequest) (map[string]any, *serviceError) {
	if req.Resolution == "" {
		return nil, errMissing("resolution is required")
	}
	if err := validateEnum("resolution", req.Resolution, validResolutions); err != nil {
		return nil, errInvalid(err.Error())
	}
	if len(req.ResolutionNote) > maxContextFieldLen {
		return nil, errInvalid(fmt.Sprintf("resolution_note exceeds maximum length of %d", maxContextFieldLen))
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		return nil, errNotFound("record not found")
	}

	now := time.Now().UTC()
	s.engine.SetProp(id, "resolution", graph.StringProperty(req.Resolution))
	s.engine.SetProp(id, "resolved_at", graph.TimestampProperty(now))
	if req.ResolutionNote != "" {
		s.engine.SetProp(id, "resolution_note", graph.StringProperty(req.ResolutionNote))
	}

	// Auto-set valid_until if not already set, so resolved records
	// naturally deprioritize in search results.
	n, _ := s.engine.Graph().GetNode(id)
	if _, hasVU := n.Properties.GetTimestamp("valid_until"); !hasVU {
		s.engine.SetProp(id, "valid_until", graph.TimestampProperty(now))
	}

	if _, err := s.engine.Save("resolve"); err != nil {
		return nil, errInternal("failed to save")
	}

	return map[string]any{"id": id, "resolved": true}, nil
}

// serviceLink creates an edge between two records.
func (s *Server) serviceLink(sourceID string, req *edgeRequest) (map[string]any, *serviceError) {
	if req.TargetID == "" {
		return nil, errMissing("target_id is required")
	}
	if req.EdgeType == "" {
		return nil, errMissing("edge_type is required")
	}
	if err := validateFloat64Range("edge_weight", req.EdgeWeight, 0.0, 1.0); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(sourceID); !ok {
		return nil, errNotFound("source record not found")
	}
	if _, ok := s.engine.Graph().GetNode(req.TargetID); !ok {
		return nil, errNotFound("target record not found")
	}

	weight := 0.5
	if req.EdgeWeight != nil {
		weight = *req.EdgeWeight
	}

	e, err := s.engine.Graph().AddEdge(sourceID, req.TargetID, req.EdgeType, weight, nil)
	if err != nil {
		return nil, errInternal(err.Error())
	}

	if _, err := s.engine.Save("link"); err != nil {
		return nil, errInternal("failed to save")
	}

	return map[string]any{
		"id":      sourceID,
		"edge_id": e.ID,
		"updated": true,
	}, nil
}

// serviceDeleteEdge removes an edge from the graph.
func (s *Server) serviceDeleteEdge(edgeID string) (map[string]any, *serviceError) {
	s.engine.Lock()
	defer s.engine.Unlock()

	if err := s.engine.Graph().DeleteEdge(edgeID); err != nil {
		return nil, errNotFound("edge not found")
	}

	if _, err := s.engine.Save("unlink"); err != nil {
		return nil, errInternal("failed to save")
	}

	return map[string]any{"edge_id": edgeID, "deleted": true}, nil
}

// serviceDeleteRecord soft-deletes a record.
func (s *Server) serviceDeleteRecord(id, reason string) (map[string]any, *serviceError) {
	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		return nil, errNotFound("record not found")
	}

	s.engine.SetProp(id, "processing_status", graph.StringProperty("deleted"))
	s.engine.SetProp(id, "deleted_at", graph.TimestampProperty(time.Now().UTC()))
	if reason != "" {
		s.engine.SetProp(id, "delete_reason", graph.StringProperty(reason))
	}

	if _, err := s.engine.Save("delete"); err != nil {
		return nil, errInternal("failed to save")
	}

	return map[string]any{"id": id, "deleted": true}, nil
}
