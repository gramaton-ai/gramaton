package graph

// TraverseOptions controls graph traversal behavior.
type TraverseOptions struct {
	MaxDepth      int
	EdgeTypes     []string // nil means all types
	MinEdgeWeight float64
}

// SubgraphNode is a node in a traversal result.
type SubgraphNode struct {
	ID           string   `json:"id"`
	Keywords     []string `json:"keywords,omitempty"`
	SummaryShort string   `json:"summary_short,omitempty"`
}

// SubgraphEdge is an edge in a traversal result.
type SubgraphEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"`
}

// Subgraph is the result of a graph traversal.
type Subgraph struct {
	Nodes []SubgraphNode `json:"nodes"`
	Edges []SubgraphEdge `json:"edges"`
}

// Traverse performs a breadth-first traversal from the given node,
// returning all reachable nodes and edges within the given depth.
// Follows both outbound and inbound edges.
func (g *Graph) Traverse(startID string, opts TraverseOptions) Subgraph {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 2
	}

	edgeTypeSet := make(map[string]struct{})
	for _, t := range opts.EdgeTypes {
		edgeTypeSet[t] = struct{}{}
	}
	filterByType := len(edgeTypeSet) > 0

	visited := make(map[string]struct{})
	resultNodes := make([]SubgraphNode, 0)
	resultEdges := make([]SubgraphEdge, 0)
	seenEdges := make(map[string]struct{})

	// BFS queue: (nodeID, currentDepth).
	type queueItem struct {
		id    string
		depth int
	}
	queue := []queueItem{{id: startID, depth: 0}}
	visited[startID] = struct{}{}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		// Add node to results.
		n, ok := g.GetNode(item.id)
		if !ok {
			continue
		}
		sn := SubgraphNode{ID: n.ID}
		if v, ok := n.Properties.GetStringList("content_keywords"); ok {
			sn.Keywords = v
		}
		if v, ok := n.Properties.GetString("content_short"); ok {
			sn.SummaryShort = v
		}
		resultNodes = append(resultNodes, sn)

		if item.depth >= opts.MaxDepth {
			continue
		}

		// Traverse outbound edges.
		for _, e := range g.EdgesFrom(item.id) {
			if filterByType {
				if _, ok := edgeTypeSet[e.Type]; !ok {
					continue
				}
			}
			if e.Weight < opts.MinEdgeWeight {
				continue
			}
			if _, ok := seenEdges[e.ID]; !ok {
				seenEdges[e.ID] = struct{}{}
				resultEdges = append(resultEdges, SubgraphEdge{
					Source: e.SourceID,
					Target: e.TargetID,
					Type:   e.Type,
					Weight: e.Weight,
				})
			}
			if _, ok := visited[e.TargetID]; !ok {
				visited[e.TargetID] = struct{}{}
				queue = append(queue, queueItem{id: e.TargetID, depth: item.depth + 1})
			}
		}

		// Traverse inbound edges.
		for _, e := range g.EdgesTo(item.id) {
			if filterByType {
				if _, ok := edgeTypeSet[e.Type]; !ok {
					continue
				}
			}
			if e.Weight < opts.MinEdgeWeight {
				continue
			}
			if _, ok := seenEdges[e.ID]; !ok {
				seenEdges[e.ID] = struct{}{}
				resultEdges = append(resultEdges, SubgraphEdge{
					Source: e.SourceID,
					Target: e.TargetID,
					Type:   e.Type,
					Weight: e.Weight,
				})
			}
			if _, ok := visited[e.SourceID]; !ok {
				visited[e.SourceID] = struct{}{}
				queue = append(queue, queueItem{id: e.SourceID, depth: item.depth + 1})
			}
		}
	}

	return Subgraph{Nodes: resultNodes, Edges: resultEdges}
}
