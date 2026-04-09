package core

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// ValidationResult holds the outcome of a store integrity check.
type ValidationResult struct {
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Stats    ValidateStats `json:"stats"`
}

// ValidateStats reports counts checked during validation.
type ValidateStats struct {
	Nodes       int `json:"nodes"`
	Edges       int `json:"edges"`
	Collections int `json:"collections"`
	Chunks      int `json:"chunks"`
	BM25Docs    int `json:"bm25_docs"`
	VecDocs     int `json:"vec_docs"`
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
	edgeCount := 0
	edgeSeen := make(map[string]struct{})
	for _, nodeID := range allIDs {
		for _, edge := range g.EdgesFrom(nodeID) {
			if _, seen := edgeSeen[edge.ID]; seen {
				continue
			}
			edgeSeen[edge.ID] = struct{}{}
			edgeCount++

			if _, ok := nodeSet[edge.SourceID]; !ok {
				r.addError("edge %s: source node %s does not exist", edge.ID, edge.SourceID)
			}
			if _, ok := nodeSet[edge.TargetID]; !ok {
				r.addError("edge %s: target node %s does not exist", edge.ID, edge.TargetID)
			}
		}
		for _, edge := range g.EdgesTo(nodeID) {
			if _, seen := edgeSeen[edge.ID]; seen {
				continue
			}
			edgeSeen[edge.ID] = struct{}{}
			edgeCount++

			if _, ok := nodeSet[edge.SourceID]; !ok {
				r.addError("edge %s: source node %s does not exist", edge.ID, edge.SourceID)
			}
			if _, ok := nodeSet[edge.TargetID]; !ok {
				r.addError("edge %s: target node %s does not exist", edge.ID, edge.TargetID)
			}
		}
	}
	r.Stats.Edges = edgeCount

	// --- Structural integrity ---
	collections := make(map[string]struct{})
	chunks := 0
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

		// Check chunk/section parents.
		for _, edge := range g.EdgesFrom(id) {
			switch edge.Type {
			case "chunk_of", "section_of":
				chunks++
				if _, ok := nodeSet[edge.TargetID]; !ok {
					r.addError("node %s: %s edge points to missing parent %s", id, edge.Type, edge.TargetID)
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

	// --- Index consistency: BM25 (three layers) ---
	bm25FullLen := e.BM25Full().Len()
	bm25MediumLen := e.BM25Medium().Len()
	bm25ShortLen := e.BM25Short().Len()
	r.Stats.BM25Docs = bm25FullLen // report the full index size as primary stat

	// Count nodes that should be in each BM25 layer.
	bm25FullExpected := 0
	bm25MediumExpected := 0
	bm25ShortExpected := 0
	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if _, ok := n.Properties.GetString("content_full"); ok {
			bm25FullExpected++
		}
		if _, ok := n.Properties.GetString("content_medium"); ok {
			bm25MediumExpected++
		}
		if _, ok := n.Properties.GetString("content_short"); ok {
			bm25ShortExpected++
		}
	}
	if bm25FullLen != bm25FullExpected {
		r.addWarning("bm25_full: indexed %d docs, expected ~%d (nodes with content_full)", bm25FullLen, bm25FullExpected)
	}
	if bm25MediumLen != bm25MediumExpected {
		r.addWarning("bm25_medium: indexed %d docs, expected ~%d (nodes with content_medium)", bm25MediumLen, bm25MediumExpected)
	}
	if bm25ShortLen != bm25ShortExpected {
		r.addWarning("bm25_short: indexed %d docs, expected ~%d (nodes with content_short)", bm25ShortLen, bm25ShortExpected)
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

// OK returns true if no errors were found.
func (r *ValidationResult) OK() bool {
	return len(r.Errors) == 0
}

func (r *ValidationResult) addError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *ValidationResult) addWarning(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// isChunkNode returns true if a node is a chunk or section child.
func isChunkNode(g graph.NodeReader, id string) bool { return g.IsStructuralChild(id) }
