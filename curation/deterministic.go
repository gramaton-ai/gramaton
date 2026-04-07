// Package curation provides background maintenance for the knowledge
// store. Deterministic tasks (no LLM) run on a schedule; autonomous
// tasks (LLM-powered) run when a provider is configured.
package curation

import (
	"log/slog"
	"sort"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
	"github.com/brandonlattin/gramaton/search"
)

// DeterministicResult summarizes what a deterministic curation cycle did.
type DeterministicResult struct {
	LifecycleTransitions int
	OrphansLinked        int
	DuplicatesSuperseded int
	SectionsLinked       int
	GCCollected          int
	GCDryRun             bool
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
	TotalRecords       int            `json:"total_records"`
	TotalEdges         int            `json:"total_edges"`
	PendingCount       int            `json:"pending_count"`
	OrphanCount        int            `json:"orphan_count"`
	StaleCount         int            `json:"stale_count"`
	RecordsByType      map[string]int `json:"records_by_type"`
	TopKeywords        []string       `json:"top_keywords,omitempty"`
	EarliestRecord     time.Time      `json:"earliest_record,omitempty"`
	LatestRecord       time.Time      `json:"latest_record,omitempty"`
	QualitativeSummary string         `json:"qualitative_summary,omitempty"`
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

	// Top keywords for manifest (sorted by count, top 20).
	type kwEntry struct {
		keyword string
		count   int
	}
	var kwList []kwEntry
	for kw, count := range kwCounts {
		kwList = append(kwList, kwEntry{kw, count})
	}
	sort.Slice(kwList, func(i, j int) bool { return kwList[i].count > kwList[j].count })
	topN := 20
	if len(kwList) < topN {
		topN = len(kwList)
	}
	for i := 0; i < topN; i++ {
		manifest.TopKeywords = append(manifest.TopKeywords, kwList[i].keyword)
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

		// Duplicate consolidation with Jaccard verification to prevent
		// false positives on structurally similar long documents.
		for _, pair := range pairs {
			na, okA := e.Graph().GetNode(pair.IDA)
			nb, okB := e.Graph().GetNode(pair.IDB)
			if !okA || !okB {
				continue
			}

			// Jaccard guard: verify content similarity, not just embedding.
			if !verifyDedupJaccard(na, nb) {
				continue
			}

			olderID := pair.IDA
			newerID := pair.IDB
			// Determine which is older by created_at.
			caA, _ := na.Properties.GetTimestamp("created_at")
			caB, _ := nb.Properties.GetTimestamp("created_at")
			if caB.Before(caA) {
				olderID, newerID = newerID, olderID
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

	// --- Concept enrichment phase ---
	// Compute evidence_count and last_evidence_at for concept nodes.
	enrichConcepts(e, logger)

	// --- Cross-section linking phase ---
	// Find similar sections across different parent articles and create
	// related_to edges. This connects knowledge that spans documents.
	result.SectionsLinked = linkSections(e, cfg, logger)

	// --- Garbage collection phase ---
	// Hard delete records that meet ALL junk criteria.
	if cfg.GC.Enabled {
		gcCollected := collectGarbage(e, cfg, logger)
		result.GCCollected = gcCollected
		result.GCDryRun = cfg.GC.DryRun
	}

	result.ConceptCandidates = candidates
	result.Manifest = manifest

	if logger != nil && mutations > 0 {
		logger.Info("deterministic curation complete",
			"component", "curation",
			"lifecycle_transitions", result.LifecycleTransitions,
			"orphans_linked", result.OrphansLinked,
			"duplicates_superseded", result.DuplicatesSuperseded,
			"gc_collected", result.GCCollected,
			"concept_candidates", len(candidates))
	}

	return result
}

// collectGarbage hard-deletes records that meet ALL junk criteria:
// - processing_status still "captured" (never classified)
// - age > MinAgeDays (had time to be noticed)
// - access_count = 0 (never retrieved)
// - confidence < 0.3 (low/default confidence)
// - importance = 0 (no assigned importance)
// - no edges (nothing links to it)
// - temporality = "ephemeral" (short-lived by classification)
//
// In dry-run mode, returns the count that WOULD be deleted without deleting.
func collectGarbage(e *core.Engine, cfg config.Config, logger *slog.Logger) int {
	minAge := cfg.GC.MinAgeDays
	if minAge <= 0 {
		minAge = 30
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -minAge)

	// Read phase: identify GC candidates.
	var gcIDs []string

	e.RLock()
	for _, id := range e.Graph().AllNodeIDs() {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}

		// Must be "captured" (never classified).
		ps, ok := n.Properties.GetString("processing_status")
		if !ok || ps != "captured" {
			continue
		}

		// Must be old enough.
		ca, ok := n.Properties.GetTimestamp("created_at")
		if !ok || ca.After(cutoff) {
			continue
		}

		// Must have zero access.
		ac, _ := n.Properties.GetInt64("access_count")
		if ac > 0 {
			continue
		}

		// Must be low confidence.
		conf, _ := n.Properties.GetFloat64("confidence")
		if conf >= 0.3 {
			continue
		}

		// Must have zero importance.
		imp, _ := n.Properties.GetFloat64("importance")
		if imp > 0 {
			continue
		}

		// Must be ephemeral.
		temp, _ := n.Properties.GetString("temporality")
		if temp != "ephemeral" {
			continue
		}

		// Must have no edges.
		if nonChunkEdgeCount(e.Graph(), id) > 0 {
			continue
		}

		gcIDs = append(gcIDs, id)
	}
	e.RUnlock()

	if len(gcIDs) == 0 {
		return 0
	}

	if cfg.GC.DryRun {
		if logger != nil {
			logger.Info("GC dry-run: would delete records",
				"component", "curation",
				"count", len(gcIDs))
		}
		return len(gcIDs)
	}

	// Write phase: hard delete.
	e.Lock()
	deleted := 0
	for _, id := range gcIDs {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		e.PropIdx().RemoveNode(id, n.Properties)
		e.VecIdx().Remove(id)
		e.Graph().DeleteNode(id)
		deleted++
	}
	if deleted > 0 {
		e.Save("curation: garbage collection")
	}
	e.Unlock()

	if logger != nil && deleted > 0 {
		logger.Info("GC: deleted debris records",
			"component", "curation",
			"deleted", deleted)
	}

	return deleted
}

// isChunkNode checks if a node has an outbound chunk_of or section_of edge.
func isChunkNode(g *graph.Graph, id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if e.Type == "chunk_of" || e.Type == "section_of" {
			return true
		}
	}
	return false
}

// enrichConcepts updates evidence_count and last_evidence_at on concept
// nodes based on their inbound edges.
func enrichConcepts(e *core.Engine, logger *slog.Logger) {
	// Read phase: find concept nodes and compute their evidence.
	type conceptUpdate struct {
		id             string
		evidenceCount  int
		lastEvidenceAt time.Time
	}
	var updates []conceptUpdate

	e.RLock()
	conceptIDs := e.PropIdx().Lookup("knowledge_type", graph.StringProperty("conceptual"))
	for _, id := range conceptIDs {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		// Count inbound edges (evidence pointing to this concept).
		inbound := e.Graph().EdgesTo(id)
		count := 0
		var latestEvidence time.Time
		for _, edge := range inbound {
			if edge.Type == "chunk_of" || edge.Type == "section_of" {
				continue
			}
			count++
			// Check the source node's created_at for last_evidence_at.
			if src, ok := e.Graph().GetNode(edge.SourceID); ok {
				if ca, ok := src.Properties.GetTimestamp("created_at"); ok {
					if ca.After(latestEvidence) {
						latestEvidence = ca
					}
				}
			}
		}

		// Check if update is needed.
		existingCount, _ := n.Properties.GetInt64("evidence_count")
		if int64(count) != existingCount || count > 0 {
			updates = append(updates, conceptUpdate{
				id:             id,
				evidenceCount:  count,
				lastEvidenceAt: latestEvidence,
			})
		}
	}
	e.RUnlock()

	if len(updates) == 0 {
		return
	}

	// Write phase.
	e.Lock()
	changed := false
	for _, u := range updates {
		if _, ok := e.Graph().GetNode(u.id); !ok {
			continue
		}
		e.SetProp(u.id, "evidence_count", graph.Int64Property(int64(u.evidenceCount)))
		if !u.lastEvidenceAt.IsZero() {
			e.SetProp(u.id, "last_evidence_at", graph.TimestampProperty(u.lastEvidenceAt))
		}
		changed = true
	}
	if changed {
		e.Save("curation: concept enrichment")
	}
	e.Unlock()

	if logger != nil && len(updates) > 0 {
		logger.Info("concept enrichment complete",
			"component", "curation",
			"concepts_updated", len(updates))
	}
}

// dedupJaccardMin is the minimum word-level Jaccard similarity required
// to confirm a cosine-based duplicate match. True duplicates (even with
// minor edits) easily exceed this; structurally similar but semantically
// different documents fall well below.
const dedupJaccardMin = 0.3

// verifyDedupJaccard checks whether two nodes are genuine duplicates by
// comparing word-level Jaccard similarity on their content. Returns false
// for long documents with high cosine similarity but different content.
func verifyDedupJaccard(a, b *graph.Node) bool {
	textA := curationNodeText(a)
	textB := curationNodeText(b)

	// Skip check for short content where cosine alone is reliable.
	if len(textA) < 200 && len(textB) < 200 {
		return true
	}

	tokA := index.Tokenize(textA)
	tokB := index.Tokenize(textB)
	return index.JaccardSimilarity(tokA, tokB) >= dedupJaccardMin
}

// curationNodeText returns the best content text for Jaccard comparison.
func curationNodeText(n *graph.Node) string {
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

// linkSections finds semantically similar sections across different parent
// documents and creates related_to edges. This is the Mem0-inspired
// entity resolution pattern: embedding comparison scoped to sections,
// with sibling exclusion to avoid linking sections from the same article.
func linkSections(e *core.Engine, cfg config.Config, logger *slog.Logger) int {
	minSim := cfg.Curation.SectionLinkMin
	if minSim <= 0 {
		minSim = 0.75
	}
	maxLinks := cfg.Curation.MaxSectionLinksPerRun
	if maxLinks <= 0 {
		maxLinks = 30
	}

	// --- Read phase: collect section nodes and their parents ---
	e.RLock()
	g := e.Graph()

	type sectionInfo struct {
		id       string
		parentID string
	}
	var sections []sectionInfo

	for _, id := range g.AllNodeIDs() {
		for _, edge := range g.EdgesFrom(id) {
			if edge.Type == "section_of" {
				sections = append(sections, sectionInfo{
					id:       id,
					parentID: edge.TargetID,
				})
				break
			}
		}
	}

	if len(sections) < 2 {
		e.RUnlock()
		return 0
	}

	// Build parent lookup and existing-edge lookup for fast checks.
	parentOf := make(map[string]string, len(sections))
	for _, s := range sections {
		parentOf[s.id] = s.parentID
	}

	// Track existing related_to edges between sections to avoid duplicates.
	type pairKey struct{ a, b string }
	existingEdges := make(map[pairKey]struct{})
	for _, s := range sections {
		for _, edge := range g.EdgesFrom(s.id) {
			if edge.Type == "related_to" {
				a, b := s.id, edge.TargetID
				if a > b {
					a, b = b, a
				}
				existingEdges[pairKey{a, b}] = struct{}{}
			}
		}
	}

	// Find candidate links: similar sections from different parents.
	type linkCandidate struct {
		sourceID   string
		targetID   string
		similarity float64
	}
	var candidates []linkCandidate
	seen := make(map[pairKey]struct{})

	for _, s := range sections {
		if len(candidates) >= maxLinks {
			break
		}

		n, ok := g.GetNode(s.id)
		if !ok {
			continue
		}
		vec, ok := n.Properties.GetVector("embedding_full")
		if !ok {
			continue
		}

		results := e.VecIdx().Search(vec, 6, nil)
		for _, r := range results {
			if r.NodeID == s.id {
				continue
			}
			if float64(r.Similarity) < minSim {
				continue
			}

			// Skip if not a section node.
			targetParent, isSection := parentOf[r.NodeID]
			if !isSection {
				continue
			}

			// Skip siblings (same parent).
			if targetParent == s.parentID {
				continue
			}

			// Skip already-linked pairs.
			a, b := s.id, r.NodeID
			if a > b {
				a, b = b, a
			}
			pk := pairKey{a, b}
			if _, ok := existingEdges[pk]; ok {
				continue
			}
			if _, ok := seen[pk]; ok {
				continue
			}
			seen[pk] = struct{}{}

			candidates = append(candidates, linkCandidate{
				sourceID:   s.id,
				targetID:   r.NodeID,
				similarity: float64(r.Similarity),
			})
		}
	}

	e.RUnlock()

	if len(candidates) == 0 {
		return 0
	}

	// --- Write phase ---
	e.Lock()
	defer e.Unlock()

	linked := 0
	for _, c := range candidates {
		if _, ok := e.Graph().GetNode(c.sourceID); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(c.targetID); !ok {
			continue
		}
		_, err := e.Graph().AddEdge(c.sourceID, c.targetID, "related_to", c.similarity, nil)
		if err == nil {
			linked++
		}
	}

	if linked > 0 {
		e.Save("curation: section linking")
		if logger != nil {
			logger.Info("cross-section linking complete",
				"component", "curation",
				"sections_linked", linked)
		}
	}

	return linked
}

// nonChunkEdgeCount returns the total edge count excluding structural edges.
func nonChunkEdgeCount(g *graph.Graph, id string) int {
	count := 0
	for _, e := range g.EdgesFrom(id) {
		if e.Type != "chunk_of" && e.Type != "section_of" {
			count++
		}
	}
	for _, e := range g.EdgesTo(id) {
		if e.Type != "chunk_of" && e.Type != "section_of" {
			count++
		}
	}
	return count
}
