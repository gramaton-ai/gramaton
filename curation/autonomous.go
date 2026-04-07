package curation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/llm"
)

// AutonomousResult summarizes what an LLM curation cycle did.
type AutonomousResult struct {
	Classified              int              `json:"classified"`
	SummariesGenerated      int              `json:"summaries_generated"`
	ConceptsCreated         int              `json:"concepts_created"`
	ContradictionsDetected  int              `json:"contradictions_detected"`
	ManifestSummary         string           `json:"manifest_summary,omitempty"`
	Errors                  int              `json:"errors"`
	LLMCalls                int              `json:"llm_calls"`
	DryRun                  bool             `json:"dry_run,omitempty"`
	PlannedChanges          []PlannedChange  `json:"planned_changes,omitempty"`
}

// PlannedChange describes a change that autonomous curation would make.
// Populated in dry-run mode instead of applying the change.
type PlannedChange struct {
	RecordID    string `json:"record_id"`
	Action      string `json:"action"`       // "classify" or "summarize"
	ContentSnip string `json:"content_snip"` // first 100 chars of content
	Details     any    `json:"details"`      // classification or summary text
}

// RunAutonomous performs LLM-powered curation tasks.
// Caller must NOT hold any lock. When dryRun is true, LLM calls are
// made and results returned in PlannedChanges but no mutations are applied.
// conceptCandidates from the deterministic layer are used to create concept nodes.
func RunAutonomous(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger, conceptCandidates []ConceptCandidate) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, logger, false, conceptCandidates)
}

// RunAutonomousDryRun is like RunAutonomous but does not apply changes.
// The LLM is still called so you can see what would be classified.
func RunAutonomousDryRun(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, logger, true, nil)
}

func runAutonomousInner(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger, dryRun bool, conceptCandidates []ConceptCandidate) *AutonomousResult {
	result := &AutonomousResult{DryRun: dryRun}
	maxCalls := cfg.LLMCuration.MaxCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 20
	}

	classifyPending(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	generateSummaries(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	createConceptNodes(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun, conceptCandidates)
	detectContradictions(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)

	// Generate manifest qualitative summary if we have a manifest from
	// the last deterministic run and haven't used too many LLM calls.
	if !dryRun && result.LLMCalls < maxCalls {
		generateManifestSummary(ctx, e, llmProv, result, logger)
	}

	if logger != nil && (result.Classified+result.SummariesGenerated+result.ConceptsCreated) > 0 {
		logger.Info("autonomous curation complete",
			"component", "curation",
			"classified", result.Classified,
			"summaries", result.SummariesGenerated,
			"concepts", result.ConceptsCreated,
			"errors", result.Errors,
			"llm_calls", result.LLMCalls)
	}

	return result
}

// classifyPending classifies records with processing_status="captured".
func classifyPending(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	batchSize := cfg.LLMCuration.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// Read phase: gather pending record IDs and content.
	e.RLock()
	pendingIDs := e.PropIdx().Lookup("processing_status", graph.StringProperty("captured"))
	type pending struct {
		id      string
		content string
	}
	var batch []pending
	for _, id := range pendingIDs {
		if len(batch) >= batchSize {
			break
		}
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		content, ok := n.Properties.GetString("content_full")
		if !ok || content == "" {
			continue
		}
		batch = append(batch, pending{id: id, content: content})
	}
	e.RUnlock()

	// Process records: parallel LLM calls outside lock, then batch write.
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Cap batch to remaining LLM budget.
	remaining := maxCalls - result.LLMCalls
	if remaining <= 0 {
		return
	}
	if len(batch) > remaining {
		batch = batch[:remaining]
	}

	type classified struct {
		id      string
		content string
		data    *classificationResult
	}
	var ready []classified

	work := make([]llmWork, len(batch))
	for i, rec := range batch {
		work[i] = llmWork{id: rec.id, prompt: fmt.Sprintf(classifyPrompt, rec.content)}
	}

	llmResults := parallelLLM(ctx, llmProv, work, 4)
	result.LLMCalls += len(llmResults)

	for i, lr := range llmResults {
		if lr.err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("classify LLM error", "component", "curation", "record", batch[i].id[:12], "err", lr.err)
			}
			continue
		}

		classification, err := parseClassification(lr.response)
		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("classify parse error", "component", "curation", "record", batch[i].id[:12], "err", err)
			}
			continue
		}

		ready = append(ready, classified{id: batch[i].id, content: batch[i].content, data: classification})
	}

	if len(ready) == 0 {
		return
	}

	// In dry-run mode, record planned changes but don't apply them.
	if dryRun {
		for _, r := range ready {
			snip := r.content
			if len(snip) > 100 {
				snip = snip[:100]
			}
			result.PlannedChanges = append(result.PlannedChanges, PlannedChange{
				RecordID:    r.id,
				Action:      "classify",
				ContentSnip: snip,
				Details:     r.data,
			})
			result.Classified++
		}
		return
	}

	// Batch write: one lock acquisition, one commit.
	e.Lock()
	for _, r := range ready {
		if _, ok := e.Graph().GetNode(r.id); !ok {
			if logger != nil {
				logger.Debug("classify node gone", "component", "curation", "record", r.id[:12])
			}
			continue
		}
		if r.data.Temporality != "" {
			e.SetProp(r.id, "temporality", graph.StringProperty(r.data.Temporality))
		}
		if r.data.Confidence > 0 {
			e.SetProp(r.id, "confidence", graph.Float64Property(r.data.Confidence))
		}
		if r.data.KnowledgeType != "" {
			e.SetProp(r.id, "knowledge_type", graph.StringProperty(r.data.KnowledgeType))
		}
		if r.data.EpistemicStatus != "" {
			e.SetProp(r.id, "epistemic_status", graph.StringProperty(r.data.EpistemicStatus))
		}
		if len(r.data.Keywords) > 0 {
			e.SetProp(r.id, "content_keywords", graph.StringListProperty(r.data.Keywords))
		}
		if r.data.SummaryShort != "" {
			e.SetContentProp(r.id, "content_short", r.data.SummaryShort)
		}
		e.SetProp(r.id, "processing_status", graph.StringProperty("processed"))
		result.Classified++
	}
	if result.Classified > 0 {
		e.Save("curation: classify")
	}
	e.Unlock()
}

