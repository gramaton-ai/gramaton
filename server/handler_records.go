package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brandonlattin/gramaton/graph"
)

// --- Request types ---

type captureRequest struct {
	Content           string   `json:"content"`
	Temporality       string   `json:"temporality,omitempty"`
	Confidence        *float64 `json:"confidence,omitempty"`
	KnowledgeType     string   `json:"knowledge_type,omitempty"`
	EpistemicStatus   string   `json:"epistemic_status,omitempty"`
	Importance        *float64 `json:"importance,omitempty"`
	Keywords          []string `json:"keywords,omitempty"`
	SummaryShort      string   `json:"summary_short,omitempty"`
	SummaryAbstract   string   `json:"summary_abstract,omitempty"`
	SourceRef         string   `json:"source_ref,omitempty"`
	SourceCredibility *float64 `json:"source_credibility,omitempty"`
	TestimonyHops     *int64   `json:"testimony_hops,omitempty"`
	ContextAbout      string   `json:"context_about,omitempty"`
	ContextWho        string   `json:"context_who,omitempty"`
	ContextPrompted   string   `json:"context_prompted,omitempty"`
	ContextFindable   string   `json:"context_findable_by,omitempty"`
	ContextRelated    string   `json:"context_related,omitempty"`
	ValidFrom         string   `json:"valid_from,omitempty"`
	ValidUntil        string   `json:"valid_until,omitempty"`
	AssertedAsOf      string   `json:"asserted_as_of,omitempty"`
}

type updateRequest struct {
	Confidence      *float64 `json:"confidence,omitempty"`
	Temporality     string   `json:"temporality,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Importance      *float64 `json:"importance,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
	ValidUntil      string   `json:"valid_until,omitempty"`
	AssertedAsOf    string   `json:"asserted_as_of,omitempty"`
}

