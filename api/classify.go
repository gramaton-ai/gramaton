package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
)

// ClassifyRequest carries the metadata fields to apply when promoting
// a pending record to processed. ID is transport-set.
type ClassifyRequest struct {
	ID              string   `json:"-" jsonschema:"-"`
	Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	Confidence      *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
	KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Importance      *float64 `json:"importance,omitempty" jsonschema:"number between 0.0 and 1.0"`
	Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
	SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
}

// ClassifyResponse confirms the record was promoted.
type ClassifyResponse struct {
	ID      string `json:"id"`
	Updated bool   `json:"updated"`
}

// ClassifyDescription is the MCP tool description for gramaton_classify.
const ClassifyDescription = "Classify a pending record with metadata. Sets processing_status to processed."

// Classify moves a record out of the "captured" state by setting its
// classification fields and flipping processing_status to "processed".
// Unlike Update, Classify is idempotent -- calling it twice with the
// same args is safe.
func (a *API) Classify(ctx context.Context, req ClassifyRequest) (ClassifyResponse, *APIError) {
	if req.ID == "" {
		return ClassifyResponse{}, ErrMissing("id is required")
	}
	if err := validateClassifyRequest(&req); err != nil {
		return ClassifyResponse{}, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, ok := a.engine.Graph().GetNode(req.ID); !ok {
		return ClassifyResponse{}, ErrNotFound("record not found")
	}

	if req.Temporality != "" {
		a.engine.SetProp(req.ID, "temporality", graph.StringProperty(req.Temporality))
	}
	if req.Confidence != nil {
		a.engine.SetProp(req.ID, "confidence", graph.Float64Property(*req.Confidence))
	}
	if req.KnowledgeType != "" {
		a.engine.SetProp(req.ID, "knowledge_type", graph.StringProperty(req.KnowledgeType))
	}
	if req.EpistemicStatus != "" {
		a.engine.SetProp(req.ID, "epistemic_status", graph.StringProperty(req.EpistemicStatus))
	}
	if req.Importance != nil {
		a.engine.SetProp(req.ID, "importance", graph.Float64Property(*req.Importance))
	}
	if len(req.Keywords) > 0 {
		a.engine.SetProp(req.ID, "content_keywords", graph.StringListProperty(req.Keywords))
	}
	if req.SummaryShort != "" {
		a.engine.SetProp(req.ID, "content_short", graph.StringProperty(req.SummaryShort))
	}

	a.engine.SetProp(req.ID, "processing_status", graph.StringProperty("processed"))

	if _, err := a.engine.Save("classify", graph.CommitAction{
		Kind: "classify", RecordID: req.ID,
	}); err != nil {
		return ClassifyResponse{}, ErrInternal("failed to save")
	}

	return ClassifyResponse{ID: req.ID, Updated: true}, nil
}

func validateClassifyRequest(r *ClassifyRequest) error {
	if err := validateFloat64Range("confidence", r.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", r.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", r.Temporality, ValidTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", r.KnowledgeType, ValidKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", r.EpistemicStatus, ValidEpistemicStatuses); err != nil {
		return err
	}
	if err := validateKeywords(r.Keywords); err != nil {
		return err
	}
	origSummary := r.SummaryShort
	r.SummaryShort = sanitize.Field(r.SummaryShort)
	if err := sanitize.Validate(origSummary, r.SummaryShort, "summary_short", MaxSummaryShort()); err != nil {
		return err
	}
	return nil
}
