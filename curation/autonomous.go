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
func RunAutonomous(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, logger, false)
}

// RunAutonomousDryRun is like RunAutonomous but does not apply changes.
// The LLM is still called so you can see what would be classified.
func RunAutonomousDryRun(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, logger, true)
}

func runAutonomousInner(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger, dryRun bool) *AutonomousResult {
	result := &AutonomousResult{DryRun: dryRun}
	maxCalls := cfg.LLMCuration.MaxCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 20
	}

	classifyPending(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	generateSummaries(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
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

	// Process each record: LLM calls outside lock, then batch write.
	type classified struct {
		id      string
		content string
		data    *classificationResult
	}
	var ready []classified

	for _, rec := range batch {
		if result.LLMCalls >= maxCalls {
			break
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		prompt := fmt.Sprintf(classifyPrompt, rec.content)
		resp, err := llmProv.Complete(ctx, prompt)
		result.LLMCalls++

		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("classify LLM error", "component", "curation", "record", rec.id[:12], "err", err)
			}
			continue
		}

		classification, err := parseClassification(resp)
		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("classify parse error", "component", "curation", "record", rec.id[:12], "err", err)
			}
			continue
		}

		ready = append(ready, classified{id: rec.id, content: rec.content, data: classification})
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
			e.SetProp(r.id, "content_short", graph.StringProperty(r.data.SummaryShort))
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

	// Read phase: find records with content but no summary.
	e.RLock()
	allIDs := e.Graph().AllNodeIDs()
	type needsSummary struct {
		id      string
		content string
	}
	var batch []needsSummary
	for _, id := range allIDs {
		if len(batch) >= batchSize {
			break
		}
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		if isChunkNode(e.Graph(), id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}
		// Only process records that have content but no summary.
		content, hasContent := n.Properties.GetString("content_full")
		_, hasSummary := n.Properties.GetString("content_short")
		if hasContent && !hasSummary && content != "" {
			batch = append(batch, needsSummary{id: id, content: content})
		}
	}
	e.RUnlock()

	type summarized struct {
		id      string
		content string
		summary string
	}
	var readySummaries []summarized

	for _, rec := range batch {
		if result.LLMCalls >= maxCalls {
			break
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		prompt := fmt.Sprintf(summarizePrompt, rec.content)
		resp, err := llmProv.Complete(ctx, prompt)
		result.LLMCalls++

		if err != nil {
			result.Errors++
			if logger != nil {
				logger.Warn("summarize LLM error", "component", "curation", "record", rec.id[:12], "err", err)
			}
			continue
		}

		summary := strings.TrimSpace(resp)
		// Rune-safe truncation.
		runes := []rune(summary)
		if len(runes) > 200 {
			summary = string(runes[:200])
		}
		if summary == "" {
			result.Errors++
			continue
		}

		readySummaries = append(readySummaries, summarized{id: rec.id, content: rec.content, summary: summary})
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
		e.SetProp(s.id, "content_short", graph.StringProperty(s.summary))
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
			e.Graph().AddEdge(f.idB, f.idA, "supersedes", f.confidence, nil)
			e.SetProp(f.idA, "valid_until", graph.TimestampProperty(time.Now().UTC()))
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