type classifyRequest struct {
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Importance      *float64 `json:"importance,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
	SummaryAbstract string   `json:"summary_abstract,omitempty"`
}

type edgeRequest struct {
	TargetID   string   `json:"target_id"`
	EdgeType   string   `json:"edge_type"`
	EdgeWeight *float64 `json:"edge_weight,omitempty"`
}

// --- Handlers ---

func (s *Server) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	var req captureRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.Content == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "content is required", true)
		return
	}

	if len(req.Content) > s.engine.Config().Limits.MaxContentLength {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "content exceeds maximum length", true)
		return
	}

	if err := validateCaptureRequest(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
		return
	}

	// Pre-embed content and pre-chunk outside the lock. Embedding can
	// take seconds (Ollama model load) and would block the entire server.
	preEmbedded := s.preEmbedContent(&req)
	preChunked := s.engine.PreChunk(context.Background(), req.Content)

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

	setOptionalProps(props, &req)

	n := s.engine.Graph().AddNode(props)
	for k, v := range n.Properties {
		s.engine.PropIdx().Add(n.ID, k, v)
	}

	// Apply pre-computed embeddings under the lock (fast, no I/O).
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
			s.writeError(w, http.StatusConflict, "duplicate", msg, false)
			return
		}

		// Auto-supersede: set valid_until on the old record and
		// create a supersedes edge from new to old. The old record
		// is preserved for history but deprioritized in search.
		now := time.Now().UTC()
		oldNode, _ := s.engine.Graph().GetNode(dupID)
		if oldNode != nil {
			// Only supersede if the old record isn't already historical.
			_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
			if !alreadyHistorical {
				s.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
				if e, err := s.engine.Graph().AddEdge(n.ID, dupID, "supersedes", sim, nil); err == nil {
					summary := ""
					if v, ok := oldNode.Properties.GetString("content_short"); ok {
						summary = v
					}
					superseded = append(superseded, map[string]any{
						"id":           dupID,
						"summary":      summary,
						"similarity":   sim,
						"edge_id":      e.ID,
					})
				}
			}
		}
	}

	if numChunks := s.engine.ApplyChunks(n.ID, preChunked); numChunks > 0 {
		warnings = append(warnings, fmt.Sprintf("content chunked into %d segments", numChunks))
	}

	if _, err := s.engine.Save("capture"); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}

	resp := map[string]any{
		"id":       n.ID,
		"warnings": warnings,
	}
	if len(superseded) > 0 {
		resp["superseded"] = superseded
	}
	s.writeJSONLocked(w, http.StatusCreated, resp)
}

// preEmbeddedVectors holds vectors computed outside the lock.
type preEmbeddedVectors struct {
	vectors map[string][]float32 // embedKey -> vector
	model   string
	err     error
}

// preEmbedContent generates embeddings before acquiring the lock.
func (s *Server) preEmbedContent(req *captureRequest) *preEmbeddedVectors {
	if s.engine.Embedder() == nil {
		return nil
	}

	type target struct {
		sourceKey string
		embedKey  string
		text      string
	}

	sources := []struct {
		sourceKey string
		embedKey  string
	}{
		{"content_keywords", "embedding_keywords"},
		{"content_short", "embedding_short"},
		{"content_abstract", "embedding_abstract"},
		{"content_full", "embedding_full"},
	}

	var targets []target
	texts := map[string]string{
		"content_full": req.Content,
	}
	if req.SummaryShort != "" {
		texts["content_short"] = req.SummaryShort
	}
	if req.SummaryAbstract != "" {
		texts["content_abstract"] = req.SummaryAbstract
	}
	if len(req.Keywords) > 0 {
		texts["content_keywords"] = joinStrings(req.Keywords)
	}

	var embedTexts []string
	for _, src := range sources {
		if t, ok := texts[src.sourceKey]; ok && t != "" {
			targets = append(targets, target{src.sourceKey, src.embedKey, t})
			embedTexts = append(embedTexts, t)
		}
	}

	if len(embedTexts) == 0 {
		return nil
	}

	vecs, err := s.engine.Embedder().Embed(context.Background(), embedTexts)
	if err != nil {
		return &preEmbeddedVectors{err: err}
	}

	result := &preEmbeddedVectors{
		vectors: make(map[string][]float32, len(vecs)),
		model:   s.engine.Embedder().ModelID(),
	}
	for i, vec := range vecs {
		result.vectors[targets[i].embedKey] = vec
	}

	return result
}

// applyPreEmbedded stores pre-computed vectors on a node. Caller must
// hold the write lock.
func (s *Server) applyPreEmbedded(nodeID string, pre *preEmbeddedVectors) error {
	if pre == nil {
		return nil
	}
	if pre.err != nil {
		return pre.err
	}

	var bestVec []float32
	for key, vec := range pre.vectors {
		prop := graph.VectorProperty(vec)
		s.engine.Graph().SetNodeProperty(nodeID, key, prop)
		s.engine.PropIdx().Add(nodeID, key, prop)
		bestVec = vec // last one wins (full > abstract > short > keywords)
	}

	if bestVec != nil {
		s.engine.VecIdx().Add(nodeID, bestVec)
	}

	modelProp := graph.StringProperty(pre.model)
	s.engine.Graph().SetNodeProperty(nodeID, "embedding_model", modelProp)
	s.engine.PropIdx().Add(nodeID, "embedding_model", modelProp)

	return nil
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += " "
		}
		result += s
	}
	return result
}

func (s *Server) handleGetRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Take write lock because we record access and spread activation.
	s.engine.Lock()
	defer s.engine.Unlock()

	n, ok := s.engine.Graph().GetNode(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "record not found", false)
		return
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
			"edge_weight": e.Weight, "direction": "outbound",
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
			"edge_weight": e.Weight, "direction": "inbound",
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

	s.writeJSONLocked(w, http.StatusOK, out)
}

func (s *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req updateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if err := validateUpdateRequest(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "record not found", false)
		return
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
		t, err := parseDateArg(req.ValidUntil)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field", "invalid valid_until date", true)
			return
		}
		s.engine.SetProp(id, "valid_until", graph.TimestampProperty(t))
		updated = true
	}
	if req.AssertedAsOf != "" {
		t, err := parseDateArg(req.AssertedAsOf)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field", "invalid asserted_as_of date", true)
			return
		}
		s.engine.SetProp(id, "asserted_as_of", graph.TimestampProperty(t))
		updated = true
	}

	if updated {
		if _, err := s.engine.Save("update"); err != nil {
			s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
			return
		}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"id": id, "updated": updated})
}

func (s *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reason := r.URL.Query().Get("reason")

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "record not found", false)
		return
	}

	// Soft delete: mark as deleted, retain for history.
	s.engine.SetProp(id, "processing_status", graph.StringProperty("deleted"))
	s.engine.SetProp(id, "deleted_at", graph.TimestampProperty(time.Now().UTC()))
	if reason != "" {
		s.engine.SetProp(id, "delete_reason", graph.StringProperty(reason))
	}

	if _, err := s.engine.Save("delete"); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (s *Server) handleCreateEdge(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")

	var req edgeRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.TargetID == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "target_id is required", true)
		return
	}
	if req.EdgeType == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "edge_type is required", true)
		return
	}
	if err := validateFloat64Range("edge_weight", req.EdgeWeight, 0.0, 1.0); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(sourceID); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "source record not found", false)
		return
	}
	if _, ok := s.engine.Graph().GetNode(req.TargetID); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "target record not found", false)
		return
	}

	weight := 0.5
	if req.EdgeWeight != nil {
		weight = *req.EdgeWeight
	}

	e, err := s.engine.Graph().AddEdge(sourceID, req.TargetID, req.EdgeType, weight, nil)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "edge_error", err.Error(), false)
		return
	}

	if _, err := s.engine.Save("link"); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}

	s.writeJSONLocked(w, http.StatusCreated, map[string]any{
		"id":      sourceID,
		"edge_id": e.ID,
		"updated": true,
	})
}

func (s *Server) handleClassifyRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req classifyRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if err := validateClassifyRequest(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, ok := s.engine.Graph().GetNode(id); !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "record not found", false)
		return
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
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"id": id, "updated": true})
}

// --- Helpers ---

func validateCaptureRequest(req *captureRequest) error {
	if err := validateFloat64Range("confidence", req.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", req.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("source_credibility", req.SourceCredibility, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", req.Temporality, validTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", req.KnowledgeType, validKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", req.EpistemicStatus, validEpistemicStatuses); err != nil {
		return err
	}
	if err := validateKeywords(req.Keywords); err != nil {
		return err
	}
	if len(req.SummaryShort) > maxSummaryShortLen {
		return fmt.Errorf("summary_short exceeds maximum length of %d", maxSummaryShortLen)
	}
	if len(req.SummaryAbstract) > maxSummaryAbstractLen {
		return fmt.Errorf("summary_abstract exceeds maximum length of %d", maxSummaryAbstractLen)
	}
	if len(req.SourceRef) > maxSourceRefLen {
		return fmt.Errorf("source_ref exceeds maximum length of %d", maxSourceRefLen)
	}
	if len(req.ContextAbout) > maxContextFieldLen {
		return fmt.Errorf("context_about exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextWho) > maxContextFieldLen {
		return fmt.Errorf("context_who exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextPrompted) > maxContextFieldLen {
		return fmt.Errorf("context_prompted exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextFindable) > maxContextFieldLen {
		return fmt.Errorf("context_findable_by exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextRelated) > maxContextFieldLen {
		return fmt.Errorf("context_related exceeds maximum length of %d", maxContextFieldLen)
	}
	return nil
}

func validateUpdateRequest(req *updateRequest) error {
	if err := validateFloat64Range("confidence", req.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", req.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", req.Temporality, validTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", req.KnowledgeType, validKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", req.EpistemicStatus, validEpistemicStatuses); err != nil {
		return err
	}
	if err := validateKeywords(req.Keywords); err != nil {
		return err
	}
	if len(req.SummaryShort) > maxSummaryShortLen {
		return fmt.Errorf("summary_short exceeds maximum length of %d", maxSummaryShortLen)
	}
	return nil
}

func validateClassifyRequest(req *classifyRequest) error {
	if err := validateFloat64Range("confidence", req.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", req.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", req.Temporality, validTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", req.KnowledgeType, validKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", req.EpistemicStatus, validEpistemicStatuses); err != nil {
		return err
	}
	if err := validateKeywords(req.Keywords); err != nil {
		return err
	}
	if len(req.SummaryShort) > maxSummaryShortLen {
		return fmt.Errorf("summary_short exceeds maximum length of %d", maxSummaryShortLen)
	}
	if len(req.SummaryAbstract) > maxSummaryAbstractLen {
		return fmt.Errorf("summary_abstract exceeds maximum length of %d", maxSummaryAbstractLen)
	}
	return nil
}

func setOptionalProps(props graph.Properties, req *captureRequest) {
	if req.Temporality != "" {
		props["temporality"] = graph.StringProperty(req.Temporality)
	}
	if req.Confidence != nil {
		props["confidence"] = graph.Float64Property(*req.Confidence)
	}
	if req.KnowledgeType != "" {
		props["knowledge_type"] = graph.StringProperty(req.KnowledgeType)
	}
	if req.EpistemicStatus != "" {
		props["epistemic_status"] = graph.StringProperty(req.EpistemicStatus)
	}
	if req.Importance != nil {
		props["importance"] = graph.Float64Property(*req.Importance)
	}
	if len(req.Keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(req.Keywords)
	}
	if req.SummaryShort != "" {
		props["content_short"] = graph.StringProperty(req.SummaryShort)
	}
	if req.SummaryAbstract != "" {
		props["content_abstract"] = graph.StringProperty(req.SummaryAbstract)
	}
	if req.SourceRef != "" {
		props["source_ref"] = graph.StringProperty(req.SourceRef)
	}
	if req.SourceCredibility != nil {
		props["source_credibility"] = graph.Float64Property(*req.SourceCredibility)
	}
	if req.TestimonyHops != nil {
		props["testimony_hops"] = graph.Int64Property(*req.TestimonyHops)
	}
	if req.ContextAbout != "" {
		props["context_about"] = graph.StringProperty(req.ContextAbout)
	}
	if req.ContextWho != "" {
		props["context_who"] = graph.StringProperty(req.ContextWho)
	}
	if req.ContextPrompted != "" {
		props["context_prompted"] = graph.StringProperty(req.ContextPrompted)
	}
	if req.ContextFindable != "" {
		props["context_findable_by"] = graph.StringProperty(req.ContextFindable)
	}
	if req.ContextRelated != "" {
		props["context_related"] = graph.StringProperty(req.ContextRelated)
	}
	if req.ValidFrom != "" {
		if t, err := time.Parse(time.RFC3339, req.ValidFrom); err == nil {
			props["valid_from"] = graph.TimestampProperty(t)
		}
	}
	if req.ValidUntil != "" {
		if t, err := time.Parse(time.RFC3339, req.ValidUntil); err == nil {
			props["valid_until"] = graph.TimestampProperty(t)
		}
	}
	if req.AssertedAsOf != "" {
		if t, err := time.Parse(time.RFC3339, req.AssertedAsOf); err == nil {
			props["asserted_as_of"] = graph.TimestampProperty(t)
		}
	}
}

// inspectMetadataSummary generates a human-readable metadata summary.
func inspectMetadataSummary(props graph.Properties) string {
	now := time.Now().UTC()
	var parts []string

	if vu, ok := props.GetTimestamp("valid_until"); ok {
		if vu.Before(now) {
			days := int(now.Sub(vu).Hours() / 24)
			if days == 0 {
				parts = append(parts, "Historical (expired today).")
			} else if days == 1 {
				parts = append(parts, "Historical (expired yesterday).")
			} else {
				parts = append(parts, fmt.Sprintf("Historical (expired %d days ago).", days))
			}
		} else {
			days := int(vu.Sub(now).Hours() / 24)
			if days == 0 {
				parts = append(parts, "Current (expires today).")
			} else if days == 1 {
				parts = append(parts, "Current (expires tomorrow).")
			} else {
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

	result := ""
	for i, p := range parts {
		if i == 0 {
			result = p
		} else if i == 1 {
			result += " " + p
		} else {
			result += ", " + p
		}
	}
	return result
}
