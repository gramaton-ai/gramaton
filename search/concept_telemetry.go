package search

import (
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// ConceptMatch is one concept whose embedding scored above the
// telemetry threshold for a given query. Live members are records
// the concept aggregates (inbound instance_of edges, filtered to
// non-historical nodes). Logged for sampled review of whether
// concept-based query expansion (PRF) would actually help.
type ConceptMatch struct {
	ID          string   `json:"id"`
	Keyword     string   `json:"keyword"`
	Cosine      float64  `json:"cosine"`
	LiveMembers []string `json:"live_members,omitempty"`
}

// ScanConceptMatches walks all concept nodes in the graph and returns
// those whose embedding has cosine >= threshold against queryVec, with
// each match's live member IDs attached.
//
// Caller must hold a read lock on the engine.
//
// Cost: O(num_concepts × embed_dim). At ~50-100 concepts in a typical
// store and 384-dim embeddings, ~20-40K float ops, well under 1ms.
// Plan for an HNSW concept-index if concept count grows past ~1k.
//
// Returns nil when queryVec is empty (no embedding) or threshold <= 0
// (telemetry effectively disabled).
func ScanConceptMatches(g graph.NodeReader, queryVec []float32, threshold float64) []ConceptMatch {
	if len(queryVec) == 0 || threshold <= 0 {
		return nil
	}

	var matches []ConceptMatch
	it := g.NodeIterator()
	defer it.Close()

	for it.Next() {
		n := it.Node()
		nt, _ := n.Properties.GetString("node_type")
		if nt != "concept" {
			continue
		}

		vec, ok := n.Properties["embedding_full"]
		if !ok {
			continue
		}
		conceptVec := vec.Vector()
		if len(conceptVec) == 0 || len(conceptVec) != len(queryVec) {
			// Missing embedding or model-dimension mismatch (mid-store
			// model swap). Skip rather than error -- the concept just
			// doesn't contribute to telemetry.
			continue
		}

		sim := float64(index.CosineSimilarity(queryVec, conceptVec))
		if sim < threshold {
			continue
		}

		keyword, _ := n.Properties.GetString("concept_keyword")

		// Collect live member IDs via inbound instance_of edges,
		// filtered to non-historical source nodes. Concepts retain
		// edges from members that have since been superseded; the
		// telemetry should reflect the live-cluster view.
		var live []string
		for _, edge := range g.EdgesTo(n.ID) {
			if edge.Type != "instance_of" {
				continue
			}
			src, ok := g.GetNode(edge.SourceID)
			if !ok {
				continue
			}
			if _, hist := src.Properties.GetTimestamp("valid_until"); hist {
				continue
			}
			live = append(live, edge.SourceID)
		}

		matches = append(matches, ConceptMatch{
			ID:          n.ID,
			Keyword:     keyword,
			Cosine:      sim,
			LiveMembers: live,
		})
	}

	return matches
}