// generateSummaries adds summary_short to records that lack one.
func generateSummaries(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	batchSize := cfg.LLMCuration.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// Read phase: find records needing summaries.
	// Priority 1: records with content but no summary at all.
	// Priority 2: section nodes with truncated summaries (no heading).
	e.RLock()
	g := e.Graph()
	allIDs := g.AllNodeIDs()
	type needsSummary struct {
		id      string
		content string
	}
	var batch []needsSummary
	for _, id := range allIDs {
		if len(batch) >= batchSize {
			break
		}
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}
		content, hasContent := n.Properties.GetString("content_full")
		_, hasSummary := n.Properties.GetString("content_short")
		if hasContent && !hasSummary && content != "" {
			batch = append(batch, needsSummary{id: id, content: content})
		}
	}

	// Priority 2: section nodes with truncated summaries.
	// A truncated summary is one that equals the first 200 chars of content
	// (set by the section splitter when no heading was detected).
	for _, id := range allIDs {
		if len(batch) >= batchSize {
			break
		}
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		// Only section nodes.
		isSection := false
		for _, edge := range g.EdgesFrom(id) {
			if edge.Type == "section_of" {
				isSection = true
				break
			}
		}
		if !isSection {
			continue
		}

		content, hasContent := n.Properties.GetString("content_full")
		summary, hasSummary := n.Properties.GetString("content_short")
		if !hasContent || !hasSummary || content == "" {
			continue
		}

		// Check if summary looks truncated: it's the first 200 chars of content.
		if len(summary) >= 150 && len(content) > len(summary) && strings.HasPrefix(content, summary) {
			batch = append(batch, needsSummary{id: id, content: content})
		}
	}
	e.RUnlock()

	// Cap batch to remaining LLM budget.
	remaining := maxCalls - result.LLMCalls
	if remaining <= 0 {
		return
	}
	if len(batch) > remaining {
		batch = batch[:remaining]
	}

	type summarized struct {
		id      string
		content string
		summary string
	}
	var readySummaries []summarized

	work := make([]llmWork, len(batch))
	for i, rec := range batch {
		work[i] = llmWork{id: rec.id, prompt: fmt.Sprintf(summarizePrompt, rec.content)}
	}

	llmResults := parallelLLM(ctx, llmProv, work, 4)
	result.LLMCalls += len(llmResults)

	for i, lr := range llmResults {
		if lr.err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("summarize LLM error", "component", "curation", "record", batch[i].id[:12], "err", lr.err)
			}
			continue
		}

		summary := strings.TrimSpace(lr.response)
		runes := []rune(summary)
		if len(runes) > 200 {
			summary = string(runes[:200])
		}
		if summary == "" {
			result.Errors++
			continue
		}

		readySummaries = append(readySummaries, summarized{id: batch[i].id, content: batch[i].content, summary: summary})
	}

	if len(readySummaries) == 0 {
		return
	}

	if dryRun {
		for _, s := range readySummaries {
			snip := s.content
			if len(snip) > 100 {
				snip = snip[:100]
			}
			result.PlannedChanges = append(result.PlannedChanges, PlannedChange{
				RecordID:    s.id,
				Action:      "summarize",
				ContentSnip: snip,
				Details:     s.summary,
			})
			result.SummariesGenerated++
		}
		return
	}

	e.Lock()
	for _, s := range readySummaries {
		if _, ok := e.Graph().GetNode(s.id); !ok {
			if logger != nil {
				logger.Debug("summarize node gone", "component", "curation", "record", s.id[:12])
			}
			continue
		}
		e.SetContentProp(s.id, "content_short", s.summary)
		result.SummariesGenerated++
	}
	if result.SummariesGenerated > 0 {
		e.Save("curation: summarize")
	}
	e.Unlock()
}

