package core

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// ValidationResult holds the outcome of a store integrity check.
type ValidationResult struct {
	Errors   []string      `json:"errors,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
	Stats    ValidateStats `json:"stats"`
}

// ValidateStats reports counts checked during validation.
type ValidateStats struct {
	Nodes        int `json:"nodes"`
	Edges        int `json:"edges"`
	Collections  int `json:"collections"`
	Chunks       int `json:"chunks"`
	Observations int `json:"observations"`
	BM25Docs     int `json:"bm25_docs"`
	VecDocs      int `json:"vec_docs"`
}

// Validate checks the store for integrity issues. Caller must hold
// at least a read lock on the engine.
func (e *Engine) Validate() *ValidationResult {
	r := &ValidationResult{}
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

	r.Stats.Nodes = len(allIDs)

	// --- FORMAT version ---
	if err := CheckFormatVersion(e.cfg.DataDir); err != nil {
		r.addError("format: %v", err)
	}

	// --- Edge integrity ---
	// Walk every edge once via ForEachEdge instead of per-node
	// EdgesFrom + EdgesTo. The old loop was N² (each node opens
	// two adjacency lookups, each of which on BboltEdgeStore costs
	// an extra View). This is a single pass over the edges bucket.
	edgeCount := 0
	g.ForEachEdge(func(edge *graph.Edge) {
		edgeCount++
		if _, ok := nodeSet[edge.SourceID]; !ok {
			r.addError("edge %s: source node %s does not exist", edge.ID, edge.SourceID)
		}
		if _, ok := nodeSet[edge.TargetID]; !ok {
			r.addError("edge %s: target node %s does not exist", edge.ID, edge.TargetID)
		}
	})
	r.Stats.Edges = edgeCount

	// --- Structural integrity ---
	collections := make(map[string]struct{})
	chunks := 0
	observations := 0
	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			r.addError("node %s: in AllNodeIDs but GetNode returns false", id)
			continue
		}

		// Track collections.
		if kt, ok := n.Properties.GetString("knowledge_type"); ok && kt == "collection" {
			collections[id] = struct{}{}
		}

		// Check structural parents (chunk/section/observation).
		for _, edge := range g.EdgesFrom(id) {
			switch edge.Type {
			case "chunk_of", "section_of":
				chunks++
				if _, ok := nodeSet[edge.TargetID]; !ok {
					r.addError("node %s: %s edge points to missing parent %s", id, edge.Type, edge.TargetID)
				}
			case "observation_of":
				observations++
				if _, ok := nodeSet[edge.TargetID]; !ok {
					r.addError("node %s: observation_of edge points to missing parent %s", id, edge.TargetID)
				}
			case "member_of":
				if _, ok := collections[edge.TargetID]; !ok {
					// Target might be a collection we haven't seen yet.
					// Check directly.
					if tn, ok := g.GetNode(edge.TargetID); ok {
						if kt, ok := tn.Properties.GetString("knowledge_type"); !ok || kt != "collection" {
							r.addWarning("node %s: member_of edge targets non-collection %s", id, edge.TargetID)
						}
					}
				}
			}
		}
	}
	r.Stats.Collections = len(collections)
	r.Stats.Chunks = chunks
	r.Stats.Observations = observations

	// --- Embedding dimension consistency ---
	var expectedDim int
	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if vec, ok := n.Properties.GetVector("embedding_full"); ok {
			if expectedDim == 0 {
				expectedDim = len(vec)
			} else if len(vec) != expectedDim {
				r.addWarning("node %s: embedding_full dimension %d, expected %d (needs reembed)", id, len(vec), expectedDim)
			}
		}
	}

	// --- Index consistency: BM25 (single layer, D12) ---
	bm25FullLen := e.BM25Full().Len()
	r.Stats.BM25Docs = bm25FullLen

	bm25FullExpected := 0
	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if RecordIndexText(n) != "" {
			bm25FullExpected++
		}
	}
	if bm25FullLen != bm25FullExpected {
		r.addWarning("bm25_full: indexed %d docs, expected ~%d (nodes with indexable text)", bm25FullLen, bm25FullExpected)
	}

	// --- Index consistency: Vector ---
	vecLen := e.VecIdx().Len()
	r.Stats.VecDocs = vecLen

	vecExpected := 0
	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if _, ok := n.Properties.GetVector("embedding_full"); ok {
			vecExpected++
		}
	}
	if vecLen != vecExpected {
		r.addWarning("vector index: contains %d entries, expected %d (nodes with embedding_full)", vecLen, vecExpected)
	}

	return r
}

func (r *ValidationResult) addError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *ValidationResult) addWarning(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
