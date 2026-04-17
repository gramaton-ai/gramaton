package search

import (
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// DuplicatePair represents two records with high embedding similarity.
type DuplicatePair struct {
	IDA          string  `json:"id_a"`
	SummaryA     string  `json:"summary_a,omitempty"`
	IDB          string  `json:"id_b"`
	SummaryB     string  `json:"summary_b,omitempty"`
	Similarity   float64 `json:"similarity"`
}

// structuralEdgeTypes are relationships where high similarity between
// two nodes is expected by design, not a duplicate. Pairs connected by
// any of these edges are excluded from duplicate detection so that
// observation-to-parent, segment-to-memory, or collection-item pairs
// never get auto-consolidated.
var structuralEdgeTypes = map[string]struct{}{
	"observation_of": {},
	"extracted_as":   {},
	"member_of":      {},
	"chunk_of":       {},
	"section_of":     {},
	"continues_from": {},
	"topic_of":       {},
	"segment_of":     {},
}

// structurallyRelated returns true if a and b are connected by any
// edge whose type denotes a structural (not semantic) relationship.
func structurallyRelated(g graph.NodeReader, a, b string) bool {
	for _, e := range g.EdgesFrom(a) {
		if e.TargetID != b {
			continue
		}
		if _, ok := structuralEdgeTypes[e.Type]; ok {
			return true
		}
	}
	for _, e := range g.EdgesFrom(b) {
		if e.TargetID != a {
			continue
		}
		if _, ok := structuralEdgeTypes[e.Type]; ok {
			return true
		}
	}
	return false
}

// FindDuplicates returns pairs of records whose embedding similarity
// exceeds the given threshold. Each pair appears once (not mirrored).
// Pairs linked by a structural edge (observation_of, extracted_as,
// member_of, chunk_of, section_of, continues_from, topic_of,
// segment_of) are excluded -- similarity between those is by design.
// Returns at most maxPairs results, ordered by descending similarity.
func FindDuplicates(g graph.NodeReader, vecIdx index.VectorIndex, threshold float64, maxPairs int) []DuplicatePair {
	if vecIdx == nil || vecIdx.Len() == 0 {
		return nil
	}
	if maxPairs <= 0 {
		maxPairs = 50
	}

	// Collect embedded node IDs.
	type embedded struct {
		id  string
		vec []float32
	}
	var nodes []embedded
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		if isLegacyChunk(g, n.ID) {
			continue
		}
		if v, ok := n.Properties["embedding_full"]; ok {
			nodes = append(nodes, embedded{id: n.ID, vec: v.Vector()})
		}
	}
	it.Close()

	if len(nodes) < 2 {
		return nil
	}

	// For each node, search for similar nodes. Track seen pairs.
	type pairKey struct{ a, b string }
	seen := make(map[pairKey]struct{})
	var pairs []DuplicatePair

	for _, node := range nodes {
		// Search for top-5 similar (enough to catch duplicates).
		results := vecIdx.Search(node.vec, 6, nil) // 6 to account for self
		for _, r := range results {
			if r.NodeID == node.id {
				continue
			}
			if float64(r.Similarity) < threshold {
				continue
			}

			// Canonical pair ordering.
			a, b := node.id, r.NodeID
			if a > b {
				a, b = b, a
			}
			key := pairKey{a, b}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			if structurallyRelated(g, a, b) {
				continue
			}

			na, _ := g.GetNode(a)
			nb, _ := g.GetNode(b)
			pair := DuplicatePair{
				IDA:        a,
				IDB:        b,
				Similarity: float64(r.Similarity),
			}
			if na != nil {
				if s, ok := na.Properties.GetString("content_short"); ok {
					pair.SummaryA = s
				}
			}
			if nb != nil {
				if s, ok := nb.Properties.GetString("content_short"); ok {
					pair.SummaryB = s
				}
			}
			pairs = append(pairs, pair)
		}
	}

	// Sort by descending similarity.
	for i := 0; i < len(pairs); i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].Similarity > pairs[i].Similarity {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}

	if maxPairs < len(pairs) {
		pairs = pairs[:maxPairs]
	}
	return pairs
}
