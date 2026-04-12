package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- Request types ---

type captureRequest struct {
	Content           string         `json:"content"`
	Temporality       string         `json:"temporality,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	KnowledgeType     string         `json:"knowledge_type,omitempty"`
	EpistemicStatus   string         `json:"epistemic_status,omitempty"`
	Importance        *float64       `json:"importance,omitempty"`
	Keywords          []string       `json:"keywords,omitempty"`
	SummaryShort      string         `json:"summary_short,omitempty"`
	SummaryMedium   string         `json:"summary_medium,omitempty"`
	SourceRef         string         `json:"source_ref,omitempty"`
	SourceCredibility *float64       `json:"source_credibility,omitempty"`
	TestimonyHops     *int64         `json:"testimony_hops,omitempty"`
	ContextAbout           string         `json:"context_about,omitempty"`
	ContextWho             string         `json:"context_who,omitempty"`
	ContextPrompted        string         `json:"context_prompted,omitempty"`
	ContextFindable        string         `json:"context_findable_by,omitempty"`
	ContextRelated         string         `json:"context_related,omitempty"`
	ContextSourceType      string         `json:"context_source_type,omitempty"`
	ContextTimeSensitivity string         `json:"context_time_sensitivity,omitempty"`
	ContextReliability     string         `json:"context_reliability,omitempty"`
	ContextCaptureReason   string         `json:"context_capture_reason,omitempty"`
	ValidFrom         string         `json:"valid_from,omitempty"`
	ValidUntil        string         `json:"valid_until,omitempty"`
	AssertedAsOf      string         `json:"asserted_as_of,omitempty"`
	Meta              map[string]any `json:"meta,omitempty"`
}

type updateRequest struct {
	Confidence      *float64       `json:"confidence,omitempty"`
	Temporality     string         `json:"temporality,omitempty"`
	KnowledgeType   string         `json:"knowledge_type,omitempty"`
	EpistemicStatus string         `json:"epistemic_status,omitempty"`
	Importance      *float64       `json:"importance,omitempty"`
	Keywords        []string       `json:"keywords,omitempty"`
	SummaryShort    string         `json:"summary_short,omitempty"`
	ValidUntil      string         `json:"valid_until,omitempty"`
	AssertedAsOf    string         `json:"asserted_as_of,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

type classifyRequest struct {
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Importance      *float64 `json:"importance,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
	SummaryMedium string   `json:"summary_medium,omitempty"`
}

type resolveRequest struct {
	Resolution     string `json:"resolution"`
	ResolutionNote string `json:"resolution_note,omitempty"`
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

	result, svcErr := s.serviceCapture(r.Context(), &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusCreated, result)
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
		{"content_full", "embedding_full"},
	}

	var targets []target
	texts := map[string]string{
		"content_full": req.Content,
	}
	if req.SummaryShort != "" {
		texts["content_short"] = req.SummaryShort
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
	includeContent := r.URL.Query().Get("include_content") != "false"

	result, svcErr := s.serviceInspect(id, includeContent)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req updateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceUpdate(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reason := r.URL.Query().Get("reason")

	result, svcErr := s.serviceDeleteRecord(id, reason)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateEdge(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")

	var req edgeRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceLink(sourceID, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleDeleteEdge(w http.ResponseWriter, r *http.Request) {
	edgeID := r.PathValue("edge_id")

	result, svcErr := s.serviceDeleteEdge(edgeID)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleClassifyRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req classifyRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceClassify(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResolveRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req resolveRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceResolve(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusOK, result)
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
	if len(req.ContextSourceType) > maxContextFieldLen {
		return fmt.Errorf("context_source_type exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextTimeSensitivity) > maxContextFieldLen {
		return fmt.Errorf("context_time_sensitivity exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextReliability) > maxContextFieldLen {
		return fmt.Errorf("context_reliability exceeds maximum length of %d", maxContextFieldLen)
	}
	if len(req.ContextCaptureReason) > maxContextFieldLen {
		return fmt.Errorf("context_capture_reason exceeds maximum length of %d", maxContextFieldLen)
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
	if req.ContextSourceType != "" {
		props["context_source_type"] = graph.StringProperty(req.ContextSourceType)
	}
	if req.ContextTimeSensitivity != "" {
		props["context_time_sensitivity"] = graph.StringProperty(req.ContextTimeSensitivity)
	}
	if req.ContextReliability != "" {
		props["context_reliability"] = graph.StringProperty(req.ContextReliability)
	}
	if req.ContextCaptureReason != "" {
		props["context_capture_reason"] = graph.StringProperty(req.ContextCaptureReason)
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
	if v, ok := props.GetString("resolution"); ok {
		parts = append(parts, fmt.Sprintf("resolved: %s", v))
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
