package core

import "fmt"

// RepairResult holds the outcome of a store repair.
type RepairResult struct {
	DanglingEdgesRemoved int      `json:"dangling_edges_removed,omitempty"`
	OrphanChunksRemoved  int      `json:"orphan_chunks_removed,omitempty"`
	IndexesRebuilt       bool     `json:"indexes_rebuilt,omitempty"`
	StaleEmbeddings      int      `json:"stale_embeddings,omitempty"`
	Messages             []string `json:"messages,omitempty"`
}

// Repair fixes structural issues found by Validate. Caller must hold
// the write lock.
func (e *Engine) Repair() *RepairResult {
	r := &RepairResult{}
	g := e.Graph()

	allIDs := make([]string, 0, g.NodeCount())
	nodeSet := make(map[string]struct{}, g.NodeCount())
	it := g.NodeIterator()
	for it.Next() {
		id := it.Node().ID
		allIDs = append(allIDs, id)
		nodeSet[id] = struct{}{}
	}
	it.Close()

	// --- Remove dangling edges ---
	edgeSeen := make(map[string]struct{})
	var danglingEdgeIDs []string
	for _, nodeID := range allIDs {
		for _, edge := range g.EdgesFrom(nodeID) {
			if _, seen := edgeSeen[edge.ID]; seen {
				continue
			}
			edgeSeen[edge.ID] = struct{}{}
			_, srcOK := nodeSet[edge.SourceID]
			_, tgtOK := nodeSet[edge.TargetID]
			if !srcOK || !tgtOK {
				danglingEdgeIDs = append(danglingEdgeIDs, edge.ID)
			}
		}
		for _, edge := range g.EdgesTo(nodeID) {
			if _, seen := edgeSeen[edge.ID]; seen {
				continue
			}
			edgeSeen[edge.ID] = struct{}{}
			_, srcOK := nodeSet[edge.SourceID]
			_, tgtOK := nodeSet[edge.TargetID]
			if !srcOK || !tgtOK {
				danglingEdgeIDs = append(danglingEdgeIDs, edge.ID)
			}
		}
	}
	for _, edgeID := range danglingEdgeIDs {
		g.DeleteEdge(edgeID)
		r.DanglingEdgesRemoved++
	}
	if r.DanglingEdgesRemoved > 0 {
		r.Messages = append(r.Messages, fmt.Sprintf("removed %d dangling edges", r.DanglingEdgesRemoved))
	}

	// --- Remove orphaned chunk/section nodes ---
	// A chunk/section node whose parent doesn't exist is unreachable.
	for _, id := range allIDs {
		for _, edge := range g.EdgesFrom(id) {
			if edge.Type != "chunk_of" && edge.Type != "section_of" {
				continue
			}
			if _, ok := nodeSet[edge.TargetID]; !ok {
				// Parent is gone. Remove this chunk and its edge.
				n, ok := g.GetNode(id)
				if ok {
					e.PropIdx().RemoveNode(id, n.Properties)
					e.VecIdx().Remove(id)
					e.BM25Full().Remove(id)
				}
				g.DeleteNode(id)
				delete(nodeSet, id)
				r.OrphanChunksRemoved++
				break // node deleted, stop checking its edges
			}
		}
	}
	if r.OrphanChunksRemoved > 0 {
		r.Messages = append(r.Messages, fmt.Sprintf("removed %d orphaned chunk/section nodes", r.OrphanChunksRemoved))
	}

	// --- Rebuild indexes if any structural changes were made ---
	// After removing dangling edges and orphan nodes, the BM25 and
	// property indexes may be inconsistent. Rebuild from the graph.
	if r.DanglingEdgesRemoved > 0 || r.OrphanChunksRemoved > 0 {
		e.RebuildAllIndexes()
		r.IndexesRebuilt = true
		r.Messages = append(r.Messages, "rebuilt all indexes from graph")
	}

	// --- Count stale embeddings ---
	// Don't fix these here -- reembed is the right tool. Just count
	// and report so the user knows to run it.
	currentModel := ""
	if e.Embedder() != nil {
		currentModel = e.Embedder().ModelID()
	}
	if currentModel != "" {
		for _, id := range allIDs {
			n, ok := g.GetNode(id)
			if !ok {
				continue
			}
			if _, ok := n.Properties.GetString("content_full"); !ok {
				continue
			}
			model, ok := n.Properties.GetString("embedding_model")
			if !ok || model != currentModel {
				r.StaleEmbeddings++
			}
		}
	}
	if r.StaleEmbeddings > 0 {
		r.Messages = append(r.Messages, fmt.Sprintf("%d records have stale embeddings (run 'gramaton reembed' to fix)", r.StaleEmbeddings))
	}

	// Save if we made changes.
	if r.DanglingEdgesRemoved > 0 || r.OrphanChunksRemoved > 0 {
		e.Save("repair")
	}

	return r
}
