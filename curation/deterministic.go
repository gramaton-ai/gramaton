// Package curation provides background maintenance for the knowledge
// store. Deterministic tasks (no LLM) run on a schedule; autonomous
// tasks (LLM-powered) run when a provider is configured.
package curation

import (
	"log/slog"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/search"
)

// DeterministicResult summarizes what a deterministic curation cycle did.
type DeterministicResult struct {
	LifecycleTransitions int
	OrphansLinked        int
	DuplicatesSuperseded int
	ConceptCandidates    []ConceptCandidate
	Manifest             *StoreManifest
}

// ConceptCandidate is a keyword that appears on enough records to
// potentially be promoted to a concept node by the LLM layer.
type ConceptCandidate struct {
	Keyword string   `json:"keyword"`
	Count   int      `json:"count"`
	NodeIDs []string `json:"node_ids"`
}

// StoreManifest is a lightweight summary of what the store contains.
type StoreManifest struct {
	TotalRecords  int            `json:"total_records"`
	TotalEdges    int            `json:"total_edges"`
	PendingCount  int            `json:"pending_count"`
	OrphanCount   int            `json:"orphan_count"`
	StaleCount    int            `json:"stale_count"`
	RecordsByType map[string]int `json:"records_by_type"`
	EarliestRecord time.Time    `json:"earliest_record,omitempty"`
	LatestRecord   time.Time    `json:"latest_record,omitempty"`
}

