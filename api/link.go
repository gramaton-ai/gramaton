package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// LinkRequest creates an edge from the source record to a target.
// SourceID is transport-set from the URL path. EdgeWeight is optional
// (default 0.5) and must be in [0.0, 1.0].
type LinkRequest struct {
	SourceID   string   `json:"-" jsonschema:"-"`
	TargetID   string   `json:"target_id" jsonschema:"destination record ID"`
	EdgeType   string   `json:"edge_type" jsonschema:"relationship name (e.g. related_to, supports, contradicts)"`
	EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0, default 0.5"`
}

// LinkResponse returns the source record's ID + the newly created
// edge ID.
type LinkResponse struct {
	ID      string `json:"id"`
	EdgeID  string `json:"edge_id"`
	Updated bool   `json:"updated"`
}

// LinkDescription is the MCP tool description for gramaton_link.
const LinkDescription = "Create a typed edge from one record to another."

// Link adds an edge between two records. Returns ErrNotFound if either
// endpoint doesn't exist; ErrInvalid for bad weight or overlong type.
func (a *API) Link(ctx context.Context, req LinkRequest) (LinkResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("link"); apiErr != nil {
		return LinkResponse{}, apiErr
	}
	if req.SourceID == "" {
		return LinkResponse{}, ErrMissing("source id is required")
	}
	if req.TargetID == "" {
		return LinkResponse{}, ErrMissing("target_id is required")
	}
	if req.EdgeType == "" {
		return LinkResponse{}, ErrMissing("edge_type is required")
	}
	if len(req.EdgeType) > MaxEdgeTypeLen {
		return LinkResponse{}, ErrInvalid(fmt.Sprintf("edge_type exceeds maximum length of %d", MaxEdgeTypeLen))
	}
	if err := validateFloat64Range("edge_weight", req.EdgeWeight, 0.0, 1.0); err != nil {
		return LinkResponse{}, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	src, ok := a.engine.Graph().GetNode(req.SourceID)
	if !ok {
		return LinkResponse{}, ErrNotFound("source record not found")
	}
	tgt, ok := a.engine.Graph().GetNode(req.TargetID)
	if !ok {
		return LinkResponse{}, ErrNotFound("target record not found")
	}
	if graph.IsConcept(src.Properties) || graph.IsConcept(tgt.Properties) {
		return LinkResponse{}, ErrInvalid("concept nodes are machine-owned derived summaries; curation manages their edges (instance_of membership) -- link the member records instead")
	}

	weight := 0.5
	if req.EdgeWeight != nil {
		weight = *req.EdgeWeight
	}

	e, err := a.engine.Graph().AddEdge(req.SourceID, req.TargetID, req.EdgeType, weight, nil)
	if err != nil {
		a.log.Warn("link edge create failed", "component", "link",
			"source_id", req.SourceID, "target_id", req.TargetID,
			"edge_type", req.EdgeType, "err", err)
		if errors.Is(err, graph.ErrNotFound) {
			return LinkResponse{}, ErrNotFound("record deleted concurrently during link")
		}
		return LinkResponse{}, ErrInternal("failed to create edge")
	}

	if _, err := a.engine.Save("link", graph.CommitAction{
		Kind: graph.ActionLink, RecordID: req.SourceID,
	}); err != nil {
		return LinkResponse{}, ErrInternal("failed to save")
	}

	return LinkResponse{ID: req.SourceID, EdgeID: e.ID, Updated: true}, nil
}

// UnlinkRequest identifies an edge to remove by its edge ID.
type UnlinkRequest struct {
	EdgeID string `json:"-" jsonschema:"-"`
}

// UnlinkResponse confirms deletion.
type UnlinkResponse struct {
	EdgeID  string `json:"edge_id"`
	Deleted bool   `json:"deleted"`
}

// UnlinkDescription is the MCP tool description for gramaton_unlink.
const UnlinkDescription = "Delete an edge by its edge_id."

// Unlink removes an edge. Returns ErrNotFound if the edge doesn't
// exist.
func (a *API) Unlink(ctx context.Context, req UnlinkRequest) (UnlinkResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("unlink"); apiErr != nil {
		return UnlinkResponse{}, apiErr
	}
	if req.EdgeID == "" {
		return UnlinkResponse{}, ErrMissing("edge_id is required")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if err := a.engine.Graph().DeleteEdge(req.EdgeID); err != nil {
		return UnlinkResponse{}, ErrNotFound("edge not found")
	}

	if _, err := a.engine.Save("unlink", graph.CommitAction{
		Kind: graph.ActionUnlink,
	}); err != nil {
		return UnlinkResponse{}, ErrInternal("failed to save")
	}

	return UnlinkResponse{EdgeID: req.EdgeID, Deleted: true}, nil
}
