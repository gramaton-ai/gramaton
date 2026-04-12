package index

import (
	"math"
	"sort"
)

// VectorIndex supports nearest-neighbor search over float32 vectors.
// Implementations: FlatIndex (exact, brute-force), MmapFlatIndex
// (disk-backed, quantized), and HNSW (approximate, added when scale
// demands it). All behind this interface.
type VectorIndex interface {
	// Add inserts a vector for a node ID. If the node already exists
	// in the index, its vector is replaced.
	Add(nodeID string, vec []float32)

	// Remove deletes a node's vector from the index.
	Remove(nodeID string)

	// Search returns the top-k nearest neighbors to the query vector,
	// ordered by descending similarity. Only searches among the given
	// candidate set if non-nil; otherwise searches the entire index.
	Search(query []float32, k int, candidates map[string]struct{}) []SearchResult

	// Len returns the number of vectors in the index.
	Len() int
}

// SearchResult pairs a node ID with its similarity score.
type SearchResult struct {
	NodeID     string
	Similarity float32
}

// FlatIndex is a brute-force vector index. It computes cosine similarity
// against every stored vector on each search. Exact results, no
// approximation. Suitable for small to medium candidate sets.
type FlatIndex struct {
	vectors map[string][]float32
}

// NewFlatIndex creates an empty flat vector index.
func NewFlatIndex() *FlatIndex {
	return &FlatIndex{
		vectors: make(map[string][]float32),
	}
}

func (f *FlatIndex) Add(nodeID string, vec []float32) {
	cp := make([]float32, len(vec))
	copy(cp, vec)
	f.vectors[nodeID] = cp
}

func (f *FlatIndex) Remove(nodeID string) {
	delete(f.vectors, nodeID)
}

func (f *FlatIndex) Search(query []float32, k int, candidates map[string]struct{}) []SearchResult {
	if len(f.vectors) == 0 || k <= 0 {
		return nil
	}

	var results []SearchResult

	if candidates != nil {
		// Search only within the candidate set.
		for id := range candidates {
			vec, ok := f.vectors[id]
			if !ok {
				continue
			}
			sim := CosineSimilarity(query, vec)
			results = append(results, SearchResult{NodeID: id, Similarity: sim})
		}
	} else {
		// Search the entire index.
		for id, vec := range f.vectors {
			sim := CosineSimilarity(query, vec)
			results = append(results, SearchResult{NodeID: id, Similarity: sim})
		}
	}

	// Sort by descending similarity.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if k < len(results) {
		results = results[:k]
	}
	return results
}

func (f *FlatIndex) Len() int {
	return len(f.vectors)
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