// RunDeterministic performs all deterministic curation tasks.
// It acquires and releases locks as needed -- caller must NOT hold
// any lock.
func RunDeterministic(e *core.Engine, cfg config.Config, logger *slog.Logger) *DeterministicResult {
	result := &DeterministicResult{}

	// All read-gather happens under RLock. We collect IDs and data,
	// then release the lock before mutating.

	// --- Read phase ---
	e.RLock()

	now := time.Now().UTC()
	g := e.Graph()
	allIDs := g.AllNodeIDs()

	// Lifecycle: find stale records that need valid_until.
	var staleIDs []string
	// Orphans: find records with 0 non-chunk edges.
	var orphanIDs []string
	// Manifest aggregates.
	manifest := &StoreManifest{
		RecordsByType: make(map[string]int),
	}
	staleCount := 0

	for _, id := range allIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}

		// Skip chunks and deleted records.
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		// Skip concept nodes.
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			continue
		}

		manifest.TotalRecords++

		// Track by knowledge type.
		if kt, ok := n.Properties.GetString("knowledge_type"); ok {
			manifest.RecordsByType[kt]++
		}

		// Track temporal range.
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			if manifest.EarliestRecord.IsZero() || ca.Before(manifest.EarliestRecord) {
				manifest.EarliestRecord = ca
			}
			if ca.After(manifest.LatestRecord) {
				manifest.LatestRecord = ca
			}
		}

		// Pending count.
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "captured" {
			manifest.PendingCount++
		}

		// Staleness check for lifecycle transitions.
		staleness := search.ComputeStaleness(n, now, cfg.Decay)
		if staleness > 0.5 {
			staleCount++
		}

		temp, _ := n.Properties.GetString("temporality")
		_, hasValidUntil := n.Properties.GetTimestamp("valid_until")
		if !hasValidUntil {
			shouldExpire := false
			if temp == "ephemeral" && staleness >= cfg.Curation.StaleEphemeralScore {
				shouldExpire = true
			}
			if temp == "temporal" && staleness >= cfg.Curation.StaleTemporalScore {
				shouldExpire = true
			}
			if shouldExpire {
				staleIDs = append(staleIDs, id)
			}
		}

		// Orphan detection: records with 0 non-chunk edges.
		ec := nonChunkEdgeCount(g, id)
		if ec == 0 {
			orphanIDs = append(orphanIDs, id)
		}
	}

	manifest.TotalEdges = g.EdgeCount()
	manifest.OrphanCount = len(orphanIDs)
	manifest.StaleCount = staleCount

	// Concept candidates: keywords above emergence threshold.
	kwCounts := e.PropIdx().KeywordCounts("content_keywords")
	threshold := cfg.Concepts.EmergenceThreshold
	var candidates []ConceptCandidate
	for kw, count := range kwCounts {
		if count >= threshold {
			// Get node IDs for this keyword.
			ids := e.PropIdx().LookupKeyword("content_keywords", kw)
			candidates = append(candidates, ConceptCandidate{
				Keyword: kw,
				Count:   count,
				NodeIDs: ids,
			})
		}
	}

	// Duplicate detection.
	dedupThreshold := cfg.Dedup.SimilarityThreshold
	maxDedup := cfg.Curation.MaxDedupPerRun
	pairs := search.FindDuplicates(g, e.VecIdx(), dedupThreshold, maxDedup)

	// Orphan similarity search: for each orphan, find similar records.
	type orphanLink struct {
		orphanID  string
		targetID  string
		similarity float64
	}
	var orphanLinks []orphanLink
	maxOrphans := cfg.Curation.MaxOrphansPerRun
	if len(orphanIDs) > maxOrphans {
		orphanIDs = orphanIDs[:maxOrphans]
	}
	for _, oid := range orphanIDs {
		n, ok := g.GetNode(oid)
		if !ok {
			continue
		}
		vec, ok := n.Properties["embedding_full"]
		if !ok {
			continue
		}
		results := e.VecIdx().Search(vec.Vector(), 3, nil)
		for _, r := range results {
			if r.NodeID == oid {
				continue
			}
			if float64(r.Similarity) >= cfg.Curation.OrphanSimilarityMin {
				orphanLinks = append(orphanLinks, orphanLink{
					orphanID:   oid,
					targetID:   r.NodeID,
					similarity: float64(r.Similarity),
				})
				break // one link per orphan is enough
			}
		}
	}

	e.RUnlock()

	// --- Write phase ---
	mutations := len(staleIDs) + len(orphanLinks) + len(pairs)
	if mutations > 0 {
		e.Lock()

		// Lifecycle transitions: set valid_until on stale records.
		for _, id := range staleIDs {
			if _, ok := e.Graph().GetNode(id); ok {
				e.SetProp(id, "valid_until", graph.TimestampProperty(now))
				result.LifecycleTransitions++
			}
		}

		// Orphan linking.
		for _, ol := range orphanLinks {
			if _, ok := e.Graph().GetNode(ol.orphanID); !ok {
				continue
			}
			if _, ok := e.Graph().GetNode(ol.targetID); !ok {
				continue
			}
			_, err := e.Graph().AddEdge(ol.orphanID, ol.targetID, "related_to", ol.similarity, nil)
			if err == nil {
				result.OrphansLinked++
			}
		}

		// Duplicate consolidation.
		for _, pair := range pairs {
			olderID := pair.IDA
			newerID := pair.IDB
			// Determine which is older by created_at.
			if na, ok := e.Graph().GetNode(pair.IDA); ok {
				if nb, ok := e.Graph().GetNode(pair.IDB); ok {
					caA, _ := na.Properties.GetTimestamp("created_at")
					caB, _ := nb.Properties.GetTimestamp("created_at")
					if caB.Before(caA) {
						olderID, newerID = newerID, olderID
					}
				}
			}
			older, ok := e.Graph().GetNode(olderID)
			if !ok {
				continue
			}
			// Skip if already historical.
			if _, has := older.Properties.GetTimestamp("valid_until"); has {
				continue
			}
			e.SetProp(olderID, "valid_until", graph.TimestampProperty(now))
			e.Graph().AddEdge(newerID, olderID, "supersedes", pair.Similarity, nil)
			result.DuplicatesSuperseded++
		}

		if result.LifecycleTransitions+result.OrphansLinked+result.DuplicatesSuperseded > 0 {
			e.Save("curation: deterministic")
		}

		e.Unlock()
	}

	result.ConceptCandidates = candidates
	result.Manifest = manifest

	if logger != nil && mutations > 0 {
		logger.Info("deterministic curation complete",
			"component", "curation",
			"lifecycle_transitions", result.LifecycleTransitions,
			"orphans_linked", result.OrphansLinked,
			"duplicates_superseded", result.DuplicatesSuperseded,
			"concept_candidates", len(candidates))
	}

	return result
}

// isChunkNode checks if a node has an outbound chunk_of edge.
func isChunkNode(g *graph.Graph, id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if e.Type == "chunk_of" {
			return true
		}
	}
	return false
}

// nonChunkEdgeCount returns the total edge count excluding chunk_of edges.
func nonChunkEdgeCount(g *graph.Graph, id string) int {
	count := 0
	for _, e := range g.EdgesFrom(id) {
		if e.Type != "chunk_of" {
			count++
		}
	}
	for _, e := range g.EdgesTo(id) {
		if e.Type != "chunk_of" {
			count++
		}
	}
	return count
}