// generateManifestSummary creates a qualitative summary of the store's
// strengths and gaps using the LLM. The summary is stored on
// AutonomousResult.ManifestSummary for the runner to apply.
func generateManifestSummary(ctx context.Context, e *core.Engine, llmProv llm.Provider, result *AutonomousResult, logger *slog.Logger) {
	// Gather lightweight stats under RLock.
	e.RLock()
	totalRecords := 0
	typeMap := make(map[string]int)
	kwCounts := e.PropIdx().KeywordCounts("content_keywords")
	var earliest, latest time.Time

	for _, id := range e.Graph().AllNodeIDs() {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && (ps == "deleted" || ps == "captured") {
			continue
		}
		if isChunkNode(e.Graph(), id) {
			continue
		}
		totalRecords++
		if kt, ok := n.Properties.GetString("knowledge_type"); ok {
			typeMap[kt]++
		}
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			if earliest.IsZero() || ca.Before(earliest) {
				earliest = ca
			}
			if ca.After(latest) {
				latest = ca
			}
		}
	}
	e.RUnlock()

	if totalRecords < 5 {
		// Not enough records for a meaningful summary.
		return
	}

	// Build top keywords string.
	type kwEntry struct {
		kw    string
		count int
	}
	var kwList []kwEntry
	for kw, count := range kwCounts {
		kwList = append(kwList, kwEntry{kw, count})
	}
	// Simple sort by count descending.
	for i := 0; i < len(kwList); i++ {
		for j := i + 1; j < len(kwList); j++ {
			if kwList[j].count > kwList[i].count {
				kwList[i], kwList[j] = kwList[j], kwList[i]
			}
		}
	}
	topN := 15
	if len(kwList) < topN {
		topN = len(kwList)
	}
	kwStrs := make([]string, topN)
	for i := 0; i < topN; i++ {
		kwStrs[i] = fmt.Sprintf("%s(%d)", kwList[i].kw, kwList[i].count)
	}

	// Build types string.
	var typeStrs []string
	for kt, count := range typeMap {
		typeStrs = append(typeStrs, fmt.Sprintf("%s(%d)", kt, count))
	}

	earliestStr := "N/A"
	latestStr := "N/A"
	if !earliest.IsZero() {
		earliestStr = earliest.Format("2006-01-02")
	}
	if !latest.IsZero() {
		latestStr = latest.Format("2006-01-02")
	}

	prompt := fmt.Sprintf(manifestSummaryPrompt,
		totalRecords,
		strings.Join(typeStrs, ", "),
		strings.Join(kwStrs, ", "),
		earliestStr, latestStr,
	)

	resp, err := llmProv.Complete(ctx, prompt)
	result.LLMCalls++
	if err != nil {
		result.Errors++
		if logger != nil {
			logger.Warn("manifest summary LLM error", "component", "curation", "err", err)
		}
		return
	}

	summary := strings.TrimSpace(resp)
	// Rune-safe truncation to 500 characters.
	runes := []rune(summary)
	if len(runes) > 500 {
		summary = string(runes[:500])
	}
	result.ManifestSummary = summary
}

