package api

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// ExploreRequest traverses the graph from a starting node.
type ExploreRequest struct {
	NodeID    string   `json:"node_id" jsonschema:"starting record ID"`
	Depth     int      `json:"depth,omitempty" jsonschema:"max traversal depth (default 2, max 10)"`
	EdgeTypes []string `json:"edge_types,omitempty" jsonschema:"restrict to these edge types (empty = all)"`
	MinWeight float64  `json:"min_weight,omitempty" jsonschema:"drop edges below this weight (default 0.0)"`
	MaxNodes  int      `json:"max_nodes,omitempty" jsonschema:"cap result node count (default 100, max 10000)"`
}

// ExploreResponse carries the traversal result subgraph.
type ExploreResponse struct {
	Nodes     []graph.SubgraphNode `json:"nodes"`
	Edges     []graph.SubgraphEdge `json:"edges"`
	Truncated bool                 `json:"truncated,omitempty"`
	MaxNodes  int                  `json:"max_nodes,omitempty"`
}

// ExploreDescription is the MCP tool description for gramaton_explore.
const ExploreDescription = "Traverse the graph from a starting record. Returns connected nodes and edges within the given depth."

// Explore runs a bounded BFS from NodeID. MaxNodes caps the returned
// subgraph; the response reports Truncated=true when the cap fires so
// callers know the result is incomplete.
func (a *API) Explore(ctx context.Context, req ExploreRequest) (ExploreResponse, *APIError) {
	if req.NodeID == "" {
		return ExploreResponse{}, ErrMissing("node_id is required")
	}
	if req.Depth <= 0 {
		req.Depth = 2
	}
	if req.Depth > MaxExploreDepth {
		req.Depth = MaxExploreDepth
	}
	if len(req.EdgeTypes) > MaxEdgeTypes {
		return ExploreResponse{}, ErrInvalid(fmt.Sprintf("maximum %d edge types allowed", MaxEdgeTypes))
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	if _, ok := a.engine.Graph().GetNode(req.NodeID); !ok {
		return ExploreResponse{}, ErrNotFound("record not found")
	}

	opts := graph.TraverseOptions{
		MaxDepth:      req.Depth,
		EdgeTypes:     req.EdgeTypes,
		MinEdgeWeight: req.MinWeight,
	}
	sub := a.engine.Graph().Traverse(req.NodeID, opts)

	ids := make([]string, 0, len(sub.Nodes)+1)
	ids = append(ids, req.NodeID)
	for _, n := range sub.Nodes {
		ids = append(ids, n.ID)
	}

	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 100
	}
	if maxNodes > MaxExploreNodes {
		maxNodes = MaxExploreNodes
	}
	truncated := false
	if len(sub.Nodes) > maxNodes {
		sub.Nodes = sub.Nodes[:maxNodes]
		truncated = true
		// Truncating nodes can leave edges pointing at IDs that were
		// dropped. Filter edges to only those whose endpoints survived
		// so the caller doesn't get dangling references. The origin
		// node is always retained (included implicitly in the subgraph
		// since Traverse starts from it).
		kept := make(map[string]struct{}, len(sub.Nodes)+1)
		kept[req.NodeID] = struct{}{}
		for _, n := range sub.Nodes {
			kept[n.ID] = struct{}{}
		}
		filtered := sub.Edges[:0]
		for _, e := range sub.Edges {
			if _, okSrc := kept[e.Source]; !okSrc {
				continue
			}
			if _, okTgt := kept[e.Target]; !okTgt {
				continue
			}
			filtered = append(filtered, e)
		}
		sub.Edges = filtered
	}

	resp := ExploreResponse{Nodes: sub.Nodes, Edges: sub.Edges}
	if truncated {
		resp.Truncated = true
		resp.MaxNodes = maxNodes
	}
	return resp, nil
}
