// Package similarity detects near-duplicate and similar records in
// the knowledge graph. It powers the save-time hold (a near-verbatim
// save is refused before creation so the caller can revise the
// existing record instead) and the advisory band (a successful save
// carries a notice naming the most similar existing record).
//
// Two-stage pipeline: uint8-quantized vector similarity for candidate
// retrieval, then float32 cosine similarity for threshold decision,
// then -- for hold decisions only -- word-level Jaccard verification
// on the raw content to reject false positives from structurally
// similar but semantically distinct documents. Advisory matches skip
// the Jaccard gate deliberately: genuine revisions share fewer exact
// words than duplicates, and an advisory is informational, not
// blocking.
package similarity

import (
	"strings"

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

// Match is one similar-record result from a scan.
type Match struct {
	// NodeID is the existing record.
	NodeID string
	// Similarity is the float32-recomputed cosine similarity.
	Similarity float64
}

// Outcome is the result of a save-guard Scan.
type Outcome struct {
	// Hold, when non-nil, is the best candidate at or above the hold
	// threshold that also passed Jaccard verification. The save must
	// not create a record; return the hold to the caller.
	Hold *Match
	// Advisory, when non-nil, is the best candidate in the advisory
	// band [advisory threshold, hold threshold) -- or a candidate at
	// hold-level cosine that failed Jaccard verification (structurally
	// similar, unverified). The save proceeds; the response carries
	// the notice.
	Advisory *Match
}

// Scan runs the save-guard candidate scan for a not-yet-inserted
// record described by its embedding and raw content. selfID is
// skipped when non-empty (post-insert re-checks).
//
// Candidates that are derived or non-knowledge nodes are never
// eligible: concepts and observations (machine-generated summaries --
// the exact shape behind historical false-supersession misfires) and
// collection items (identified by field.* properties; tracked data,
// not prose knowledge). Candidates with no content at all are
// likewise skipped.
//
// The caller must hold at least a read lock on the engine. The result
// is advisory with respect to concurrency: a similar record that
// commits after this scan is caught by the engine's delta re-scan
// under the write lock (see core.Engine.WritesSince).
func Scan(g *graph.Graph, vecIdx index.VectorIndex, cfg config.SaveGuardConfig, vec []float32, content string, selfID string) Outcome {
	var out Outcome
	if vecIdx.Len() < 1 || vec == nil {
		return out
	}
	// Unconfigured thresholds disable the guard rather than hold
	// everything: a zero-value SaveGuardConfig (raw struct construction
	// bypassing config.Load's normalization) must fail open.
	if cfg.SimilarHoldThreshold <= 0 {
		return out
	}
	results := vecIdx.Search(vec, 10, nil)
	for _, r := range results {
		if selfID != "" && r.NodeID == selfID {
			continue
		}
		candidate, ok := g.GetNode(r.NodeID)
		if !ok || excluded(candidate) {
			continue
		}
		sim := float64(r.Similarity)
		if candVec := nodeEmbedding(candidate); candVec != nil {
			sim = float64(index.CosineSimilarity(vec, candVec))
		}
		if sim < cfg.AdvisoryThreshold {
			continue
		}
		if sim >= cfg.SimilarHoldThreshold && verifyJaccard(g, content, r.NodeID) {
			if out.Hold == nil || sim > out.Hold.Similarity {
				out.Hold = &Match{NodeID: r.NodeID, Similarity: sim}
			}
			continue
		}
		if out.Advisory == nil || sim > out.Advisory.Similarity {
			out.Advisory = &Match{NodeID: r.NodeID, Similarity: sim}
		}
	}
	if out.Hold != nil {
		// A hold supersedes any advisory -- the caller acts on the
		// hold and the advisory would be noise.
		out.Advisory = nil
	}
	return out
}

// MatchAgainst evaluates one specific candidate for a hold: cosine
// against the candidate's stored embedding plus Jaccard verification.
// Used by the engine's delta re-scan under the write lock, where the
// candidate set is the handful of records committed since the
// off-lock Scan. Returns the similarity and whether the pair
// qualifies as a hold at the configured threshold.
func MatchAgainst(g *graph.Graph, cfg config.SaveGuardConfig, vec []float32, content string, candidateID string) (float64, bool) {
	candidate, ok := g.GetNode(candidateID)
	if !ok || excluded(candidate) {
		return 0, false
	}
	candVec := nodeEmbedding(candidate)
	if candVec == nil {
		return 0, false
	}
	sim := float64(index.CosineSimilarity(vec, candVec))
	if sim < cfg.SimilarHoldThreshold {
		return sim, false
	}
	return sim, verifyJaccard(g, content, candidateID)
}

// excluded reports whether a node is ineligible as a scan candidate:
// derived nodes (concepts, observations), collection items (field.*
// properties), and nodes with no content to judge against.
func excluded(n *graph.Node) bool {
	if nt, _ := n.Properties.GetString("node_type"); nt == "concept" || nt == "observation" {
		return true
	}
	for key := range n.Properties {
		if strings.HasPrefix(key, "field.") {
			return true
		}
	}
	return nodeContent(n) == ""
}

// Check reports whether nodeID is a near-duplicate of an existing
// record. Returns the existing node's ID and the cosine similarity
// when a duplicate is found, otherwise ("", 0).
//
// Deprecated: legacy auto-supersession scan, retained only while the
// remaining capture-site callers migrate to Scan. Remove with the
// supersession removal cleanup.
//
// The caller must hold at least a read lock on the engine -- this
// function does no locking of its own. It reads the graph and the
// vector index concurrently and assumes a consistent snapshot.
func Check(g *graph.Graph, vecIdx index.VectorIndex, cfg config.DedupConfig, nodeID string) (string, float64) {
	// The index contains nodeID itself, so fewer than two entries
	// means there is nothing else to compare against.
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
	return check(g, vecIdx, cfg, vec, nodeContent(n), nodeID)
}

// CheckVec reports whether a record that has NOT been inserted yet --
// described by its embedding vector and raw content -- is a
// near-duplicate of an existing record. Save uses it to run the
// candidate scan against a read snapshot before taking the write
// lock, so the O(N) search stays out of the critical section.
//
// The result is advisory: because the caller re-acquires the lock
// after this returns, a duplicate that commits in between is missed.
// Callers must re-verify the returned candidate under the write lock
// (existence, not-already-historical) before acting on it.
//
// The caller must hold at least a read lock on the engine.
func CheckVec(g *graph.Graph, vecIdx index.VectorIndex, cfg config.DedupConfig, vec []float32, content string) (string, float64) {
	if vecIdx.Len() < 1 || vec == nil {
		return "", 0
	}
	return check(g, vecIdx, cfg, vec, content, "")
}

// check is the shared candidate scan. selfID is skipped in the
// results (empty when the record is not in the index yet). content
// is the record's raw text for Jaccard verification.
func check(g *graph.Graph, vecIdx index.VectorIndex, cfg config.DedupConfig, vec []float32, content string, selfID string) (string, float64) {
	// Request extra candidates: one may be self (skipped) and others
	// may fail Jaccard verification.
	results := vecIdx.Search(vec, 10, nil)
	for _, r := range results {
		if selfID != "" && r.NodeID == selfID {
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
			if !verifyJaccard(g, content, r.NodeID) {
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
func verifyJaccard(g *graph.Graph, textA string, candidateID string) bool {
	candidate, ok := g.GetNode(candidateID)
	if !ok {
		return false
	}
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

// NodeEmbeddingAndContent returns a node's best available embedding
// (priority order embedding_full > medium > short > keywords) and its
// raw content (content_full preferred over content_short). Exported
// for the engine's post-insert re-scan entry point.
func NodeEmbeddingAndContent(n *graph.Node) ([]float32, string) {
	return nodeEmbedding(n), nodeContent(n)
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