// createConceptNodes converts concept candidates into searchable concept
// nodes with LLM-generated summaries. Each concept node links to its
// constituent records, acting as a retrieval hub (RAPTOR-inspired).
func createConceptNodes(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool, candidates []ConceptCandidate) {
	if len(candidates) == 0 || result.LLMCalls >= maxCalls {
		return
	}

	maxConcepts := cfg.LLMCuration.MaxConceptsPerRun
	if maxConcepts <= 0 {
		maxConcepts = 5
	}

	// Filter out candidates that already have a concept node.
	e.RLock()
	g := e.Graph()

	// Find existing concept nodes by checking node_type.
	existingConcepts := make(map[string]struct{})
	for _, id := range g.AllNodeIDs() {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			if kw, ok := n.Properties.GetString("concept_keyword"); ok {
				existingConcepts[kw] = struct{}{}
			}
		}
	}

	// Filter and sort candidates by count (most connected first).
	var eligible []ConceptCandidate
	for _, c := range candidates {
		if _, exists := existingConcepts[c.Keyword]; exists {
			continue
		}
		eligible = append(eligible, c)
	}
	e.RUnlock()

	if len(eligible) == 0 {
		return
	}
	if len(eligible) > maxConcepts {
		eligible = eligible[:maxConcepts]
	}

	for _, candidate := range eligible {
		if result.LLMCalls >= maxCalls {
			break
		}

		// Gather summaries from member records.
		e.RLock()
		var memberSummaries []string
		for _, id := range candidate.NodeIDs {
			n, ok := g.GetNode(id)
			if !ok {
				continue
			}
			if s, ok := n.Properties.GetString("content_short"); ok && s != "" {
				memberSummaries = append(memberSummaries, "- "+s)
			} else if s, ok := n.Properties.GetString("content_full"); ok && len(s) > 200 {
				memberSummaries = append(memberSummaries, "- "+s[:200])
			} else if s, ok := n.Properties.GetString("content_full"); ok {
				memberSummaries = append(memberSummaries, "- "+s)
			}
		}
		e.RUnlock()

		if len(memberSummaries) == 0 {
			continue
		}

		// Cap member summaries to avoid exceeding context.
		if len(memberSummaries) > 20 {
			memberSummaries = memberSummaries[:20]
		}

		summaryText := ""
		for _, s := range memberSummaries {
			summaryText += s + "\n"
		}

		// LLM call to synthesize.
		prompt := fmt.Sprintf(conceptSynthesisPrompt,
			candidate.Keyword, candidate.Count, summaryText)

		synthesis, err := llmProv.Complete(ctx, prompt)
		result.LLMCalls++
		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("concept synthesis failed",
					"component", "curation",
					"keyword", candidate.Keyword,
					"err", err)
			}
			continue
		}

		// Truncate synthesis.
		runes := []rune(synthesis)
		if len(runes) > 500 {
			synthesis = string(runes[:500])
		}

		if dryRun {
			result.PlannedChanges = append(result.PlannedChanges, PlannedChange{
				Action:      "create_concept",
				ContentSnip: candidate.Keyword,
				Details:     synthesis,
			})
			result.ConceptsCreated++
			continue
		}

		// Build content_short from synthesis: first sentence, capped
		// at 200 chars. The keyword is preserved in concept_keyword.
		shortSummary := conceptShortSummary(synthesis, 200)

		// Pre-embed outside the lock (I/O can be slow).
		// Embed both content_full (synthesis) and content_short
		// to populate embedding_full and embedding_short.
		var conceptVecFull, conceptVecShort []float32
		var conceptModel string
		if e.Embedder() != nil {
			texts := []string{synthesis}
			if shortSummary != synthesis {
				texts = append(texts, shortSummary)
			}
			vecs, err := e.Embedder().Embed(ctx, texts)
			if err == nil && len(vecs) > 0 {
				conceptVecFull = vecs[0]
				conceptModel = e.Embedder().ModelID()
				if len(vecs) > 1 {
					conceptVecShort = vecs[1]
				}
			}
		}

		// Create concept node under write lock.
		e.Lock()

		props := graph.Properties{
			"content_full":      graph.StringProperty(synthesis),
			"content_short":     graph.StringProperty(shortSummary),
			"node_type":         graph.StringProperty("concept"),
			"concept_keyword":   graph.StringProperty(candidate.Keyword),
			"knowledge_type":    graph.StringProperty("conceptual"),
			"temporality":       graph.StringProperty("durable"),
			"confidence":        graph.Float64Property(0.7),
			"epistemic_status":  graph.StringProperty("probable"),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
			"access_count":      graph.Int64Property(0),
			"evidence_count":    graph.Int64Property(int64(candidate.Count)),
		}

		node := e.Graph().AddNode(props)
		e.IndexNode(node.ID, synthesis, conceptVecFull)

		// Apply embedding_short if we generated it.
		if conceptVecShort != nil {
			e.Graph().SetNodeProperty(node.ID, "embedding_short", graph.VectorProperty(conceptVecShort))
			e.PropIdx().Add(node.ID, "embedding_short", graph.VectorProperty(conceptVecShort))
		}

		// Set embedding model property if we embedded.
		if conceptVecFull != nil && conceptModel != "" {
			e.SetProp(node.ID, "embedding_model", graph.StringProperty(conceptModel))
		}

		// Create instance_of edges from member records to concept node.
		for _, memberID := range candidate.NodeIDs {
			if _, ok := e.Graph().GetNode(memberID); ok {
				e.Graph().AddEdge(memberID, node.ID, "instance_of", 0.8, nil)
			}
		}

		e.Save("curation: concept node")
		e.Unlock()

		result.ConceptsCreated++

		if logger != nil {
			logger.Info("concept node created",
				"component", "curation",
				"keyword", candidate.Keyword,
				"members", candidate.Count,
				"node_id", node.ID)
		}
	}
}

