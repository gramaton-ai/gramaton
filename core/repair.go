package core

import (
	"errors"
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// RepairResult holds the outcome of a store repair.
type RepairResult struct {
	DanglingEdgesRemoved int      `json:"dangling_edges_removed,omitempty"`
	OrphanChunksRemoved  int      `json:"orphan_chunks_removed,omitempty"`
	StaleEmbeddings      int      `json:"stale_embeddings,omitempty"`
	Messages             []string `json:"messages,omitempty"`
}

// Repair fixes structural issues found by Validate. It manages its
// own locking: scan and deletions run inside one WithWriteBatch, so
// every removal goes through the WriteSession deletion set -- the
// same complete path as any other hard delete (property index, BM25,
// vector, secondary index, collection-member caches) -- and the
// whole repair commits as a single transaction.
func (e *Engine) Repair() *RepairResult {
	r := &RepairResult{}

	err := e.WithWriteBatch("repair", func(ws *WriteSession) (bool, error) {
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

		// --- Scan for dangling edges ---
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

		// --- Scan for orphaned chunk/section nodes ---
		// A chunk/section node whose parent doesn't exist is
		// unreachable.
		var orphanIDs []string
		for _, id := range allIDs {
			for _, edge := range g.EdgesFrom(id) {
				if edge.Type != "chunk_of" && edge.Type != "section_of" {
					continue
				}
				if _, ok := nodeSet[edge.TargetID]; !ok {
					orphanIDs = append(orphanIDs, id)
					break
				}
			}
		}

		// Orphan nodes first: the cascade removes their edges --
		// including the dangling chunk_of/section_of itself. Track
		// what cascaded so the edge pass below neither double-deletes
		// nor double-counts; the in-batch edge store defers adjacency
		// updates to the flush, so a mid-batch existence check cannot
		// tell.
		cascaded := make(map[string]struct{})
		for _, id := range orphanIDs {
			var edgeIDs []string
			for _, edge := range g.EdgesFrom(id) {
				edgeIDs = append(edgeIDs, edge.ID)
			}
			for _, edge := range g.EdgesTo(id) {
				edgeIDs = append(edgeIDs, edge.ID)
			}
			if err := ws.DeleteNode(id); err != nil {
				r.Messages = append(r.Messages, fmt.Sprintf("failed to remove orphan node: %v", err))
				continue
			}
			for _, eid := range edgeIDs {
				cascaded[eid] = struct{}{}
			}
			delete(nodeSet, id)
			r.OrphanChunksRemoved++
		}
		for _, edgeID := range danglingEdgeIDs {
			if _, done := cascaded[edgeID]; done {
				continue
			}
			if err := ws.DeleteEdge(edgeID); err != nil {
				if !errors.Is(err, graph.ErrNotFound) {
					r.Messages = append(r.Messages, fmt.Sprintf("failed to remove dangling edge: %v", err))
				}
				continue
			}
			r.DanglingEdgesRemoved++
		}
		if r.DanglingEdgesRemoved > 0 {
			r.Messages = append(r.Messages, fmt.Sprintf("removed %d dangling edges", r.DanglingEdgesRemoved))
		}
		if r.OrphanChunksRemoved > 0 {
			r.Messages = append(r.Messages, fmt.Sprintf("removed %d orphaned chunk/section nodes", r.OrphanChunksRemoved))
		}

		// --- Count stale embeddings ---
		// Don't fix these here -- reembed is the right tool. Just count
		// and report so the user knows to run it. nodeSet reflects the
		// deletions above; a removed orphan must not be counted.
		currentModel := ""
		if e.Embedder() != nil {
			currentModel = e.Embedder().ModelID()
		}
		if currentModel != "" {
			for _, id := range allIDs {
				if _, live := nodeSet[id]; !live {
					continue
				}
				n, ok := g.GetNode(id)
				if !ok {
					continue
				}
				if graph.RecordIndexText(n) == "" {
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

		mutated := r.DanglingEdgesRemoved > 0 || r.OrphanChunksRemoved > 0
		if mutated {
			ws.AddAction(graph.CommitAction{Kind: graph.ActionRepair})
		}
		return mutated, nil
	})
	if err != nil {
		// The tx rolled back: nothing the closure counted actually
		// happened. Report only the failure.
		return &RepairResult{
			StaleEmbeddings: r.StaleEmbeddings,
			Messages:        []string{fmt.Sprintf("repair failed and applied nothing: %v", err)},
		}
	}

	return r
}
