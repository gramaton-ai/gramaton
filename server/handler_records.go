package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- Request types ---

type saveRequest struct {
	Content           string         `json:"content"`
	Temporality       string         `json:"temporality,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	KnowledgeType     string         `json:"knowledge_type,omitempty"`
	EpistemicStatus   string         `json:"epistemic_status,omitempty"`
	Importance        *float64       `json:"importance,omitempty"`
	Keywords          []string       `json:"keywords,omitempty"`
	SummaryShort      string         `json:"summary_short,omitempty"`
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

// preEmbeddedVectors holds vectors computed outside the lock.
type preEmbeddedVectors struct {
	vectors map[string][]float32 // embedKey -> vector
	model   string
	err     error
}

// preEmbedContent generates embeddings before acquiring the lock.
// Threads ctx so client cancellation aborts the embedder call instead
// of waiting it out.
func (s *Server) preEmbedContent(ctx context.Context, req *saveRequest) *preEmbeddedVectors {
	if s.engine.Embedder() == nil {
		return nil
	}

	type target struct {
		sourceKey string
		embedKey  string
		text      string
	}

	// Embed content_short at capture time (D5/D13: ~125 tokens, ~30ms).
	// content_full is BM25-indexed only (D12). Observations get their
	// own embeddings during curation (D18/D23).
	//
	// content_short is the primary vector for parent-record search.
	// If no summary is provided, derive one from the content using
	// the configured MaxSummaryShort cap (rather than a hardcoded
	// 500). Curation later replaces this with an LLM-generated
	// summary; the temporary embed at capture time should at least
	// match the cap that subsequent rewrites are bounded by, so
	// vector geometry is consistent across the lifecycle.
	sources := []struct {
		sourceKey string
		embedKey  string
	}{
		{"content_short", "embedding_full"},
	}

	embedText := req.SummaryShort
	if embedText == "" {
		embedText = req.Content
		cap := getMaxSummaryShort()
		if len(embedText) > cap {
			embedText = embedText[:cap]
		}
	}

	var targets []target
	texts := map[string]string{
		"content_short": embedText,
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

	vecs, err := s.engine.Embedder().Embed(ctx, embedTexts)
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

	// Store all embedding vectors as node properties.
	for key, vec := range pre.vectors {
		prop := graph.VectorProperty(vec)
		s.engine.Graph().SetNodeProperty(nodeID, key, prop)
		s.engine.PropIdx().Add(nodeID, key, prop)
	}

	// Pick the best vector for the search index with deterministic
	// preference: full > abstract > short > keywords.
	bestKey := ""
	for _, candidate := range []string{
		"embedding_full", "embedding_abstract",
		"embedding_short", "embedding_keywords",
	} {
		if _, ok := pre.vectors[candidate]; ok {
			bestKey = candidate
			break
		}
	}
	if bestKey != "" {
		s.engine.VecIdx().Add(nodeID, pre.vectors[bestKey])
	}

	modelProp := graph.StringProperty(pre.model)
	s.engine.Graph().SetNodeProperty(nodeID, "embedding_model", modelProp)
	s.engine.PropIdx().Add(nodeID, "embedding_model", modelProp)

	return nil
}

// --- Helpers ---

func validateSaveRequest(req *saveRequest) error {
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
	if len(req.SummaryShort) > getMaxSummaryShort() {
		return fmt.Errorf("summary_short exceeds maximum length of %d", getMaxSummaryShort())
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

func setOptionalProps(props graph.Properties, req *saveRequest) {
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