// conceptShortSummary extracts a short summary from a synthesis text.
// Takes the first sentence, capped at maxLen characters.
func conceptShortSummary(synthesis string, maxLen int) string {
	// Find first sentence boundary.
	for i, r := range synthesis {
		if r == '.' && i > 20 && i < maxLen {
			return synthesis[:i+1]
		}
	}
	// No sentence boundary found within limit; truncate at word boundary.
	if len(synthesis) <= maxLen {
		return synthesis
	}
	// Find last space before maxLen.
	cut := maxLen
	for cut > 0 && synthesis[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = maxLen
	}
	return synthesis[:cut]
}

// detectContradictions finds records with moderate similarity and uses the
// LLM to determine if they contradict or supersede each other.
func detectContradictions(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	maxChecks := cfg.LLMCuration.MaxContradictionChecks
	if maxChecks <= 0 {
		maxChecks = 5
	}
	minSim := cfg.LLMCuration.ContradictionMinSim
	if minSim <= 0 {
		minSim = 0.5
	}
	maxSim := cfg.LLMCuration.ContradictionMaxSim
	if maxSim <= 0 {
		maxSim = 0.85
	}

	// Read phase: find recently processed records and their similar neighbors.
	type candidate struct {
		idA, idB           string
		contentA, contentB string
	}
	var candidates []candidate

	e.RLock()
	processedIDs := e.PropIdx().Lookup("processing_status", graph.StringProperty("processed"))
	seen := make(map[string]bool)

	for _, idA := range processedIDs {
		if len(candidates) >= maxChecks {
			break
		}
		nA, ok := e.Graph().GetNode(idA)
		if !ok {
			continue
		}
		contentA, ok := nA.Properties.GetString("content_full")
		if !ok || contentA == "" {
			continue
		}
		if isChunkNode(e.Graph(), idA) {
			continue
		}

		// Use the node's own embedding to find similar records.
		emb, ok := nA.Properties.GetVector("embedding_full")
		if !ok {
			continue
		}
		results := e.VecIdx().Search(emb, 6, nil) // 6 to account for self

		for _, sr := range results {
			if sr.NodeID == idA {
				continue
			}
			sim := float64(sr.Similarity)
			if sim < minSim || sim >= maxSim {
				continue
			}
			// Deduplicate pairs.
			pairKey := idA + ":" + sr.NodeID
			pairKeyRev := sr.NodeID + ":" + idA
			if seen[pairKey] || seen[pairKeyRev] {
				continue
			}
			seen[pairKey] = true

			// Check if they already have an edge between them.
			hasEdge := false
			for _, edge := range e.Graph().EdgesFrom(idA) {
				if edge.TargetID == sr.NodeID {
					hasEdge = true
					break
				}
			}
			if hasEdge {
				continue
			}

			nB, ok := e.Graph().GetNode(sr.NodeID)
			if !ok {
				continue
			}
			contentB, ok := nB.Properties.GetString("content_full")
			if !ok || contentB == "" {
				continue
			}

			candidates = append(candidates, candidate{
				idA: idA, idB: sr.NodeID,
				contentA: contentA, contentB: contentB,
			})
			if len(candidates) >= maxChecks {
				break
			}
		}
	}
	e.RUnlock()

	if len(candidates) == 0 {
		return
	}

	// LLM phase: ask about each pair.
	type detected struct {
		idA, idB     string
		contentA     string
		relationship string
		confidence   float64
		explanation  string
	}
	var findings []detected

	for _, c := range candidates {
		if result.LLMCalls >= maxCalls {
			break
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		prompt := fmt.Sprintf(contradictionPrompt, c.contentA, c.contentB)
		resp, err := llmProv.Complete(ctx, prompt)
		result.LLMCalls++

		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("contradiction LLM error", "component", "curation", "err", err)
			}
			continue
		}

		cr, err := parseContradictionResult(resp)
		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("contradiction parse error", "component", "curation", "err", err)
			}
			continue
		}

		if cr.Relationship == "contradicts" || cr.Relationship == "supersedes" {
			findings = append(findings, detected{
				idA: c.idA, idB: c.idB, contentA: c.contentA,
				relationship: cr.Relationship,
				confidence:   cr.Confidence,
				explanation:  cr.Explanation,
			})
		}
	}

	if len(findings) == 0 {
		return
	}

	if dryRun {
		for _, f := range findings {
			snip := f.contentA
			if len(snip) > 100 {
				snip = snip[:100]
			}
			result.PlannedChanges = append(result.PlannedChanges, PlannedChange{
				RecordID:    f.idA,
				Action:      f.relationship,
				ContentSnip: snip,
				Details: map[string]any{
					"target_id":   f.idB,
					"confidence":  f.confidence,
					"explanation": f.explanation,
				},
			})
			result.ContradictionsDetected++
		}
		return
	}

	// Write phase: create edges and mark superseded records.
	e.Lock()
	for _, f := range findings {
		if _, ok := e.Graph().GetNode(f.idA); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(f.idB); !ok {
			continue
		}

		switch f.relationship {
		case "contradicts":
			e.Graph().AddEdge(f.idA, f.idB, f.relationship, f.confidence, nil)
			e.Graph().AddEdge(f.idB, f.idA, f.relationship, f.confidence, nil)
			result.ContradictionsDetected++

		case "supersedes":
			// B supersedes A: A is older, B is the replacement.
			now := time.Now().UTC()
			e.Graph().AddEdge(f.idB, f.idA, "supersedes", f.confidence, nil)
			e.SetProp(f.idA, "valid_until", graph.TimestampProperty(now))
			e.SetProp(f.idA, "resolution", graph.StringProperty("superseded"))
			e.SetProp(f.idA, "resolved_at", graph.TimestampProperty(now))
			result.ContradictionsDetected++
		}
	}
	if result.ContradictionsDetected > 0 {
		e.Save("curation: contradictions")
	}
	e.Unlock()

	if logger != nil && result.ContradictionsDetected > 0 {
		logger.Info("contradiction detection complete",
			"component", "curation",
			"detected", result.ContradictionsDetected)
	}
}

