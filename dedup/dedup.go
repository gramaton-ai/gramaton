// Package dedup detects near-duplicate nodes in the knowledge graph.
// Two-stage pipeline: uint8-quantized vector similarity for candidate
// retrieval, then float32 cosine similarity for threshold decision,
// then word-level Jaccard verification on the raw content to reject
// false positives from structurally similar but semantically distinct
// documents.
package dedup

import (
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// JaccardMin is the minimum word-level Jaccard similarity required to
// confirm a cosine-based duplicate match. Set conservatively low --
// true duplicates easily exceed this even with minor edits. The
// threshold exists to reject long documents that happen to share
// structural phrasing (boilerplate, headers) but carry different
// content.
const JaccardMin = 0.3

// jaccardSkipCharLimit is the content-length threshold below which
// Jaccard verification is bypassed. Short content rarely triggers the
// structural-false-positive problem that Jaccard was added to catch.
const jaccardSkipCharLimit = 200

// embeddingKeys lists the property keys to consult for a node's
// stored embedding, in priority order. Most nodes use embedding_full;
// the medium/short/keywords variants exist for chunked or curated
// records.
var embeddingKeys = []string{
	"embedding_full",
	"embedding_medium",
	"embedding_short",
	"embedding_keywords",
}

// Check reports whether nodeID is a near-duplicate of an existing
// record. Returns the existing node's ID and the cosine similarity
// when a duplicate is found, otherwise ("", 0).
//
// The caller must hold at least a read lock on the engine -- this
// function does no locking of its own. It reads the graph and the
// vector index concurrently and assumes a consistent snapshot.
func Check(g *graph.Graph, vecIdx index.VectorIndex, cfg config.DedupConfig, nodeID string) (string, float64) {
	if vecIdx.Len() < 2 {
		return "", 0
	}
	n, ok := g.GetNode(nodeID)
	if !ok {
		return "", 0
	}
	vec := nodeEmbedding(n)
	if vec == nil {
		return "", 0
	}

	// Request extra candidates: one will be self (skipped) and others
	// may fail Jaccard verification.
	results := vecIdx.Search(vec, 10, nil)
	for _, r := range results {
		if r.NodeID == nodeID {
			continue
		}

		// Recompute similarity with float32 embeddings for accurate
		// threshold comparison. The uint8 quantized similarity from the
		// vector index is too coarse for the 0.92 dedup threshold. Fall
		// back to the uint8 similarity when the candidate has no
		// float32 embedding (legacy records added directly to the
		// vector index without a property-side copy).
		sim := float64(r.Similarity)
		if candidate, ok := g.GetNode(r.NodeID); ok {
			if candVec := nodeEmbedding(candidate); candVec != nil {
				sim = float64(index.CosineSimilarity(vec, candVec))
			}
		}

		if sim >= cfg.SimilarityThreshold {
			if !verifyJaccard(g, n, r.NodeID) {
				continue
			}
			return r.NodeID, sim
		}
	}
	return "", 0
}

// verifyJaccard confirms a cosine-similarity duplicate match by
// checking word-level Jaccard similarity on actual content. Returns
// false (reject) when the texts are too dissimilar.
func verifyJaccard(g *graph.Graph, node *graph.Node, candidateID string) bool {
	candidate, ok := g.GetNode(candidateID)
	if !ok {
		return false
	}
	textA := nodeContent(node)
	textB := nodeContent(candidate)

	// Short content rarely triggers structural false positives -- the
	// cosine threshold alone is reliable.
	if len(textA) < jaccardSkipCharLimit && len(textB) < jaccardSkipCharLimit {
		return true
	}
	tokA := index.Tokenize(textA)
	tokB := index.Tokenize(textB)
	return index.JaccardSimilarity(tokA, tokB) >= JaccardMin
}

// nodeEmbedding returns the first available embedding vector on n
// from the priority-ordered embeddingKeys list.
func nodeEmbedding(n *graph.Node) []float32 {
	for _, key := range embeddingKeys {
		if v, ok := n.Properties.GetVector(key); ok {
			return v
		}
	}
	return nil
}

// nodeContent returns the best available text content for a node,
// preferring content_full over content_short.
func nodeContent(n *graph.Node) string {
	if n == nil {
		return ""
	}
	if s, ok := n.Properties.GetString("content_full"); ok {
		return s
	}
	if s, ok := n.Properties.GetString("content_short"); ok {
		return s
	}
	return ""
}
