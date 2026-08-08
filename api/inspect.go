package api

import (
	"context"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// InspectRequest identifies the record to inspect and whether to
// include the full content in the response.
type InspectRequest struct {
	ID             string `json:"id" jsonschema:"record ID to inspect"`
	IncludeContent *bool  `json:"include_content,omitempty" jsonschema:"include content_full in response (default true)"`
	AsOf           string `json:"as_of,omitempty" jsonschema:"point-in-time view: a date (YYYY-MM-DD or RFC3339) or a FULL commit hash. Returns the record's frozen reality at that commit (semantics: point_in_time) -- the live record may say something else NOW. The commit must be on the current branch's ancestry."`
}

// RelatedEdge describes an edge connected to an inspected record.
type RelatedEdge struct {
	ID           string  `json:"id"` // the other node's ID
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

	// EffectiveCuration is the resolved per-record curation behaviour
	// computed from the node's member_of edges. Tells callers exactly
	// what curation work will run on this record (curation, supersession,
	// contradictions). Absent on structural/container nodes (collections,
	// sessions, topics) and concept-synthesis nodes -- those are not
	// records that flow through curation.
	EffectiveCuration *curation.EffectiveConfig `json:"effective_curation,omitempty"`

	// AsOf + Semantics are set on point-in-time reads: the response
	// is the record's frozen reality at that commit, and the shape
	// names its contract so callers never confuse it with the live
	// record.
	AsOf      string `json:"as_of,omitempty"`
	Semantics string `json:"semantics,omitempty"`
}

// InspectDescription is the MCP tool description for gramaton_inspect.
const InspectDescription = "Get full content, metadata, and related records for a specific record by ULID. PREFER OVER SEARCH when the user's prompt names a specific ID (any ULID -- 26 chars starting with 0 or 1, e.g. 01KPED88HKK...) or when you already know the target record's ID from earlier context. Inspect is one call that returns the record plus its one-hop related edges with summaries; search on the same topic returns ranked candidates and takes two calls (search then inspect) to get the same depth. Set include_content=false for lightweight mode (omits content_full)."

// Inspect returns a record with its properties, metadata summary, and
// related edges. Records access and spreads activation (D14). When
// IncludeContent is false, content_full is omitted from the properties
// map. Lazily loads the node from storage if not cached.
func (a *API) Inspect(ctx context.Context, req InspectRequest) (InspectResponse, *APIError) {
	if req.ID == "" {
		return InspectResponse{}, ErrMissing("id is required")
	}
	includeContent := req.IncludeContent == nil || *req.IncludeContent
	if req.AsOf != "" {
		return a.inspectAsOf(req, includeContent)
	}

	// The write lock exists solely for the access bump below. On a
	// read-only store the bump is skipped (access metadata is
	// knowledge-graph state a frozen store must not mutate), so the
	// read work runs under the READ lock instead and Inspect never
	// contends with the write lock.
	readOnly := a.engine.ReadOnly()
	if readOnly {
		a.engine.RLock()
		defer a.engine.RUnlock()
	} else {
		a.engine.Lock()
		defer a.engine.Unlock()
	}

	n, ok := a.engine.Graph().GetNode(req.ID)
	if !ok {
		return InspectResponse{}, ErrNotFound("record not found")
	}

	if !readOnly {
		// Record access in the sidecar; never a commit.
		a.engine.RecordAccess(req.ID, time.Now().UTC())
		n, ok = a.engine.Graph().GetNode(req.ID)
		if !ok {
			return InspectResponse{}, ErrInternal("node disappeared after access update")
		}
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
	// Conflict visibility (Tenet 12): mirror the search renderer.
	if conflicts := search.ConflictingRecordIDs(a.engine.Graph(), n.ID); len(conflicts) > 0 {
		out.MetadataSummary += ". CONFLICTS with record(s): " + strings.Join(conflicts, ", ") + " -- present both sides or resolve via update"
	}

	if isRecordNode(n) {
		ec := curation.EffectiveCurationFor(a.engine.Graph(), n.ID)
		out.EffectiveCuration = &ec
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

// isRecordNode returns true for nodes that flow through curation
// (Memory records, collection items, session segments). Returns
// false for structural/container nodes (collections, sessions,
// topics) and concept-synthesis nodes -- those don't have a
// meaningful effective_curation. Their classification is unrelated
// to the per-record knob model.
func isRecordNode(n *graph.Node) bool {
	if n == nil {
		return false
	}
	if knType, _ := n.Properties.GetString("knowledge_type"); knType != "" {
		switch knType {
		case "collection", "session", "topic":
			return false
		}
	}
	if nodeType, _ := n.Properties.GetString("node_type"); nodeType == "concept" {
		return false
	}
	return true
}