// contradictionResult is the parsed LLM contradiction analysis.
type contradictionResult struct {
	Relationship string  `json:"relationship"`
	Confidence   float64 `json:"confidence"`
	Explanation  string  `json:"explanation"`
}

// parseContradictionResult extracts the contradiction analysis from LLM response.
func parseContradictionResult(resp string) (*contradictionResult, error) {
	resp = strings.TrimSpace(resp)

	// Strip markdown code fences if present.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		resp = strings.Join(jsonLines, "\n")
	}

	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start >= 0 && end > start {
		resp = resp[start : end+1]
	}

	var result contradictionResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("parse contradiction JSON: %w", err)
	}

	result.Relationship = validateEnum(result.Relationship, []string{"contradicts", "supersedes", "related", "none"})
	if result.Relationship == "" {
		result.Relationship = "none"
	}

	if result.Confidence < 0 || result.Confidence > 1 {
		result.Confidence = 0.5
	}

	return &result, nil
}

// classificationResult is the parsed LLM classification output.
type classificationResult struct {
	Temporality     string   `json:"temporality"`
	Confidence      float64  `json:"confidence"`
	KnowledgeType   string   `json:"knowledge_type"`
	EpistemicStatus string   `json:"epistemic_status"`
	Keywords        []string `json:"keywords"`
	SummaryShort    string   `json:"summary_short"`
}

