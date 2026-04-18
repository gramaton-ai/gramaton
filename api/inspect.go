package api

import (
	"context"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// InspectRequest identifies the record to inspect and whether to
// include the full content in the response.
type InspectRequest struct {
	ID             string `json:"id" jsonschema:"record ID to inspect"`
	IncludeContent *bool  `json:"include_content,omitempty" jsonschema:"include content_full in response (default true)"`
}

// RelatedEdge describes an edge connected to an inspected record.
type RelatedEdge struct {
	ID           string  `json:"id"`             // the other node's ID
	EdgeType     string  `json:"edge_type"`
	EdgeID       string  `json:"edge_id"`
	EdgeWeight   float64 `json:"edge_weight"`
	Direction    string  `json:"direction"` // "outbound" or "inbound"
	SummaryShort string  `json:"summary_short,omitempty"`
}

// InspectResponse is the full view of a record.
type InspectResponse struct {
	ID              string         `json:"id"`
	Properties      map[string]any `json:"properties"`
	MetadataSummary string         `json:"metadata_summary"`
	Related         []RelatedEdge  `json:"related"`
}

// InspectDescription is the MCP tool description for gramaton_inspect.
const InspectDescription = "Get full content, metadata, and related records for a specific record. Set include_content=false for lightweight mode (omits content_full)."

// Inspect returns a record with its properties, metadata summary, and
// related edges. Records access and spreads activation (D14). When
// IncludeContent is false, content_full is omitted from the properties
// map. Lazily loads the node from storage if not cached.
func (a *API) Inspect(ctx context.Context, req InspectRequest) (InspectResponse, *APIError) {
	if req.ID == "" {
		return InspectResponse{}, ErrMissing("id is required")
	}
	includeContent := req.IncludeContent == nil || *req.IncludeContent

	a.engine.Lock()
	defer a.engine.Unlock()

	n, ok := a.engine.Graph().GetNode(req.ID)
	if !ok {
		return InspectResponse{}, ErrNotFound("record not found")
	}

	// Spread activation; in-memory updates only. Disk persistence is
	// deferred to the periodic access-flush goroutine.
	now := time.Now().UTC()
	cfg := a.engine.Config()
	a.engine.Graph().RecordAccess(req.ID, now, graph.ActivationConfig{
		BaseAmount:        cfg.Activation.BaseAmount,
		AttenuationFactor: cfg.Activation.AttenuationFactor,
	})
	a.engine.MarkAccessDirty()
	n, ok = a.engine.Graph().GetNode(req.ID)
	if !ok {
		return InspectResponse{}, ErrInternal("node disappeared after access update")
	}

	props := make(map[string]any, len(n.Properties))
	for k, v := range n.Properties {
		if !includeContent && k == "content_full" {
			continue
		}
		props[k] = v.FormatValue()
	}

	out := InspectResponse{
		ID:              n.ID,
		Properties:      props,
		MetadataSummary: inspectMetadataSummary(n.Properties),
	}

	related := []RelatedEdge{}
	for _, e := range a.engine.Graph().EdgesFrom(req.ID) {
		rel := RelatedEdge{
			ID: e.TargetID, EdgeType: e.Type, EdgeID: e.ID,
			EdgeWeight: e.Weight, Direction: "outbound",
		}
		if target, ok := a.engine.Graph().GetNode(e.TargetID); ok {
			if v, ok := target.Properties.GetString("content_short"); ok {
				rel.SummaryShort = v
			}
		}
		related = append(related, rel)
	}
	for _, e := range a.engine.Graph().EdgesTo(req.ID) {
		rel := RelatedEdge{
			ID: e.SourceID, EdgeType: e.Type, EdgeID: e.ID,
			EdgeWeight: e.Weight, Direction: "inbound",
		}
		if source, ok := a.engine.Graph().GetNode(e.SourceID); ok {
			if v, ok := source.Properties.GetString("content_short"); ok {
				rel.SummaryShort = v
			}
		}
		related = append(related, rel)
	}
	out.Related = related

	// Track inspected ID for observe feedback loop detection.
	a.retrieval.Track(req.ID)

	return out, nil
}
