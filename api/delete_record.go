package api

import (
	"context"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// DeleteRecordRequest soft-deletes a record with an optional reason.
type DeleteRecordRequest struct {
	ID     string `json:"-" jsonschema:"-"`
	Reason string `json:"reason,omitempty" jsonschema:"why the record is being deleted (stored on the record)"`
}

// DeleteRecordResponse confirms the soft-delete.
type DeleteRecordResponse struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// DeleteRecordDescription is the MCP tool description for the delete
// operation. Soft-delete semantics: sets processing_status = "deleted"
// rather than removing the node.
const DeleteRecordDescription = "Soft-delete a record. Sets processing_status=deleted and records deleted_at; the node is retained for provenance/rollback."

// DeleteRecord sets processing_status to "deleted" and timestamps the
// delete. Caller can still inspect the record; search deprioritizes
// deleted records by default.
//
// Unlike Update, Classify, and Resolve, this path deliberately accepts
// concept nodes: editing one would fight curation over a derived
// summary, but discarding a bad one is a supported move -- the next
// synthesis pass regenerates the concept from its members. Session
// segments are likewise accepted: append-only protects their content
// from rewriting, and a soft delete rewrites nothing.
func (a *API) DeleteRecord(ctx context.Context, req DeleteRecordRequest) (DeleteRecordResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("delete"); apiErr != nil {
		return DeleteRecordResponse{}, apiErr
	}
	if req.ID == "" {
		return DeleteRecordResponse{}, ErrMissing("id is required")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, ok := a.engine.Graph().GetNode(req.ID); !ok {
		return DeleteRecordResponse{}, ErrNotFound("record not found")
	}

	a.engine.SetProp(req.ID, "processing_status", graph.StringProperty("deleted"))
	a.engine.SetProp(req.ID, "deleted_at", graph.TimestampProperty(time.Now().UTC()))
	if req.Reason != "" {
		a.engine.SetProp(req.ID, "delete_reason", graph.StringProperty(req.Reason))
	}

	if _, err := a.engine.Save("delete", graph.CommitAction{
		Kind: graph.ActionDelete, RecordID: req.ID,
	}); err != nil {
		return DeleteRecordResponse{}, ErrInternal("failed to save")
	}

	return DeleteRecordResponse{ID: req.ID, Deleted: true}, nil
}