// parseClassification extracts JSON from an LLM response. Handles
// responses that include markdown code fences around the JSON.
func parseClassification(resp string) (*classificationResult, error) {
	resp = strings.TrimSpace(resp)

	// Strip markdown code fences if present.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		resp = strings.Join(jsonLines, "\n")
	}

	// Try to find JSON object in the response.
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start >= 0 && end > start {
		resp = resp[start : end+1]
	}

	var result classificationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("parse classification JSON: %w", err)
	}

	// Validate enum values.
	result.Temporality = validateEnum(result.Temporality, []string{"immutable", "durable", "temporal", "ephemeral"})
	result.KnowledgeType = validateEnum(result.KnowledgeType, []string{"episodic", "semantic", "procedural", "conceptual", "reference"})
	result.EpistemicStatus = validateEnum(result.EpistemicStatus, []string{"well_established", "probable", "speculative", "contested", "refuted"})

	if result.Confidence < 0 || result.Confidence > 1 {
		result.Confidence = 0.5
	}

	// Validate keywords: cap count and individual length.
	if len(result.Keywords) > 100 {
		result.Keywords = result.Keywords[:100]
	}
	for i, kw := range result.Keywords {
		if len(kw) > 100 {
			result.Keywords[i] = kw[:100]
		}
	}

	// Rune-safe summary truncation.
	if len(result.SummaryShort) > 200 {
		runes := []rune(result.SummaryShort)
		if len(runes) > 200 {
			result.SummaryShort = string(runes[:200])
		}
	}

	return &result, nil
}

func validateEnum(val string, allowed []string) string {
	for _, a := range allowed {
		if val == a {
			return val
		}
	}
	return ""
}
