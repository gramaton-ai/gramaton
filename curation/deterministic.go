// Package curation provides background maintenance for the knowledge
// store. Deterministic tasks (no LLM) run on a schedule; autonomous
// tasks (LLM-powered) run when a provider is configured.
package curation

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/search"
)

// ensureLogger returns a no-op logger if the provided logger is nil.
func ensureLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// DeterministicResult summarizes what a deterministic curation cycle did.
type DeterministicResult struct {
	LifecycleTransitions int
	OrphansLinked        int
	DuplicatesSuperseded int
	SectionsLinked       int
	ObservationsCreated  int // observation child nodes extracted (D18, D23)
	ConceptsCreated      int // new concept nodes created (template content)
	GCCollected          int
	GCDryRun             bool
	QualityRepairs       int // deterministic quality fixes applied
	QualityFlags         int // records flagged for autonomous repair
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
	start := time.Now()
	logger = ensureLogger(logger)
	result := &DeterministicResult{}

	// All read-gather happens under RLock. We collect IDs and data,
	// then release the lock before mutating.

	// --- Read phase ---
	e.RLock()

	now := time.Now().UTC()
	g := e.Graph()

	// Lifecycle: find stale records that need valid_until.
	var staleIDs []string
	// Orphans: find records with 0 non-chunk edges.
	var orphanIDs []string
	// Manifest aggregates.
	manifest := &StoreManifest{
		RecordsByType: make(map[string]int),
	}
	staleCount := 0

	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		id := n.ID

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
		// Skip low-quality records to prevent creating edges on junk
		// that blocks GC. Unclassified (captured) records should be
		// classified first; low-confidence records are likely noise.
		ec := nonChunkEdgeCount(g, id)
		if ec == 0 {
			conf, hasConf := n.Properties.GetFloat64("confidence")
			ps, _ := n.Properties.GetString("processing_status")
			if ps != "captured" && (!hasConf || conf >= 0.3) {
				orphanIDs = append(orphanIDs, id)
			}
		}
	}

	it.Close()

	manifest.TotalEdges = g.EdgeCount()
	manifest.OrphanCount = len(orphanIDs)
	manifest.StaleCount = staleCount

	// Concept candidates: keywords above emergence threshold but below
	// specificity ceiling. Keywords that appear in too many records are
	// corpus-wide vocabulary, not useful concepts.
	kwCounts := e.PropIdx().KeywordCounts("content_keywords")
	threshold := cfg.Concepts.EmergenceThreshold
	maxPct := cfg.Concepts.MaxKeywordPct
	if maxPct <= 0 {
		maxPct = 0.2
	}
	maxCount := int(float64(manifest.TotalRecords) * maxPct)
	if maxCount < threshold {
		maxCount = manifest.TotalRecords // don't filter if store is tiny
	}

	var candidates []ConceptCandidate
	for kw, count := range kwCounts {
		if count < threshold || count > maxCount {
			continue
		}

		// Quality gate: skip keywords with too many words (likely
		// garbage from bad classification, e.g., concatenated tag dumps).
		if strings.Count(kw, " ") > 3 {
			continue
		}

		// Quality gate: skip proper names. A keyword like "Thomas Bayes"
		// is useful for search but not a concept to synthesize.
		if isLikelyProperName(kw) {
			continue
		}

		// Quality gate: skip weak/meta keywords that don't represent
		// real concepts ("article", "section", "overview", etc.).
		if isWeakConceptKeyword(kw) {
			continue
		}

		ids := e.PropIdx().LookupKeyword("content_keywords", kw)
		candidates = append(candidates, ConceptCandidate{
			Keyword: kw,
			Count:   count,
			NodeIDs: ids,
		})
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

	// Quality audit: find records with metadata gaps that hurt retrieval.
	type qualityIssue struct {
		nodeID string
		fix    string // "concept_summary" | "extract_short" | "flag_embed"
		short  string // new content_short for deterministic fixes
	}
	var qualityIssues []qualityIssue

	it2 := g.NodeIterator()
	for it2.Next() {
		n := it2.Node()
		id := n.ID
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		contentFull, hasFullContent := n.Properties.GetString("content_full")
		contentShort, hasShort := n.Properties.GetString("content_short")
		_, hasEmbedShort := n.Properties["embedding_short"]

		// Rule 1: Concept node with label-as-summary.
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok && contentShort == kw && hasFullContent {
				qualityIssues = append(qualityIssues, qualityIssue{
					nodeID: id,
					fix:    "concept_summary",
					short:  conceptShortSummary(contentFull, 200),
				})
				continue
			}
		}

		// Rule 2: content_short too short relative to content_full.
		if hasShort && hasFullContent && len(contentShort) < 20 && len(contentFull) > 100 {
			// Short is suspiciously brief. Extract first sentence from full.
			qualityIssues = append(qualityIssues, qualityIssue{
				nodeID: id,
				fix:    "extract_short",
				short:  conceptShortSummary(contentFull, 200),
			})
			continue
		}

		// Rule 3: content_short exists but embedding_short is missing.
		if hasShort && len(contentShort) > 10 && !hasEmbedShort {
			qualityIssues = append(qualityIssues, qualityIssue{
				nodeID: id,
				fix:    "flag_embed",
			})
		}
	}
	it2.Close()

	// Find existing concept nodes to avoid creating duplicates.
	existingConcepts := make(map[string]struct{})
	cnIt := g.NodeIterator()
	for cnIt.Next() {
		n := cnIt.Node()
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok {
				existingConcepts[kw] = struct{}{}
			}
		}
	}
	cnIt.Close()

	// Filter candidates to only new concepts.
	var newConcepts []ConceptCandidate
	maxNewConcepts := cfg.LLMCuration.MaxConceptsPerRun // reuse as deterministic budget
	if maxNewConcepts <= 0 {
		maxNewConcepts = 5
	}
	for _, c := range candidates {
		if _, exists := existingConcepts[c.Keyword]; exists {
			continue
		}
		newConcepts = append(newConcepts, c)
		if len(newConcepts) >= maxNewConcepts {
			break
		}
	}

	e.RUnlock()

	// --- Write phase ---
	mutations := len(staleIDs) + len(orphanLinks) + len(pairs) + len(qualityIssues) + len(newConcepts)
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

			// Observations are derived from their parents, so observation-
			// vs-parent similarity is not a duplicate signal. FindDuplicates
			// already filters observation_of pairs, but we defense-in-depth
			// here in case an observation ever lands without its edge.
			if nt, ok := na.Properties.GetString("node_type"); ok && nt == "observation" {
				continue
			}
			if nt, ok := nb.Properties.GetString("node_type"); ok && nt == "observation" {
				continue
			}

			// Collection members have structured per-item fields (status,
			// title, etc.). Silently consolidating them on embedding alone
			// would merge distinct tracked work. Operators still see these
			// pairs via gramaton_duplicates for manual triage.
			if isCollectionMember(e.Graph(), na.ID) || isCollectionMember(e.Graph(), nb.ID) {
				continue
			}

			// Jaccard guard: verify content similarity, not just embedding.
			if !verifyDedupJaccard(na, nb) {
				continue
			}

			// Determine which is older. Tie-break on identical
			// created_at (common in bulk imports) by inbound edge
			// count -- keep the more-referenced record as the
			// "newer" survivor since rewriting more inbound edges
			// is more destructive. Final fallback is lex order on
			// ID, which matches FindDuplicates' canonical pair
			// ordering and stays deterministic.
			// (Wave 3 P1-36: previously the lex-smaller ID was
			// silently chosen as "older" on identical timestamps,
			// because pair.IDA = lex-smaller per FindDuplicates.)
			caA, _ := na.Properties.GetTimestamp("created_at")
			caB, _ := nb.Properties.GetTimestamp("created_at")
			olderID, newerID := pickOlder(e.Graph(), pair.IDA, pair.IDB, caA, caB)

			older, ok := e.Graph().GetNode(olderID)
			if !ok {
				continue
			}
			// Skip if already historical.
			if _, has := older.Properties.GetTimestamp("valid_until"); has {
				continue
			}
			e.SetProp(olderID, "valid_until", graph.TimestampProperty(now))
			e.SetProp(olderID, "resolution", graph.StringProperty("superseded"))
			e.SetProp(olderID, "resolved_at", graph.TimestampProperty(now))
			if _, err := e.Graph().AddEdge(newerID, olderID, "supersedes", pair.Similarity, nil); err != nil {
				logger.Error("failed to add supersedes edge",
					"component", "curation", "newer", newerID, "older", olderID, "err", err)
			}
			result.DuplicatesSuperseded++
		}

		// Quality repairs: fix deterministic issues, flag others.
		for _, qi := range qualityIssues {
			if _, ok := e.Graph().GetNode(qi.nodeID); !ok {
				continue
			}
			switch qi.fix {
			case "concept_summary", "extract_short":
				// Deterministic fix: update content_short.
				e.SetContentProp(qi.nodeID, "content_short", qi.short)
				result.QualityRepairs++
			case "flag_embed":
				// Can't fix deterministically (needs embedder).
				// Stale count in manifest already captures this;
				// reembed pipeline will handle it.
				result.QualityFlags++
			}
		}

		// Deterministic concept creation: create concept nodes with
		// template content and computed metadata. LLM synthesis is
		// deferred to the autonomous enrichment phase.
		for _, c := range newConcepts {
			// Compute metadata from member records.
			var confSum float64
			var confCount int
			var coKeywords []string
			coKWCounts := make(map[string]int)
			hasSpeculative := false
			allWellEstablished := true

			for _, memberID := range c.NodeIDs {
				mn, ok := e.Graph().GetNode(memberID)
				if !ok {
					continue
				}
				if conf, ok := mn.Properties.GetFloat64("confidence"); ok && conf > 0 {
					confSum += conf
					confCount++
				}
				if es, ok := mn.Properties.GetString("epistemic_status"); ok {
					if es == "speculative" {
						hasSpeculative = true
					}
					if es != "well_established" {
						allWellEstablished = false
					}
				}
				if kws, ok := mn.Properties.GetStringList("content_keywords"); ok {
					for _, mk := range kws {
						if mk != c.Keyword {
							coKWCounts[mk]++
						}
					}
				}
			}

			// Derived confidence: average of member confidence.
			derivedConf := 0.7 // default
			if confCount > 0 {
				derivedConf = confSum / float64(confCount)
			}

			// Derived epistemic status.
			derivedES := "probable"
			if allWellEstablished {
				derivedES = "well_established"
			} else if hasSpeculative {
				derivedES = "probable" // mixed -> probable
			}

			// Top co-occurring keywords (up to 5).
			type kwE struct {
				kw    string
				count int
			}
			var coKWList []kwE
			for kw, cnt := range coKWCounts {
				coKWList = append(coKWList, kwE{kw, cnt})
			}
			sort.Slice(coKWList, func(i, j int) bool { return coKWList[i].count > coKWList[j].count })
			topCoKW := 5
			if len(coKWList) < topCoKW {
				topCoKW = len(coKWList)
			}
			for i := 0; i < topCoKW; i++ {
				coKeywords = append(coKeywords, coKWList[i].kw)
			}

			// Template content.
			templateFull := fmt.Sprintf("Concept: %s. Connects %d records.", c.Keyword, c.Count)
			if len(coKeywords) > 0 {
				templateFull += fmt.Sprintf(" Related terms: %s.", strings.Join(coKeywords, ", "))
			}
			templateShort := fmt.Sprintf("%s (%d records)", c.Keyword, c.Count)
			if len(templateShort) > 200 {
				templateShort = templateShort[:200]
			}

			allKeywords := append([]string{c.Keyword}, coKeywords...)

			props := graph.Properties{
				"content_full":      graph.StringProperty(templateFull),
				"content_short":     graph.StringProperty(templateShort),
				"content_keywords":  graph.StringListProperty(allKeywords),
				"processing_status": graph.StringProperty("processed"),
				"synthesis_status":  graph.StringProperty("pending"),
				"node_type":         graph.StringProperty("concept"),
				"concept_keyword":   graph.StringProperty(c.Keyword),
				"temporality":       graph.StringProperty("durable"),
				"knowledge_type":    graph.StringProperty("conceptual"),
				"epistemic_status":  graph.StringProperty(derivedES),
				"confidence":        graph.Float64Property(derivedConf),
				"evidence_count":    graph.Int64Property(int64(c.Count)),
				"created_at":        graph.TimestampProperty(now),
				"access_count":      graph.Int64Property(0),
			}

			cn := e.Graph().AddNode(props)
			for k, v := range cn.Properties {
				e.PropIdx().Add(cn.ID, k, v)
			}
			e.IndexNode(cn.ID, templateFull, nil)

			// Create instance_of edges from member records.
			for _, memberID := range c.NodeIDs {
				if _, ok := e.Graph().GetNode(memberID); ok {
					if _, err := e.Graph().AddEdge(memberID, cn.ID, "instance_of", 0.8, nil); err != nil {
						logger.Error("failed to add instance_of edge",
							"component", "curation", "member", memberID, "concept", cn.ID, "err", err)
					}
				}
			}

			result.ConceptsCreated++
		}

		if result.LifecycleTransitions+result.OrphansLinked+result.DuplicatesSuperseded+result.QualityRepairs+result.ConceptsCreated > 0 {
			e.SaveOrLog("curation: deterministic")
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

	// --- Observation extraction phase (D18, D23) ---
	// Extract key sentences from processed records >500 chars that
	// don't yet have observation children. Embeds via BERT (~30ms each).
	result.ObservationsCreated = extractAndCreateObservations(e, cfg, logger)

	// --- Garbage collection phase ---
	// Hard delete records that meet ALL junk criteria.
	if cfg.GC.Enabled {
		gcCollected := collectGarbage(e, cfg, logger)
		result.GCCollected = gcCollected
		result.GCDryRun = cfg.GC.DryRun
	}

	result.ConceptCandidates = candidates
	result.Manifest = manifest

	if mutations > 0 || result.ObservationsCreated > 0 {
		logger.Info("deterministic curation complete",
			"component", "curation",
			"lifecycle_transitions", result.LifecycleTransitions,
			"orphans_linked", result.OrphansLinked,
			"duplicates_superseded", result.DuplicatesSuperseded,
			"observations_created", result.ObservationsCreated,
			"quality_repairs", result.QualityRepairs,
			"quality_flags", result.QualityFlags,
			"gc_collected", result.GCCollected,
			"concepts_created", result.ConceptsCreated,
			"concept_candidates", len(candidates),
			"duration_ms", time.Since(start).Milliseconds())
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
	logger = ensureLogger(logger)
	minAge := cfg.GC.MinAgeDays
	if minAge <= 0 {
		minAge = 30
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -minAge)

	// Read phase: identify GC candidates.
	var gcIDs []string

	e.RLock()
	gcIt := e.Graph().NodeIterator()
	for gcIt.Next() {
		n := gcIt.Node()
		id := n.ID

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
	gcIt.Close()
	e.RUnlock()

	if len(gcIDs) == 0 {
		return 0
	}

	if cfg.GC.DryRun {
		logger.Info("GC dry-run: would delete records",
			"component", "curation",
			"count", len(gcIDs))
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
		e.BM25Full().Remove(id)
		e.VecIdx().Remove(id)
		if e.SecIdx() != nil {
			e.SecIdx().RemoveNode(id)
		}
		e.Graph().DeleteNode(id)
		deleted++
	}
	if deleted > 0 {
		e.SaveOrLog("curation: garbage collection")
	}
	e.Unlock()

	if deleted > 0 {
		logger.Info("GC: deleted debris records",
			"component", "curation",
			"deleted", deleted)
	}

	return deleted
}

func isChunkNode(g graph.NodeReader, id string) bool { return g.IsStructuralChild(id) }

// isCollectionMember returns true if id has a member_of edge to any
// collection. Used by the dedup loop to skip collection items --
// their structured per-item fields shouldn't be merged on embedding
// similarity alone.
func isCollectionMember(g graph.NodeReader, id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if e.Type == "member_of" {
			return true
		}
	}
	return false
}

// enrichConcepts updates evidence_count and last_evidence_at on concept
// nodes based on their inbound edges.
func enrichConcepts(e *core.Engine, logger *slog.Logger) {
	logger = ensureLogger(logger)
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
			if graph.IsStructuralEdge(edge.Type) {
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
		e.SaveOrLog("curation: concept enrichment")
	}
	e.Unlock()

	if len(updates) > 0 {
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
	logger = ensureLogger(logger)
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

	slIt := g.NodeIterator()
	for slIt.Next() {
		id := slIt.Node().ID
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
	slIt.Close()

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
		e.SaveOrLog("curation: section linking")
		logger.Info("cross-section linking complete",
			"component", "curation",
			"sections_linked", linked)
	}

	return linked
}

func nonChunkEdgeCount(g graph.NodeReader, id string) int { return g.SemanticEdgeCount(id) }

// isLikelyProperName returns true if a keyword looks like a person
// or place name: 2-3 words where every word starts with uppercase.
// "Thomas Bayes" -> true, "Bayesian epistemology" -> false (second
// word lowercase), "kafka" -> false (single word).
func isLikelyProperName(kw string) bool {
	words := strings.Fields(kw)
	if len(words) < 2 || len(words) > 3 {
		return false
	}
	for _, w := range words {
		if len(w) == 0 || w[0] < 'A' || w[0] > 'Z' {
			return false
		}
	}
	return true
}

// isWeakConceptKeyword returns true if a keyword is too short, too
// pickOlder selects which of (idA, idB) should be marked as the
// historical/older record in an auto-supersession pair. Strategy:
//
//  1. If created_at differs, the older timestamp wins.
//  2. If timestamps are identical (common in bulk imports), the
//     record with FEWER inbound edges is treated as older. This
//     keeps the more-referenced record alive so we rewrite fewer
//     existing graph relationships.
//  3. Final fallback is lex-order on ID for determinism.
//
// Returns (olderID, newerID).
func pickOlder(g *graph.Graph, idA, idB string, caA, caB time.Time) (string, string) {
	if caA.Before(caB) {
		return idA, idB
	}
	if caB.Before(caA) {
		return idB, idA
	}
	// Identical timestamps: tie-break on inbound edge count.
	inA := len(g.EdgesTo(idA))
	inB := len(g.EdgesTo(idB))
	if inA < inB {
		return idA, idB
	}
	if inB < inA {
		return idB, idA
	}
	// Final fallback: lex-order. Matches FindDuplicates' canonical
	// pair ordering so behaviour is at least deterministic across
	// runs even when fully ambiguous.
	if idA < idB {
		return idA, idB
	}
	return idB, idA
}

// generic, or is a meta-term that doesn't represent a real concept.
func isWeakConceptKeyword(kw string) bool {
	if len(kw) < 3 {
		return true
	}
	// Meta-terms that describe the source, not the content.
	weak := map[string]bool{
		"article": true, "section": true, "overview": true,
		"summary": true, "reference": true, "document": true,
		"note": true, "notes": true, "todo": true,
	}
	return weak[strings.ToLower(kw)]
}
