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

// NodeAuthor is the system-author identity stamped on nodes the
// curation subsystem creates itself (concept nodes in
// deterministic.go, observation nodes in observe.go). System-created
// records carry this constant unconditionally -- never the operator's
// configured author -- so provenance distinguishes derived nodes from
// user-initiated captures. The author backfill (cli/backfill.go)
// stamps the same constant when retro-attributing curation-created
// nodes in pre-attribution stores.
const NodeAuthor = core.CurationAuthor

// ensureLogger returns a no-op logger if the provided logger is nil.
func ensureLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// DeterministicResult summarizes what a deterministic curation cycle did.
type DeterministicResult struct {
	LifecycleTransitions   int
	OrphansLinked          int
	SectionsLinked         int
	ObservationsCreated    int // observation child nodes extracted (D18, D23)
	ConceptsCreated        int // new concept nodes created (template content)
	ConceptsAliased        int // candidate keywords merged into existing concepts as aliases (Phase F)
	ConceptMembersAttached int // instance_of edges added to existing concepts for new evidence (#99)
	GCCollected            int
	GCDryRun               bool
	QualityRepairs         int // deterministic quality fixes applied
	QualityFlags           int // records flagged for autonomous repair
	ConceptCandidates      []ConceptCandidate
	Manifest               *StoreManifest
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

// maxConceptAttachPerRun caps how many instance_of attachment edges
// one deterministic cycle adds to existing concepts (#99). The first
// cycle against an old store can find a large backlog of members that
// were never linked; the cap keeps the write batch bounded and the
// remainder drains on subsequent cycles. There is no per-phase config
// knob for attachments (unlike MaxOrphansPerRun), so this is a
// constant set to the 200 ceiling config validation clamps that knob
// to.
const maxConceptAttachPerRun = 200

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

	// Single-pass collector: this loop replaces three previously-separate
	// full-graph iterators (it / it2 / cnIt). Each iterator did its own
	// O(N) scan of the node set with overlapping skip filters. At 100k
	// nodes on a 1m curation cadence that was 3 full scans every 60s
	// just for the read phase — wasted disk + CPU. The pass below
	// branches on node_type to feed the right phase: concept nodes
	// populate `existingConcepts` and run the concept-quality rule;
	// non-concept records feed the manifest stats, lifecycle/orphan
	// checks, and the two non-concept quality rules.
	type qualityIssue struct {
		nodeID string
		fix    string // "concept_summary" | "extract_short" | "flag_embed"
		short  string // new content_short for deterministic fixes
	}
	var qualityIssues []qualityIssue
	existingConcepts := make(map[string]struct{})
	// conceptIDByKeyword resolves a keyword back to the concept node
	// that owns it. A claim records HOW the keyword resolved: via the
	// concept's primary concept_keyword, or via content_keywords --
	// which mixes Phase F aliases with the co-occurring "related
	// terms" stamped at emergence and cannot tell them apart. The
	// candidate loop below uses claims to attach new members to an
	// existing concept instead of skipping the keyword outright
	// (#99); non-primary claims must additionally pass the Phase F
	// member-overlap gate before attaching, because attaching on
	// co-occurrence alone would link unrelated records as false
	// evidence.
	type keywordClaim struct {
		conceptID string
		primary   bool
	}
	conceptIDByKeyword := make(map[string]keywordClaim)
	// existingConceptMembers maps each concept's node ID to the set of
	// member record IDs (collected via inbound instance_of edges). Used
	// by Phase F's member-set overlap gate to suppress emergence of
	// near-duplicate concepts whose evidence sets substantially overlap
	// existing concepts, and by the attachment path (#99) to diff a
	// candidate's members against those already linked.
	existingConceptMembers := make(map[string][]string)

	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		id := n.ID

		// Skip chunks and deleted records (common to all three phases).
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		nodeType, _ := n.Properties.GetString("node_type")
		isConcept := nodeType == "concept"

		contentFull, hasFullContent := n.Properties.GetString("content_full")
		contentShort, hasShort := n.Properties.GetString("content_short")
		_, hasEmbedShort := n.Properties["embedding_short"]

		// Concept-only work: existingConcepts tracking + Quality Rule 1.
		// Rule 1 (label-as-summary) firing is exclusive — when it fires
		// the original `it2` continued without checking Rules 2/3,
		// matching the cleanup intent (replace the placeholder summary
		// in one shot). Rule 1 NOT firing falls through to Rules 2/3
		// below (so a fresh concept with a template content_short still
		// gets flag_embed coverage).
		rule1Fired := false
		if isConcept {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok {
				existingConcepts[kw] = struct{}{}
				// Primary keywords always win keyword->concept
				// resolution (unconditional write; the alias loop
				// below only fills unclaimed slots).
				conceptIDByKeyword[kw] = keywordClaim{conceptID: id, primary: true}
				if contentShort == kw && hasFullContent {
					qualityIssues = append(qualityIssues, qualityIssue{
						nodeID: id,
						fix:    "concept_summary",
						short:  conceptShortSummary(contentFull, 200),
					})
					rule1Fired = true
				}
			}
			// Phase F: also add every keyword in the concept's
			// content_keywords list to existingConcepts. Aliases added
			// by the member-set overlap gate live here, and we want
			// future cycles to short-circuit on them just like primary
			// keywords.
			// cooccurring_keywords (stamped at emergence) marks which
			// content_keywords entries are correlated terms rather than
			// the primary or Phase F aliases. Those still suppress
			// emergence (a "messaging" concept shouldn't fragment off a
			// "kafka" one) but never become attachment claims: records
			// sharing only a co-occurring term are not instances of the
			// concept. Concepts created before this property existed
			// have no marker; their content_keywords claims fall back
			// to the live-population Jaccard gate at attach time.
			cooccurring := make(map[string]struct{})
			if coKws, ok := n.Properties.GetStringList("cooccurring_keywords"); ok {
				for _, kw := range coKws {
					cooccurring[kw] = struct{}{}
				}
			}
			if kws, ok := n.Properties.GetStringList("content_keywords"); ok {
				for _, kw := range kws {
					existingConcepts[kw] = struct{}{}
					if _, isCo := cooccurring[kw]; isCo {
						continue
					}
					// content_keywords also carries co-occurring
					// keywords, a weaker ownership signal than the
					// primary -- never displace an existing claim.
					if _, claimed := conceptIDByKeyword[kw]; !claimed {
						conceptIDByKeyword[kw] = keywordClaim{conceptID: id}
					}
				}
			}
			// Phase F: collect member IDs for this concept (inbound
			// instance_of edges). Cheap walk; concepts typically have
			// 3-20 inbound edges. The candidate-emission loop below
			// computes Jaccard against each concept's member set.
			var members []string
			for _, edge := range g.EdgesTo(id) {
				if edge.Type == "instance_of" {
					members = append(members, edge.SourceID)
				}
			}
			if len(members) > 0 {
				existingConceptMembers[id] = members
			}
		}

		// Non-concept work: manifest stats, lifecycle, orphan detection.
		// Concepts are derived from clusters, not user records, so they
		// don't contribute to TotalRecords / RecordsByType / etc., and
		// they don't get the staleness lifecycle treatment.
		if !isConcept {
			manifest.TotalRecords++

			if kt, ok := n.Properties.GetString("knowledge_type"); ok {
				manifest.RecordsByType[kt]++
			}

			if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
				if manifest.EarliestRecord.IsZero() || ca.Before(manifest.EarliestRecord) {
					manifest.EarliestRecord = ca
				}
				if ca.After(manifest.LatestRecord) {
					manifest.LatestRecord = ca
				}
			}

			if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "captured" {
				manifest.PendingCount++
			}

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

			// Observations are sub-records (always have an observation_of
			// edge to their parent, filtered out of SemanticEdgeCount as
			// structural). Treating them as orphans makes the linking
			// pass below add weak related_to edges against arbitrary
			// similar records every cycle, polluting the graph.
			if nodeType != "observation" {
				ec := nonChunkEdgeCount(g, id)
				if ec == 0 {
					conf, hasConf := n.Properties.GetFloat64("confidence")
					ps, _ := n.Properties.GetString("processing_status")
					if ps != "captured" && (!hasConf || conf >= 0.3) {
						orphanIDs = append(orphanIDs, id)
					}
				}
			}
		}

		// Quality Rules 2 and 3 apply to BOTH concept and non-concept
		// nodes (the original `it2` ran them on every non-deleted,
		// non-chunk node), but skip when Rule 1 already produced a fix
		// for this concept. This preserves the original semantics that
		// a fresh concept with a template content_short and no
		// embedding_short fires flag_embed.
		if rule1Fired {
			continue
		}

		// Rule 2: content_short too short relative to content_full.
		if hasShort && hasFullContent && len(contentShort) < 20 && len(contentFull) > 100 {
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

		// Filter out observation nodes: observations inherit the
		// parent's content_keywords verbatim (curation/observe.go), so
		// LookupKeyword returns BOTH the parent and each of its
		// observation children. Counting both would inflate the
		// concept's evidence_count and add redundant instance_of edges
		// from sub-records. Observations are projections of their
		// parent; the parent is the canonical instance.
		//
		// Filter out concept nodes too: a concept carries its own
		// keyword (plus aliases and co-occurring terms) in
		// content_keywords, so LookupKeyword returns the concept
		// itself. Concepts are derived hubs, not evidence -- counting
		// them inflates Count, and the attachment path (#99) would
		// otherwise add concept-to-concept (or self) instance_of edges.
		rawIDs := e.PropIdx().LookupKeyword("content_keywords", kw)
		ids := rawIDs[:0]
		for _, id := range rawIDs {
			if n, ok := g.GetNode(id); ok {
				if nt, _ := n.Properties.GetString("node_type"); nt == "observation" || nt == "concept" {
					continue
				}
			}
			ids = append(ids, id)
		}
		if len(ids) < threshold {
			continue
		}
		candidates = append(candidates, ConceptCandidate{
			Keyword: kw,
			Count:   len(ids),
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

	// Orphan similarity search: for each orphan, find similar records.
	type orphanLink struct {
		orphanID   string
		targetID   string
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
			// Skip observations as link targets too: they're sub-records
			// and should not appear as the partner end of a related_to
			// edge from a real record.
			if tn, ok := g.GetNode(r.NodeID); ok {
				if nt, _ := tn.Properties.GetString("node_type"); nt == "observation" {
					continue
				}
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

	// Quality issues + existingConcepts are now collected in the single-
	// pass loop at the top of this function (`it`). The previous `it2`
	// and `cnIt` blocks have been folded in.

	var newConcepts []ConceptCandidate
	// newConceptAliases is parallel to newConcepts: extra keywords that
	// candidates with overlapping member sets contributed via Phase F's
	// gate. Folded into the emitted concept's content_keywords by the
	// write phase below.
	var newConceptAliases [][]string
	// aliasMerges captures candidates that overlap an *existing* concept
	// (one already in the store) above the Jaccard threshold. The write
	// phase appends each keyword to that concept's content_keywords.
	type aliasMerge struct {
		conceptID string
		keyword   string
	}
	var aliasMerges []aliasMerge
	// pendingAttach collects, per existing concept, the member records
	// to link via new instance_of edges (#99). Keyed by concept ID and
	// deduped per member because multiple alias keywords can resolve to
	// the same concept. attachTotal bounds the write batch.
	pendingAttach := make(map[string]map[string]struct{})
	attachTotal := 0

	maxNewConcepts := cfg.LLM.Curation.Concept.MaxPerRun // reuse as deterministic budget
	if maxNewConcepts <= 0 {
		maxNewConcepts = 5
	}
	overlapThreshold := cfg.Concepts.MemberOverlapThreshold
	for _, c := range candidates {
		if _, exists := existingConcepts[c.Keyword]; exists {
			// The keyword already has a concept. Pre-#99 this was an
			// unconditional skip, so a record captured after its
			// keyword's concept emerged never got an instance_of edge
			// -- evidence_count could only hold or shrink. Resolve the
			// concept (by primary keyword or alias) and queue any
			// members that aren't linked yet; the write phase adds the
			// missing edges and enrichConcepts recomputes
			// evidence_count from them in the same cycle. c.NodeIDs is
			// already observation- and concept-filtered above.
			claim, ok := conceptIDByKeyword[c.Keyword]
			if !ok {
				// Keyword claimed earlier in this loop by a pending
				// emission or alias merge; no stored concept to
				// attach to yet. The next cycle resolves it.
				continue
			}
			if !claim.primary {
				// The keyword resolved through content_keywords. On
				// concepts stamped with cooccurring_keywords, co-terms
				// never produce claims, so a non-primary claim is a
				// Phase F alias; on older concepts the marker doesn't
				// exist and aliases are indistinguishable from
				// co-occurring "related terms". Either way, gate on
				// live-population overlap -- a genuine alias's records
				// substantially overlap the concept's members; records
				// sharing only a correlated keyword (e.g. tagged
				// "messaging" against a "kafka" concept) must not be
				// attached as false evidence. The gate is best-effort
				// for legacy full-coverage co-terms (trickle arrivals
				// can ratchet past it), which is why new emergences
				// persist provenance instead. A disabled threshold
				// (<= 0) turns non-primary attachment off entirely
				// rather than open.
				if overlapThreshold <= 0 ||
					index.JaccardSimilarity(c.NodeIDs, existingConceptMembers[claim.conceptID]) <= overlapThreshold {
					continue
				}
			}
			conceptID := claim.conceptID
			linked := make(map[string]struct{}, len(existingConceptMembers[conceptID]))
			for _, mid := range existingConceptMembers[conceptID] {
				linked[mid] = struct{}{}
			}
			for _, mid := range c.NodeIDs {
				if attachTotal >= maxConceptAttachPerRun {
					break
				}
				if _, dup := linked[mid]; dup {
					continue
				}
				pending := pendingAttach[conceptID]
				if pending == nil {
					pending = make(map[string]struct{})
					pendingAttach[conceptID] = pending
				}
				if _, dup := pending[mid]; dup {
					continue
				}
				pending[mid] = struct{}{}
				attachTotal++
			}
			continue
		}

		// Phase F: member-set overlap gate. Without this, each
		// content_keyword on a shared evidence set spawns its own
		// concept node (TZ-fragile / parseDateArg / test timezone bugs
		// were three separate concepts about the same bug). Compute
		// Jaccard against both already-stored concepts AND concepts
		// being emitted earlier in this same cycle, and treat
		// high-overlap matches as alias contributions instead of new
		// emissions.
		if overlapThreshold > 0 {
			var bestExistingID string
			bestExistingJ := 0.0
			for conceptID, members := range existingConceptMembers {
				if j := index.JaccardSimilarity(c.NodeIDs, members); j > bestExistingJ {
					bestExistingJ = j
					bestExistingID = conceptID
				}
			}
			bestPendingIdx := -1
			bestPendingJ := 0.0
			for i, nc := range newConcepts {
				if j := index.JaccardSimilarity(c.NodeIDs, nc.NodeIDs); j > bestPendingJ {
					bestPendingJ = j
					bestPendingIdx = i
				}
			}
			if bestExistingJ > overlapThreshold && bestExistingJ >= bestPendingJ {
				aliasMerges = append(aliasMerges, aliasMerge{
					conceptID: bestExistingID,
					keyword:   c.Keyword,
				})
				existingConcepts[c.Keyword] = struct{}{}
				continue
			}
			if bestPendingJ > overlapThreshold {
				newConceptAliases[bestPendingIdx] = append(newConceptAliases[bestPendingIdx], c.Keyword)
				existingConcepts[c.Keyword] = struct{}{}
				continue
			}
		}

		newConcepts = append(newConcepts, c)
		newConceptAliases = append(newConceptAliases, nil)
		existingConcepts[c.Keyword] = struct{}{}
		if len(newConcepts) >= maxNewConcepts {
			break
		}
	}

	// Materialize pendingAttach with sorted member lists so the write
	// phase adds edges in a deterministic order.
	type attachment struct {
		conceptID string
		memberIDs []string
	}
	var attachments []attachment
	for conceptID, memberSet := range pendingAttach {
		members := make([]string, 0, len(memberSet))
		for mid := range memberSet {
			members = append(members, mid)
		}
		sort.Strings(members)
		attachments = append(attachments, attachment{conceptID: conceptID, memberIDs: members})
	}

	e.RUnlock()

	// --- Write phase ---
	// Uses WithWriteBatch so all bbolt index writes inside land in a
	// single transaction (previously unbatched: every SetProp / AddEdge
	// / IndexNode did its own fsync, so a busy cycle with hundreds of
	// mutations serialised hundreds of disk syncs under the write
	// lock). The helper also handles the "only Save if something
	// actually changed" gate and wraps errors with the phase label.
	mutations := len(staleIDs) + len(orphanLinks) + len(qualityIssues) + len(newConcepts) + len(aliasMerges) + len(attachments)
	if mutations > 0 {
		err := e.WithWriteBatch("curation: deterministic", func(ws *core.WriteSession) (bool, error) {

			// Lifecycle transitions: set valid_until on stale records.
			for _, id := range staleIDs {
				if _, ok := ws.Graph().GetNode(id); ok {
					ws.SetProp(id, "valid_until", graph.TimestampProperty(now))
					result.LifecycleTransitions++
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationLifecycle, RecordID: id})
				}
			}

			// Orphan linking.
			for _, ol := range orphanLinks {
				if _, ok := ws.Graph().GetNode(ol.orphanID); !ok {
					continue
				}
				if _, ok := ws.Graph().GetNode(ol.targetID); !ok {
					continue
				}
				_, err := ws.AddEdge(ol.orphanID, ol.targetID, "related_to", ol.similarity, nil)
				if err == nil {
					result.OrphansLinked++
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationLink, RecordID: ol.orphanID})
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationLink, RecordID: ol.targetID})
				}
			}

			// Quality repairs: fix deterministic issues, flag others.
			for _, qi := range qualityIssues {
				if _, ok := ws.Graph().GetNode(qi.nodeID); !ok {
					continue
				}
				switch qi.fix {
				case "concept_summary", "extract_short":
					// Deterministic fix: update content_short.
					ws.SetContentProp(qi.nodeID, "content_short", qi.short)
					result.QualityRepairs++
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationQualityRepair, RecordID: qi.nodeID})
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
			for ci, c := range newConcepts {
				aliases := newConceptAliases[ci]
				// Compute metadata from member records.
				var confSum float64
				var confCount int
				var coKeywords []string
				coKWCounts := make(map[string]int)
				hasSpeculative := false
				allWellEstablished := true

				for _, memberID := range c.NodeIDs {
					mn, ok := ws.Graph().GetNode(memberID)
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

				// Phase F: aliases are keywords from peer candidates that
				// the overlap gate folded into this concept. Surface them
				// in content_keywords so future emergence cycles short
				// circuit on the keyword-already-known check (and so the
				// concept is discoverable via either alias).
				allKeywords := append([]string{c.Keyword}, aliases...)
				allKeywords = append(allKeywords, coKeywords...)
				// Dedup; coKeywords can collide with aliases.
				seen := make(map[string]struct{}, len(allKeywords))
				deduped := allKeywords[:0]
				for _, kw := range allKeywords {
					if _, ok := seen[kw]; ok {
						continue
					}
					seen[kw] = struct{}{}
					deduped = append(deduped, kw)
				}
				allKeywords = deduped

				// Record which content_keywords entries are merely
				// co-occurring stamps (not the primary, not Phase F
				// aliases). The attachment path (#99) reads this to
				// skip them structurally: a co-occurring term's records
				// are correlated with, not instances of, the concept,
				// and the live-population Jaccard gate alone cannot
				// separate a full-coverage co-term from a genuine alias
				// (trickle arrivals ratchet past it one record per
				// cycle). Provenance is known here at write time -- keep
				// it instead of discarding it into the mixed list.
				aliasSet := make(map[string]struct{}, len(aliases)+1)
				aliasSet[c.Keyword] = struct{}{}
				for _, kw := range aliases {
					aliasSet[kw] = struct{}{}
				}
				var cooccurringOnly []string
				for _, kw := range coKeywords {
					if _, isAlias := aliasSet[kw]; !isAlias {
						cooccurringOnly = append(cooccurringOnly, kw)
					}
				}

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
					"author":            graph.StringProperty(NodeAuthor),
				}
				if len(cooccurringOnly) > 0 {
					props["cooccurring_keywords"] = graph.StringListProperty(cooccurringOnly)
				}

				cn := ws.AddNode(props)
				// IndexNode covers property + BM25 + vector. The manual
				// propIdx.Add loop was a pre-IndexNode holdover; dropping
				// it here avoids double-adding each property.
				ws.IndexNode(cn.ID, templateFull, nil)

				ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationConceptEmerge, RecordID: cn.ID})
				for _, memberID := range c.NodeIDs {
					if _, ok := ws.Graph().GetNode(memberID); ok {
						if _, err := ws.AddEdge(memberID, cn.ID, "instance_of", 0.8, nil); err != nil {
							logger.Error("failed to add instance_of edge",
								"component", "curation", "member", memberID, "concept", cn.ID, "err", err)
						} else {
							ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationConceptEmerge, RecordID: memberID})
						}
					}
				}

				result.ConceptsCreated++
			}

			// Phase F: apply alias merges to existing concepts. Each
			// candidate that overlapped an existing concept's member
			// set above the threshold contributed its keyword here;
			// fold all such keywords into the concept's content_keywords
			// (deduped) so search and future emergence cycles see the
			// alias.
			aliasesByConcept := make(map[string][]string)
			for _, m := range aliasMerges {
				aliasesByConcept[m.conceptID] = append(aliasesByConcept[m.conceptID], m.keyword)
			}
			for conceptID, addKws := range aliasesByConcept {
				cn, ok := ws.Graph().GetNode(conceptID)
				if !ok {
					continue
				}
				existing, _ := cn.Properties.GetStringList("content_keywords")
				seen := make(map[string]struct{}, len(existing)+len(addKws))
				for _, kw := range existing {
					seen[kw] = struct{}{}
				}
				merged := append([]string{}, existing...)
				added := false
				for _, kw := range addKws {
					if _, dup := seen[kw]; dup {
						continue
					}
					seen[kw] = struct{}{}
					merged = append(merged, kw)
					added = true
				}
				if added {
					ws.SetProp(conceptID, "content_keywords", graph.StringListProperty(merged))
					result.ConceptsAliased++
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationConceptEnrich, RecordID: conceptID})
					logger.Info("concept alias added",
						"component", "curation",
						"concept", conceptID,
						"added_keywords", addKws)
				}
			}

			// Attach new members to existing concepts (#99). Each
			// attachment carries records that share a concept's keyword
			// (primary or alias) but had no instance_of edge yet --
			// records captured after the concept emerged. Weight
			// matches the emergence path above; enrichConcepts (after
			// this batch) recomputes evidence_count from the new edges
			// in this same cycle.
			for _, att := range attachments {
				if _, ok := ws.Graph().GetNode(att.conceptID); !ok {
					continue
				}
				// Re-diff against inbound edges under the write lock: a
				// caller may have linked a queued member (gramaton_link)
				// between the read phase and this batch. AddEdge does
				// not dedupe and enrichConcepts counts inbound edges,
				// so a duplicate would double-count evidence forever.
				linked := make(map[string]struct{})
				for _, edge := range ws.Graph().EdgesTo(att.conceptID) {
					if edge.Type == "instance_of" {
						linked[edge.SourceID] = struct{}{}
					}
				}
				attached := false
				for _, memberID := range att.memberIDs {
					if _, ok := ws.Graph().GetNode(memberID); !ok {
						continue
					}
					if _, dup := linked[memberID]; dup {
						continue
					}
					if _, err := ws.AddEdge(memberID, att.conceptID, "instance_of", 0.8, nil); err != nil {
						logger.Error("failed to add instance_of edge",
							"component", "curation", "member", memberID, "concept", att.conceptID, "err", err)
						continue
					}
					result.ConceptMembersAttached++
					attached = true
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationConceptEnrich, RecordID: memberID})
				}
				if attached {
					ws.AddAction(graph.CommitAction{Kind: graph.ActionCurationConceptEnrich, RecordID: att.conceptID})
				}
			}

			changed := result.LifecycleTransitions + result.OrphansLinked + result.QualityRepairs + result.ConceptsCreated + result.ConceptsAliased + result.ConceptMembersAttached
			return changed > 0, nil
		})
		if err != nil {
			logger.Error("deterministic write batch failed",
				"component", "curation", "err", err)
		}
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
			"observations_created", result.ObservationsCreated,
			"quality_repairs", result.QualityRepairs,
			"quality_flags", result.QualityFlags,
			"gc_collected", result.GCCollected,
			"concepts_created", result.ConceptsCreated,
			"concepts_aliased", result.ConceptsAliased,
			"concept_members_attached", result.ConceptMembersAttached,
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

		// Temporality: allow unset or ephemeral. The previous
		// `temp != "ephemeral"` requirement filtered to ~0 matches in
		// practice — the captured filter above means the record has
		// not been classified yet, so temporality is always unset (LLM
		// classification is what assigns it). Treating unset+ephemeral
		// as the GC-eligible band lets aged-out unclassified debris
		// actually reach deletion.
		temp, _ := n.Properties.GetString("temporality")
		if temp != "" && temp != "ephemeral" {
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
	var gcActions []graph.CommitAction
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
		gcActions = append(gcActions, graph.CommitAction{
			Kind: graph.ActionCurationGC, RecordID: id,
		})
	}
	if deleted > 0 {
		e.SaveOrLog("curation: garbage collection", gcActions...)
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
		// Observations are excluded: they're sub-records of their
		// parent and the parent is the canonical evidence; counting
		// both inflates evidence_count by the per-parent observation
		// fan-out.
		inbound := e.Graph().EdgesTo(id)
		count := 0
		var latestEvidence time.Time
		for _, edge := range inbound {
			if graph.IsStructuralEdge(edge.Type) {
				continue
			}
			src, ok := e.Graph().GetNode(edge.SourceID)
			if !ok {
				continue
			}
			if nt, _ := src.Properties.GetString("node_type"); nt == "observation" {
				continue
			}
			count++
			if ca, ok := src.Properties.GetTimestamp("created_at"); ok {
				if ca.After(latestEvidence) {
					latestEvidence = ca
				}
			}
		}

		// Check if update is needed. Pre-fix this used `count > 0` as
		// a fall-through trigger which always fired once a concept had
		// any inbound edge — so every concept with evidence got
		// re-written every cycle, producing a hot write loop with no
		// real change. Post-fix: only update when evidence_count
		// actually changed OR last_evidence_at drifted (new edge from
		// a source whose created_at exceeds the stored timestamp).
		existingCount, _ := n.Properties.GetInt64("evidence_count")
		existingLatest, _ := n.Properties.GetTimestamp("last_evidence_at")
		countChanged := int64(count) != existingCount
		latestChanged := !latestEvidence.IsZero() && latestEvidence.After(existingLatest)
		if countChanged || latestChanged {
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
	var enrichActions []graph.CommitAction
	for _, u := range updates {
		if _, ok := e.Graph().GetNode(u.id); !ok {
			continue
		}
		e.SetProp(u.id, "evidence_count", graph.Int64Property(int64(u.evidenceCount)))
		if !u.lastEvidenceAt.IsZero() {
			e.SetProp(u.id, "last_evidence_at", graph.TimestampProperty(u.lastEvidenceAt))
		}
		changed = true
		enrichActions = append(enrichActions, graph.CommitAction{
			Kind: graph.ActionCurationConceptEnrich, RecordID: u.id,
		})
	}
	if changed {
		e.SaveOrLog("curation: concept enrichment", enrichActions...)
	}
	e.Unlock()

	if len(updates) > 0 {
		logger.Info("concept enrichment complete",
			"component", "curation",
			"concepts_updated", len(updates))
	}
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
	var sectionActions []graph.CommitAction
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
			sectionActions = append(sectionActions,
				graph.CommitAction{Kind: graph.ActionCurationSectionLink, RecordID: c.sourceID},
				graph.CommitAction{Kind: graph.ActionCurationSectionLink, RecordID: c.targetID},
			)
		}
	}

	if linked > 0 {
		e.SaveOrLog("curation: section linking", sectionActions...)
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
// generic, or is a meta-term that doesn't represent a real concept.
func isWeakConceptKeyword(kw string) bool {
	if len(kw) < 3 {
		return true
	}
	// Meta-terms that describe the source, not the content.
	weak := map[string]bool{
		// Source/structure meta-terms.
		"article": true, "section": true, "overview": true,
		"summary": true, "reference": true, "document": true,
		"note": true, "notes": true, "todo": true,
		// Generic LLM/agent vocabulary that appears across nearly
		// every record without distinguishing concepts. Pre-fix these
		// were leaking into concept clusters and producing muddled
		// "context"-themed concepts.
		"context": true, "content": true, "system": true,
	}
	return weak[strings.ToLower(kw)]
}
