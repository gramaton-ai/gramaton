package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
)

// UpdateRequest is the input to the update operation. ID is set by
// the transport from the URL path / tool args and is not part of the
// HTTP request body.
type UpdateRequest struct {
	ID              string         `json:"-" jsonschema:"-"` // transport-set
	Confidence      *float64       `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
	Temporality     string         `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	KnowledgeType   string         `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string         `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Importance      *float64       `json:"importance,omitempty" jsonschema:"0.0-1.0"`
	Keywords        []string       `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
	SummaryShort    string         `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
	ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration date (YYYY-MM-DD or RFC3339) -- marks record as historical. Use 'clear' to remove."`
	AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
	Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata (e.g. {assignee: Sarah, status: done})"`
}

// UpdateResponse carries the id that was updated, whether any field
// actually changed, and an optional warning when updating a record
// that's a collection member.
type UpdateResponse struct {
	ID                string `json:"id"`
	Updated           bool   `json:"updated"`
	CollectionWarning string `json:"collection_warning,omitempty"`
}

// UpdateDescription is the MCP tool description for gramaton_update.
const UpdateDescription = "Update metadata on a Memory record. For collection item fields, use gramaton_collection_update instead."

// Update sets metadata fields on an existing record. Empty / nil
// fields leave the existing property unchanged. Setting valid_until
// to "clear" removes the valid_until + resolution + resolved_at
// triple (undoes supersession or resolution). Returns ErrInvalid for
// any unknown enum value or out-of-range numeric. Refuses to update
// Session segments (append-only per D19).
func (a *API) Update(ctx context.Context, req UpdateRequest) (UpdateResponse, *APIError) {
	if req.ID == "" {
		return UpdateResponse{}, ErrMissing("id is required")
	}
	if err := validateUpdateRequest(req); err != nil {
		return UpdateResponse{}, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	n, ok := a.engine.Graph().GetNode(req.ID)
	if !ok {
		return UpdateResponse{}, ErrNotFound("record not found")
	}
	if kt, _ := n.Properties.GetString("knowledge_type"); kt == "segment" {
		return UpdateResponse{}, ErrInvalid("session segments are append-only; use gramaton_session_commit to update capture status")
	}

	updated := false
	if req.Confidence != nil {
		a.engine.SetProp(req.ID, "confidence", graph.Float64Property(*req.Confidence))
		updated = true
	}
	if req.Temporality != "" {
		a.engine.SetProp(req.ID, "temporality", graph.StringProperty(req.Temporality))
		updated = true
	}
	if req.KnowledgeType != "" {
		a.engine.SetProp(req.ID, "knowledge_type", graph.StringProperty(req.KnowledgeType))
		updated = true
	}
	if req.EpistemicStatus != "" {
		a.engine.SetProp(req.ID, "epistemic_status", graph.StringProperty(req.EpistemicStatus))
		updated = true
	}
	if req.Importance != nil {
		a.engine.SetProp(req.ID, "importance", graph.Float64Property(*req.Importance))
		updated = true
	}
	if len(req.Keywords) > 0 {
		a.engine.SetProp(req.ID, "content_keywords", graph.StringListProperty(req.Keywords))
		updated = true
	}
	if req.SummaryShort != "" {
		a.engine.SetProp(req.ID, "content_short", graph.StringProperty(req.SummaryShort))
		updated = true
	}
	if req.ValidUntil != "" {
		if req.ValidUntil == "clear" {
			n, _ := a.engine.Graph().GetNode(req.ID)
			for _, key := range []string{"valid_until", "resolution", "resolved_at"} {
				if old, has := n.Properties[key]; has {
					a.engine.PropIdx().Remove(req.ID, key, old)
					a.engine.Graph().RemoveNodeProperty(req.ID, key)
				}
			}
			updated = true
		} else {
			t, err := parseDateArg(req.ValidUntil)
			if err != nil {
				return UpdateResponse{}, ErrInvalid("invalid valid_until date")
			}
			a.engine.SetProp(req.ID, "valid_until", graph.TimestampProperty(t))
			updated = true
		}
	}
	if req.AssertedAsOf != "" {
		t, err := parseDateArg(req.AssertedAsOf)
		if err != nil {
			return UpdateResponse{}, ErrInvalid("invalid asserted_as_of date")
		}
		a.engine.SetProp(req.ID, "asserted_as_of", graph.TimestampProperty(t))
		updated = true
	}
	if len(req.Meta) > 0 {
		if err := validateMeta(req.Meta); err != nil {
			return UpdateResponse{}, ErrInvalid(err.Error())
		}
		a.setMetaProps(req.ID, req.Meta)
		updated = true
	}

	if updated {
		if _, err := a.engine.Save("update", graph.CommitAction{
			Kind: "update", RecordID: req.ID,
		}); err != nil {
			return UpdateResponse{}, ErrInternal("failed to save")
		}
	}

	resp := UpdateResponse{ID: req.ID, Updated: updated}
	if colls := a.nodeCollectionNames(req.ID); len(colls) > 0 {
		resp.CollectionWarning = fmt.Sprintf(
			"This record is a member of collection(s): %s. Use gramaton_collection_update to modify collection item fields.",
			joinCollectionNames(colls))
	}
	return resp, nil
}

func validateUpdateRequest(r UpdateRequest) error {
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

// used is a compile-time reference so the unused-function linter
// doesn't complain about helpers that become live as more ops migrate.
var _ = time.Now
