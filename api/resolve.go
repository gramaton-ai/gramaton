package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// ResolveRequest marks a record as resolved (completed, superseded,
// abandoned, or obsolete) with an optional note.
type ResolveRequest struct {
	ID             string `json:"-" jsonschema:"-"`
	Resolution     string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
	ResolutionNote string `json:"resolution_note,omitempty" jsonschema:"free-form note about why/how"`
}

// ResolveResponse confirms the resolution. Warns if the record is in
// collections (the agent likely meant to update the collection item's
// status field instead).
type ResolveResponse struct {
	ID                string `json:"id"`
	Resolved          bool   `json:"resolved"`
	CollectionWarning string `json:"collection_warning,omitempty"`
}

// ResolveDescription is the MCP tool description for gramaton_resolve.
const ResolveDescription = "Mark a record as resolved (completed/superseded/abandoned/obsolete). Auto-sets valid_until so resolved records deprioritize in search."

// Resolve marks a record and auto-sets valid_until so search
// deprioritizes the record going forward. Repeated calls with
// different resolutions overwrite; resolution_note is set only when
// provided.
func (a *API) Resolve(ctx context.Context, req ResolveRequest) (ResolveResponse, *APIError) {
	if req.ID == "" {
		return ResolveResponse{}, ErrMissing("id is required")
	}
	if req.Resolution == "" {
		return ResolveResponse{}, ErrMissing("resolution is required")
	}
	if err := validateEnum("resolution", req.Resolution, ValidResolutions); err != nil {
		return ResolveResponse{}, ErrInvalid(err.Error())
	}
	if len(req.ResolutionNote) > MaxContextFieldLen {
		return ResolveResponse{}, ErrInvalid(fmt.Sprintf("resolution_note exceeds maximum length of %d", MaxContextFieldLen))
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, ok := a.engine.Graph().GetNode(req.ID); !ok {
		return ResolveResponse{}, ErrNotFound("record not found")
	}

	now := time.Now().UTC()
	a.engine.SetProp(req.ID, "resolution", graph.StringProperty(req.Resolution))
	a.engine.SetProp(req.ID, "resolved_at", graph.TimestampProperty(now))
	if req.ResolutionNote != "" {
		a.engine.SetProp(req.ID, "resolution_note", graph.StringProperty(req.ResolutionNote))
	}

	// Auto-set valid_until only when not already set. Prevents a
	// re-resolve from bumping the expiration forward (which would
	// bring a historical record back to "Current" in the search UI).
	n, _ := a.engine.Graph().GetNode(req.ID)
	if _, hasVU := n.Properties.GetTimestamp("valid_until"); !hasVU {
		a.engine.SetProp(req.ID, "valid_until", graph.TimestampProperty(now))
	}

	if _, err := a.engine.Save("resolve"); err != nil {
		return ResolveResponse{}, ErrInternal("failed to save")
	}

	resp := ResolveResponse{ID: req.ID, Resolved: true}
	if colls := a.nodeCollectionNames(req.ID); len(colls) > 0 {
		resp.CollectionWarning = fmt.Sprintf(
			"This record is in collection(s): %s. Consider updating the item's status field via gramaton_collection_update instead.",
			joinCollectionNames(colls))
	}
	return resp, nil
}
