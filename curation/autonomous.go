package curation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// AutonomousResult summarizes what an LLM curation cycle did.
type AutonomousResult struct {
	Classified             int                             `json:"classified"`
	SummariesGenerated     int                             `json:"summaries_generated"`
	ConceptsCreated        int                             `json:"concepts_created"`
	ContradictionsDetected int                             `json:"contradictions_detected"`
	ManifestSummary        string                          `json:"manifest_summary,omitempty"`
	ManifestCacheHit       bool                            `json:"manifest_cache_hit,omitempty"` // true when manifest summary was reused from prior-cycle cache
	Errors                 int                             `json:"errors"`
	LLMCalls               int                             `json:"llm_calls"`
	ModelCounts            map[string]int                  `json:"model_counts,omitempty"` // per-model classification counts

	// Cycle-level token usage accounting. TokenUsage is the sum across
	// all tasks; TokenUsageByTask breaks it down. Populated at the end
	// of runAutonomousInner from the cycle-scoped telemetry recorder.
	TokenUsage       telemetry.CallUsage            `json:"token_usage,omitempty"`
	TokenUsageByTask map[string]telemetry.CallUsage `json:"token_usage_by_task,omitempty"`
	CycleCostUSD     float64                        `json:"cycle_cost_usd,omitempty"`

	DryRun         bool            `json:"dry_run,omitempty"`
	PlannedChanges []PlannedChange `json:"planned_changes,omitempty"`

	// LastRunPaused is set by the runner when the circuit breaker
	// is engaged for an entire cycle, so /v1/status doesn't keep
	// surfacing stale numbers from the previous successful cycle.
	// (Wave 7 P1-63.)
	LastRunPaused bool   `json:"last_run_paused,omitempty"`
	PauseReason   string `json:"pause_reason,omitempty"`
}

// ManifestCache holds the last-computed manifest state-fingerprint hash
// and the associated qualitative summary. Passed by pointer into
// RunAutonomous so the cache persists across cycles. Empty fields mean
// "no cached value"; the next call populates them.
type ManifestCache struct {
	Hash    string
	Summary string
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
// manifestCache may be nil to disable the cross-cycle manifest cache.
func RunAutonomous(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, manifestCache *ManifestCache, logger *slog.Logger) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, manifestCache, logger, false)
}

// RunAutonomousDryRun is like RunAutonomous but does not apply changes.
// The LLM is still called so you can see what would be classified.
func RunAutonomousDryRun(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *AutonomousResult {
	return runAutonomousInner(ctx, e, llmProv, cfg, nil, logger, true)
}

func runAutonomousInner(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, manifestCache *ManifestCache, logger *slog.Logger, dryRun bool) *AutonomousResult {
	start := time.Now()
	logger = ensureLogger(logger)
	result := &AutonomousResult{DryRun: dryRun}
	maxCalls := cfg.LLMCuration.MaxCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 20
	}

	// Cycle-scoped usage recorder. All LLM calls in this cycle
	// accumulate tokens + cost here; the "autonomous curation complete"
	// log reads totals and per-task breakdown. Metered still records
	// into its own tracker too; this is additional per-cycle view.
	cycleUsage := &telemetry.UsageRecorder{}
	ctx = telemetry.WithUsageRecorder(ctx, cycleUsage)

	classifyPending(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	runtime.Gosched() // yield so other goroutines can acquire the lock
	generateSummaries(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	runtime.Gosched()
	enrichConceptSyntheses(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)
	runtime.Gosched()
	detectContradictions(ctx, e, llmProv, cfg, result, maxCalls, logger, dryRun)

	// Generate manifest qualitative summary if we have a manifest from
	// the last deterministic run and haven't used too many LLM calls.
	if !dryRun && result.LLMCalls < maxCalls {
		generateManifestSummary(ctx, e, llmProv, cfg, result, manifestCache, logger)
	}

	// Attach the cycle-level usage totals and per-task breakdown to the
	// AutonomousResult so callers (and the log below) can read them.
	result.TokenUsage = cycleUsage.Total()
	result.TokenUsageByTask = cycleUsage.ByTask()
	// Sum cost across all tasks. Per-task cost needs model attribution
	// which the cycle recorder doesn't hold; we approximate cost via
	// CallMetrics-side logging (per-call log emits cost_usd per call).
	// Aggregate cycle cost is the sum of per-task cost using whichever
	// model the task used most (looked up from cfg).
	cycleCost := 0.0
	for task, u := range result.TokenUsageByTask {
		model := modelForTaskLabel(cfg, task)
		cycleCost += llm.EstimateCost(model, u)
	}
	result.CycleCostUSD = cycleCost

	// Emit the per-cycle completion log. Fires on any LLM activity
	// (including contradiction-only or manifest-only cycles) by keying
	// on total tokens rather than just the counted-task counters.
	if result.LLMCalls > 0 || (result.Classified+result.SummariesGenerated+result.ConceptsCreated+result.ContradictionsDetected) > 0 {
		logArgs := []any{
			"component", "curation",
			"classified", result.Classified,
			"summaries", result.SummariesGenerated,
			"concepts", result.ConceptsCreated,
			"contradictions", result.ContradictionsDetected,
			"errors", result.Errors,
			"llm_calls", result.LLMCalls,
			"input_tokens", result.TokenUsage.InputTokens,
			"output_tokens", result.TokenUsage.OutputTokens,
			"cache_read", result.TokenUsage.CacheReadTokens,
			"cache_write", result.TokenUsage.CacheWriteTokens,
			"cost_usd", cycleCost,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		for model, count := range result.ModelCounts {
			if model == "" {
				model = "default"
			}
			logArgs = append(logArgs, "model:"+model, count)
		}
		// Per-task token breakdown (compact form).
		for task, u := range result.TokenUsageByTask {
			logArgs = append(logArgs,
				"tokens:"+task,
				fmt.Sprintf("in=%d/out=%d/cache=%d", u.InputTokens, u.OutputTokens, u.CacheReadTokens),
			)
		}
		logger.Info("autonomous curation complete", logArgs...)
	}

	return result
}

// modelForTaskLabel maps the string task label emitted by curation
// code back to a concrete model name via config. Labels that don't
// correspond to a curation task (or the contradiction_batch synonym)
// fall back to the medium tier.
func modelForTaskLabel(cfg config.Config, task string) string {
	switch task {
	case "classify":
		// Both short and long classification go through here; pick the
		// medium model as a reasonable single-number estimate. Per-call
		// log has the exact model used.
		return cfg.ModelForTask(config.TaskClassificationLong)
	case "summarize":
		return cfg.ModelForTask(config.TaskSummarization)
	case "contradiction", "contradiction_batch":
		return cfg.ModelForTask(config.TaskContradiction)
	case "concept":
		return cfg.ModelForTask(config.TaskConcept)
	case "manifest":
		return cfg.ModelForTask(config.TaskManifest)
	}
	return cfg.LLM.Models.Medium
}

// classifyPending classifies records with processing_status="captured".
func classifyPending(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)

	// Each tier (short / long) sets its own system prompt per pass.
	// Ensure the provider's cached prompt is cleared on exit.
	if setter, ok := llmProv.(llm.SystemPromptSetter); ok {
		defer setter.SetSystemPrompt("")
	}
	batchSize := cfg.LLMCuration.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// Read phase: gather pending record IDs and content.
	// Sort pendingIDs by created_at ascending so older captures
	// are classified first. PropIdx.Lookup returns IDs in
	// map-iteration order, which is quasi-stable but not FIFO --
	// without the sort, a 50-record burst could starve behind
	// later trickle captures depending on hash collisions.
	// (Wave 6 P1-62.)
	e.RLock()
	pendingIDs := e.PropIdx().Lookup("processing_status", graph.StringProperty("captured"))
	type pending struct {
		id             string
		content        string
		contextSignals string
		createdAt      time.Time
	}
	var batch []pending
	all := make([]pending, 0, len(pendingIDs))
	for _, id := range pendingIDs {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		content, ok := n.Properties.GetString("content_full")
		if !ok || content == "" {
			continue
		}
		ca, _ := n.Properties.GetTimestamp("created_at")
		all = append(all, pending{
			id:             id,
			content:        content,
			contextSignals: buildContextSignals(n),
			createdAt:      ca,
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].createdAt.Before(all[j].createdAt)
	})
	if len(all) > batchSize {
		batch = all[:batchSize]
	} else {
		batch = all
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
		model   string // model that classified this record
		data    *classificationResult
	}

	// Assign model per record: effort-based (short vs long classification).
	longThreshold := cfg.LLMCuration.LongClassificationThreshold
	if longThreshold <= 0 {
		longThreshold = 2000
	}
	shortModel := cfg.ModelForTask(config.TaskClassificationShort)
	longModel := cfg.ModelForTask(config.TaskClassificationLong)

	setter, hasSystemPrompt := llmProv.(llm.SystemPromptSetter)
	useCache := hasSystemPrompt && cfg.LLMCuration.PromptCachingEnabled

	// Pick the short-tier system prompt. When
	// ClassifyShortPromptCompressed is true (default), short records
	// use the condensed ClassifySystemPromptShort; when false, they get
	// the full ClassifySystemPrompt identical to long-tier records.
	shortSystemPrompt := ClassifySystemPromptShort
	if !cfg.LLMCuration.ClassifyShortPromptCompressed {
		shortSystemPrompt = ClassifySystemPrompt
	}

	// Partition batch by tier. Short-tier records get the leaner prompt
	// (or full, depending on the flag); long-tier records always get
	// the full ClassifySystemPrompt. If only one tier is configured,
	// all records route to that tier.
	var shortBatch, longBatch []pending
	for _, rec := range batch {
		isShort := len(rec.content) < longThreshold
		switch {
		case isShort && shortModel != "":
			shortBatch = append(shortBatch, rec)
		case !isShort && longModel != "":
			longBatch = append(longBatch, rec)
		case longModel != "":
			longBatch = append(longBatch, rec)
		case shortModel != "":
			shortBatch = append(shortBatch, rec)
		default:
			// Neither tier configured: route to long so it fails loudly
			// with an empty-model error in the provider layer.
			longBatch = append(longBatch, rec)
		}
	}

	runPass := func(sub []pending, systemPrompt, model string) []classified {
		if len(sub) == 0 {
			return nil
		}

		userTemplate := classifyPrompt
		if useCache {
			setter.SetSystemPrompt(systemPrompt)
		} else {
			userTemplate = systemPrompt + "\n\n" + classifyPrompt
		}

		work := make([]llmWork, len(sub))
		for i, rec := range sub {
			work[i] = llmWork{
				id:     rec.id,
				prompt: fmt.Sprintf(userTemplate, rec.content, rec.contextSignals),
				model:  model,
				task:   "classify",
			}
		}

		llmResults := parallelLLM(ctx, llmProv, work, 4)
		result.LLMCalls += len(llmResults)

		var out []classified
		for i, lr := range llmResults {
			if lr.err != nil {
				result.Errors++
				logger.Warn("classify LLM error", "component", "curation", "record", sub[i].id, "err", lr.err)
				continue
			}

			classification, err := parseClassification(lr.response)
			if err != nil {
				result.Errors++
				logger.Warn("classify parse error", "component", "curation", "record", sub[i].id, "err", err)
				continue
			}

			usedModel := work[i].model
			if usedModel == "" {
				usedModel = llmProv.ModelID()
			}
			out = append(out, classified{id: sub[i].id, content: sub[i].content, model: usedModel, data: classification})
		}
		return out
	}

	ready := append(runPass(shortBatch, shortSystemPrompt, shortModel),
		runPass(longBatch, ClassifySystemPrompt, longModel)...)

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
			logger.Debug("classify node gone", "component", "curation", "record", r.id)
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
		// content_medium generation removed (D12: single BM25 layer).
		if r.model != "" {
			e.SetProp(r.id, "classified_by", graph.StringProperty(r.model))
		}
		e.SetProp(r.id, "processing_status", graph.StringProperty("processed"))
		result.Classified++
		if result.ModelCounts == nil {
			result.ModelCounts = make(map[string]int)
		}
		result.ModelCounts[r.model]++
	}
	if result.Classified > 0 {
		e.SaveOrLog("curation: classify")
	}
	e.Unlock()
}

// generateSummaries adds summary_short to records that lack one.
func generateSummaries(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)

	// Cache the invariant instructions on providers that support it so
	// subsequent calls within the 5-minute TTL reuse the cached block.
	// Falls back to concatenation if caching is disabled or the provider
	// lacks SystemPromptSetter.
	userPromptTemplate := summarizePrompt
	setter, hasSetter := llmProv.(llm.SystemPromptSetter)
	if hasSetter && cfg.LLMCuration.PromptCachingEnabled {
		setter.SetSystemPrompt(SummarizeSystemPrompt)
		defer setter.SetSystemPrompt("")
	} else {
		userPromptTemplate = SummarizeSystemPrompt + "\n\n" + summarizePrompt
	}

	batchSize := cfg.LLMCuration.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// Read phase: find records needing summaries.
	// Priority 1: records with content but no summary at all.
	// Priority 2: section nodes with truncated summaries (no heading).
	e.RLock()
	g := e.Graph()
	type needsSummary struct {
		id      string
		content string
	}
	var batch []needsSummary
	var sectionCandidates []needsSummary

	sumIt := g.NodeIterator()
	for sumIt.Next() {
		n := sumIt.Node()
		id := n.ID
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}
		content, hasContent := n.Properties.GetString("content_full")
		summary, hasSummary := n.Properties.GetString("content_short")

		// Priority 1: non-chunk records with no summary.
		if !isChunkNode(g, id) && hasContent && !hasSummary && content != "" && len(batch) < batchSize {
			batch = append(batch, needsSummary{id: id, content: content})
			continue
		}

		// Priority 2: section nodes with truncated summaries.
		// Section nodes ARE structural children (isChunkNode returns true)
		// so this must be checked separately.
		if hasContent && hasSummary && content != "" {
			isSection := false
			for _, edge := range g.EdgesFrom(id) {
				if edge.Type == "section_of" {
					isSection = true
					break
				}
			}
			if isSection && len(summary) >= 150 && len(content) > len(summary) && strings.HasPrefix(content, summary) {
				sectionCandidates = append(sectionCandidates, needsSummary{id: id, content: content})
			}
		}
	}
	sumIt.Close()

	// Fill remaining batch capacity with section candidates.
	for _, sc := range sectionCandidates {
		if len(batch) >= batchSize {
			break
		}
		batch = append(batch, sc)
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

	summModel := cfg.ModelForTask(config.TaskSummarization)
	work := make([]llmWork, len(batch))
	for i, rec := range batch {
		work[i] = llmWork{
			id:     rec.id,
			prompt: fmt.Sprintf(userPromptTemplate, rec.content),
			model:  summModel,
			task:   "summarize",
		}
	}

	llmResults := parallelLLM(ctx, llmProv, work, 4)
	result.LLMCalls += len(llmResults)

	for i, lr := range llmResults {
		if lr.err != nil {
			result.Errors++
			logger.Warn("summarize LLM error", "component", "curation", "record", batch[i].id, "err", lr.err)
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
			logger.Debug("summarize node gone", "component", "curation", "record", s.id)
			continue
		}
		e.SetContentProp(s.id, "content_short", s.summary)
		result.SummariesGenerated++
	}
	if result.SummariesGenerated > 0 {
		e.SaveOrLog("curation: summarize")
	}
	e.Unlock()
}

// generateManifestSummary creates a qualitative summary of the store's
// strengths and gaps using the LLM. The summary is stored on
// AutonomousResult.ManifestSummary for the runner to apply. When cache is
// non-nil and its Hash matches the current store fingerprint, the cached
// Summary is reused without an LLM call (ManifestCacheHit=true). Otherwise
// the LLM is called and cache.Hash/Summary are updated in place.
func generateManifestSummary(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, cache *ManifestCache, logger *slog.Logger) {
	logger = ensureLogger(logger)
	// Gather lightweight stats under RLock.
	e.RLock()
	totalRecords := 0
	typeMap := make(map[string]int)
	kwCounts := e.PropIdx().KeywordCounts("content_keywords")
	var earliest, latest time.Time

	msIt := e.Graph().NodeIterator()
	for msIt.Next() {
		n := msIt.Node()
		id := n.ID
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
	msIt.Close()
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
	// Canonical sort: count descending, break ties by keyword ascending
	// so the top-N list is deterministic for the same counts regardless
	// of map iteration order. This stability is what makes the cache
	// fingerprint safe to hash across cycles.
	sort.SliceStable(kwList, func(i, j int) bool {
		if kwList[i].count != kwList[j].count {
			return kwList[i].count > kwList[j].count
		}
		return kwList[i].kw < kwList[j].kw
	})
	topN := 15
	if len(kwList) < topN {
		topN = len(kwList)
	}
	kwStrs := make([]string, topN)
	for i := 0; i < topN; i++ {
		kwStrs[i] = fmt.Sprintf("%s(%d)", kwList[i].kw, kwList[i].count)
	}

	// Build types string. Sort by key for canonical fingerprinting.
	typeKeys := make([]string, 0, len(typeMap))
	for kt := range typeMap {
		typeKeys = append(typeKeys, kt)
	}
	sort.Strings(typeKeys)
	typeStrs := make([]string, 0, len(typeKeys))
	for _, kt := range typeKeys {
		typeStrs = append(typeStrs, fmt.Sprintf("%s(%d)", kt, typeMap[kt]))
	}

	earliestStr := "N/A"
	latestStr := "N/A"
	if !earliest.IsZero() {
		earliestStr = earliest.Format("2006-01-02")
	}
	if !latest.IsZero() {
		latestStr = latest.Format("2006-01-02")
	}

	// Compute the state fingerprint hash. Same inputs -> same summary,
	// so we can skip the LLM call when nothing has changed.
	fp := fmt.Sprintf("records=%d|types=%s|keywords=%s|span=%s..%s",
		totalRecords,
		strings.Join(typeStrs, ","),
		strings.Join(kwStrs, ","),
		earliestStr, latestStr,
	)
	sum := sha256.Sum256([]byte(fp))
	currentHash := hex.EncodeToString(sum[:])

	cacheEnabled := cfg.LLMCuration.ManifestCacheEnabled
	if cacheEnabled && cache != nil && cache.Hash == currentHash && cache.Summary != "" {
		result.ManifestSummary = cache.Summary
		result.ManifestCacheHit = true
		logger.Info("manifest summary",
			"component", "curation",
			"cached", true,
			"hash", currentHash[:8],
			"records", totalRecords,
		)
		return
	}

	// Cache the invariant summarize-the-store instructions.
	userPromptTemplate := manifestSummaryPrompt
	setter, hasSetter := llmProv.(llm.SystemPromptSetter)
	if hasSetter && cfg.LLMCuration.PromptCachingEnabled {
		setter.SetSystemPrompt(ManifestSystemPrompt)
		defer setter.SetSystemPrompt("")
	} else {
		userPromptTemplate = ManifestSystemPrompt + "\n\n" + manifestSummaryPrompt
	}

	prompt := fmt.Sprintf(userPromptTemplate,
		totalRecords,
		strings.Join(typeStrs, ", "),
		strings.Join(kwStrs, ", "),
		earliestStr, latestStr,
	)

	model := cfg.ModelForTask(config.TaskManifest)
	resp, err := completeWithModelOrDefault(ctx, llmProv, "manifest", model, prompt)
	result.LLMCalls++
	if err != nil {
		result.Errors++
		logger.Warn("manifest summary LLM error", "component", "curation", "err", err)
		return
	}

	summary := strings.TrimSpace(resp)
	// Rune-safe truncation to 500 characters.
	runes := []rune(summary)
	if len(runes) > 500 {
		summary = string(runes[:500])
	}
	result.ManifestSummary = summary

	// Update the cache so the next cycle with the same fingerprint can
	// skip the LLM call.
	if cacheEnabled && cache != nil {
		cache.Hash = currentHash
		cache.Summary = summary
	}

	logger.Info("manifest summary",
		"component", "curation",
		"cached", false,
		"hash", currentHash[:8],
		"records", totalRecords,
		"model", model,
	)
}

// enrichConceptSyntheses finds concept nodes with synthesis_status=pending
// and upgrades their template content with LLM-generated synthesis.
// Prioritizes by access_count (most-accessed first). Batches multiple
// concepts per LLM call for efficiency. Uses leftover LLM budget only.
func enrichConceptSyntheses(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)
	if result.LLMCalls >= maxCalls {
		return
	}

	batchSize := cfg.LLMCuration.SynthesisBatchSize
	if batchSize <= 0 {
		batchSize = 5
	}
	maxInputTokens := cfg.LLMCuration.SynthesisMaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = 8000
	}

	// Cap to remaining LLM budget.
	remaining := maxCalls - result.LLMCalls
	maxConcepts := cfg.LLMCuration.MaxConceptsPerRun
	if maxConcepts <= 0 {
		maxConcepts = 5
	}
	if maxConcepts > remaining {
		maxConcepts = remaining
	}

	// Find pending concept nodes, sorted by access_count descending.
	type pendingConcept struct {
		id          string
		keyword     string
		accessCount int64
		memberIDs   []string
	}
	var pending []pendingConcept

	e.RLock()
	g := e.Graph()
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		nt, _ := n.Properties.GetString("node_type")
		ss, _ := n.Properties.GetString("synthesis_status")
		if nt != "concept" || ss != "pending" {
			continue
		}
		kw, _ := n.Properties.GetString("concept_keyword")
		ac, _ := n.Properties.GetInt64("access_count")

		// Get member IDs from instance_of edges.
		var memberIDs []string
		for _, edge := range g.EdgesTo(n.ID) {
			if edge.Type == "instance_of" {
				memberIDs = append(memberIDs, edge.SourceID)
			}
		}

		pending = append(pending, pendingConcept{
			id:          n.ID,
			keyword:     kw,
			accessCount: ac,
			memberIDs:   memberIDs,
		})
	}
	it.Close()

	if len(pending) == 0 {
		e.RUnlock()
		return
	}

	// Sort by access_count descending (most-accessed first).
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].accessCount > pending[j].accessCount
	})
	if len(pending) > maxConcepts {
		pending = pending[:maxConcepts]
	}

	// Cache the invariant synthesis instructions on providers that
	// support it; otherwise include them at the top of every batch.
	setter, hasSystemPrompt := llmProv.(llm.SystemPromptSetter)
	useCache := hasSystemPrompt && cfg.LLMCuration.PromptCachingEnabled
	preamble := ""
	if !useCache {
		preamble = ConceptSynthesisSystemPrompt + "\n\n"
	}

	// Build batched synthesis prompts.
	type conceptBatch struct {
		concepts []pendingConcept
		prompt   string
	}
	var batches []conceptBatch
	var currentBatch []pendingConcept
	var currentPrompt strings.Builder
	estimatedTokens := 0

	currentPrompt.WriteString(preamble)
	baseTokens := currentPrompt.Len() / 4

	coherenceMin := cfg.LLMCuration.ConceptCoherenceMin

	for _, pc := range pending {
		// Optional coherence pre-filter: skip clusters whose members
		// don't embed near a common centroid. Incoherent clusters tend
		// to produce low-quality syntheses, so filtering them out
		// preserves LLM budget for clusters that benefit. Default
		// threshold is 0 (no filter) so behavior is unchanged unless
		// the user opts in.
		if coherenceMin > 0 {
			cos, n := meanCosineToCentroid(g, pc.memberIDs)
			if n >= 2 && cos < coherenceMin {
				logger.Debug("concept below coherence threshold, skipping",
					"component", "curation",
					"keyword", pc.keyword,
					"mean_cosine", cos,
					"threshold", coherenceMin,
					"members", n)
				continue
			}
		}

		// Gather member summaries.
		var memberSummaries []string
		for _, mid := range pc.memberIDs {
			mn, ok := g.GetNode(mid)
			if !ok {
				continue
			}
			if es, ok := mn.Properties.GetString("epistemic_status"); ok && es == "speculative" {
				continue
			}
			if s, ok := mn.Properties.GetString("content_short"); ok && s != "" {
				memberSummaries = append(memberSummaries, "- "+s)
			} else if s, ok := mn.Properties.GetString("content_full"); ok && len(s) > 200 {
				memberSummaries = append(memberSummaries, "- "+s[:200])
			} else if s, ok := mn.Properties.GetString("content_full"); ok {
				memberSummaries = append(memberSummaries, "- "+s)
			}
		}
		if len(memberSummaries) == 0 {
			continue
		}
		if len(memberSummaries) > 20 {
			memberSummaries = memberSummaries[:20]
		}

		conceptSection := fmt.Sprintf("Concept: %q (%d records)\nMembers:\n%s\n\n",
			pc.keyword, len(pc.memberIDs), strings.Join(memberSummaries, "\n"))
		sectionTokens := len(conceptSection) / 4

		// Check if adding this concept would exceed limits.
		if len(currentBatch) > 0 && (len(currentBatch) >= batchSize || estimatedTokens+sectionTokens > maxInputTokens) {
			batches = append(batches, conceptBatch{
				concepts: currentBatch,
				prompt:   currentPrompt.String(),
			})
			currentBatch = nil
			currentPrompt.Reset()
			currentPrompt.WriteString(preamble)
			estimatedTokens = baseTokens
		}

		currentPrompt.WriteString(conceptSection)
		currentBatch = append(currentBatch, pc)
		estimatedTokens += sectionTokens
	}
	if len(currentBatch) > 0 {
		batches = append(batches, conceptBatch{
			concepts: currentBatch,
			prompt:   currentPrompt.String(),
		})
	}

	e.RUnlock()

	// Activate the cached synthesis instructions for the provider if
	// supported and enabled. Cleared on return.
	if useCache {
		setter.SetSystemPrompt(ConceptSynthesisSystemPrompt)
		defer setter.SetSystemPrompt("")
	}

	// Execute batched LLM calls.
	for _, batch := range batches {
		if result.LLMCalls >= maxCalls {
			break
		}

		if dryRun {
			for _, pc := range batch.concepts {
				result.PlannedChanges = append(result.PlannedChanges, PlannedChange{
					Action:      "enrich_concept",
					RecordID:    pc.id,
					ContentSnip: pc.keyword,
				})
			}
			result.LLMCalls++
			result.ConceptsCreated += len(batch.concepts)
			continue
		}

		resp, err := completeWithModelOrDefault(ctx, llmProv, "concept", cfg.ModelForTask(config.TaskConcept), batch.prompt)
		result.LLMCalls++
		if err != nil {
			result.Errors++
			logger.Warn("concept synthesis batch failed",
				"component", "curation",
				"batch_size", len(batch.concepts),
				"err", err)
			continue
		}

		// Parse JSON array response.
		syntheses := parseBatchSynthesis(resp)
		if syntheses == nil {
			result.Errors++
			logger.Warn("concept synthesis parse failed",
				"component", "curation",
				"batch_size", len(batch.concepts),
				"response_len", len(resp))
			continue
		}

		// Apply syntheses to concept nodes.
		e.Lock()
		for i, pc := range batch.concepts {
			if i >= len(syntheses) {
				break
			}
			synthesis := syntheses[i]
			if synthesis == "" {
				continue
			}

			// Truncate.
			runes := []rune(synthesis)
			if len(runes) > 500 {
				synthesis = string(runes[:500])
			}

			shortSummary := conceptShortSummary(synthesis, 200)

			e.SetContentProp(pc.id, "content_full", synthesis)
			e.SetContentProp(pc.id, "content_short", shortSummary)
			e.SetProp(pc.id, "synthesis_status", graph.StringProperty("complete"))
			result.ConceptsCreated++

			logger.Info("concept enriched",
				"component", "curation",
				"keyword", pc.keyword,
				"node_id", pc.id)
		}
		if result.ConceptsCreated > 0 {
			e.SaveOrLog("curation: enrich concepts")
		}
		e.Unlock()
	}
}

// meanCosineToCentroid computes the mean cosine similarity of member
// records to their centroid, using `embedding_full` as the vector.
// Members without an embedding are skipped. Returns (0, 0) when fewer
// than 2 members have embeddings (meaningful coherence requires at least
// two vectors). Assumes embeddings are already L2-normalized (which the
// current embedding pipelines produce); the centroid is re-normalized
// defensively.
func meanCosineToCentroid(g *graph.Graph, memberIDs []string) (float64, int) {
	var vecs [][]float32
	var dim int
	for _, id := range memberIDs {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		emb, ok := n.Properties.GetVector("embedding_full")
		if !ok || len(emb) == 0 {
			continue
		}
		if dim == 0 {
			dim = len(emb)
		}
		if len(emb) != dim {
			continue // dimension mismatch, skip
		}
		vecs = append(vecs, emb)
	}
	if len(vecs) < 2 {
		return 0, len(vecs)
	}

	// Centroid = mean of all vectors.
	centroid := make([]float32, dim)
	for _, v := range vecs {
		for i, f := range v {
			centroid[i] += f
		}
	}
	inv := 1.0 / float32(len(vecs))
	for i := range centroid {
		centroid[i] *= inv
	}
	// Normalize centroid so cosine = dot product (vectors assumed
	// normalized already).
	var norm float32
	for _, f := range centroid {
		norm += f * f
	}
	if norm <= 0 {
		return 0, len(vecs)
	}
	invN := 1.0 / float32(math.Sqrt(float64(norm)))
	for i := range centroid {
		centroid[i] *= invN
	}

	// Mean cosine = average dot product of each vector with centroid.
	var sum float64
	for _, v := range vecs {
		var dot float32
		for i, f := range v {
			dot += f * centroid[i]
		}
		sum += float64(dot)
	}
	return sum / float64(len(vecs)), len(vecs)
}

// parseBatchSynthesis extracts synthesis strings from a JSON array
// response. Handles markdown code fences and partial JSON.
func parseBatchSynthesis(resp string) []string {
	text := strings.TrimSpace(resp)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var results []struct {
		Keyword   string `json:"keyword"`
		Synthesis string `json:"synthesis"`
	}
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil // caller logs the batch error; individual parse failures are expected
	}

	syntheses := make([]string, len(results))
	for i, r := range results {
		syntheses[i] = r.Synthesis
	}
	return syntheses
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
	logger = ensureLogger(logger)
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
	// Shuffle so different records get checked across cycles.
	// PropIdx.Lookup returns IDs in map-iteration order, which is
	// quasi-stable -- without this, the same first-N records would
	// be re-checked every cycle and the rest never reached. A
	// random pick covers the population over time. We don't pick
	// newest-first because the contradiction-application path
	// trusts the LLM's A/B assignment, which is sensitive to
	// prompt ordering; a stable newest-first sort would change
	// behavior for the same store across restarts. (Wave 7 P1-61.)
	rand.Shuffle(len(processedIDs), func(i, j int) {
		processedIDs[i], processedIDs[j] = processedIDs[j], processedIDs[i]
	})
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

			// Check if they already have an edge between them. The A->B
			// direction is always checked; the B->A direction is checked
			// only when ContradictionCheckReverseEdges is true (default)
			// -- some edges (notably supersedes) are unidirectional.
			hasEdge := false
			for _, edge := range e.Graph().EdgesFrom(idA) {
				if edge.TargetID == sr.NodeID {
					hasEdge = true
					break
				}
			}
			if !hasEdge && cfg.LLMCuration.ContradictionCheckReverseEdges {
				for _, edge := range e.Graph().EdgesFrom(sr.NodeID) {
					if edge.TargetID == idA {
						hasEdge = true
						break
					}
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

	// LLM phase: single-pair or batched depending on config.
	type detected struct {
		idA, idB     string
		contentA     string
		relationship string
		confidence   float64
		explanation  string
	}
	var findings []detected

	batchSize := cfg.LLMCuration.ContradictionBatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	model := cfg.ModelForTask(config.TaskContradiction)

	if batchSize == 1 {
		// Cache the single-pair instructions. User body is the two records.
		userPromptTemplate := contradictionPrompt
		setter, hasSetter := llmProv.(llm.SystemPromptSetter)
		if hasSetter && cfg.LLMCuration.PromptCachingEnabled {
			setter.SetSystemPrompt(ContradictionSystemPrompt)
			defer setter.SetSystemPrompt("")
		} else {
			userPromptTemplate = ContradictionSystemPrompt + "\n\n" + contradictionPrompt
		}

		for _, c := range candidates {
			if result.LLMCalls >= maxCalls {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}

			prompt := fmt.Sprintf(userPromptTemplate, c.contentA, c.contentB)
			resp, err := completeWithModelOrDefault(ctx, llmProv, "contradiction", model, prompt)
			result.LLMCalls++

			if err != nil {
				result.Errors++
				logger.Warn("contradiction LLM error", "component", "curation", "err", err)
				continue
			}

			cr, err := parseContradictionResult(resp)
			if err != nil {
				result.Errors++
				logger.Warn("contradiction parse error", "component", "curation", "err", err)
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
	} else {
		// Batched mode: N pairs per LLM call. Use the batch system prompt,
		// which instructs the LLM to return a JSON array with pair_id.
		includeSystemInline := true
		setter, hasSetter := llmProv.(llm.SystemPromptSetter)
		if hasSetter && cfg.LLMCuration.PromptCachingEnabled {
			setter.SetSystemPrompt(ContradictionBatchSystemPrompt)
			defer setter.SetSystemPrompt("")
			includeSystemInline = false
		}

		for start := 0; start < len(candidates); start += batchSize {
			if result.LLMCalls >= maxCalls {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}

			end := start + batchSize
			if end > len(candidates) {
				end = len(candidates)
			}
			batch := candidates[start:end]

			var sb strings.Builder
			if includeSystemInline {
				sb.WriteString(ContradictionBatchSystemPrompt)
				sb.WriteString("\n\n")
			}
			for i, c := range batch {
				fmt.Fprintf(&sb, "Pair %d:\nRecord A:\n%s\n\nRecord B:\n%s\n\n", i+1, c.contentA, c.contentB)
			}
			fmt.Fprintf(&sb, "Classify all %d pairs. Respond with a JSON array of %d objects, one per pair, preserving the input order and pair_id.", len(batch), len(batch))

			resp, err := completeWithModelOrDefault(ctx, llmProv, "contradiction_batch", model, sb.String())
			result.LLMCalls++

			if err != nil {
				result.Errors++
				logger.Warn("contradiction batch LLM error", "component", "curation", "batch_size", len(batch), "err", err)
				continue
			}

			batchResults, err := parseContradictionBatchResult(resp)
			if err != nil {
				result.Errors++
				logger.Warn("contradiction batch parse error", "component", "curation", "batch_size", len(batch), "err", err)
				continue
			}

			for _, br := range batchResults {
				// pair_id is 1-indexed into the current batch; fall back
				// to positional match if pair_id is missing or out of range.
				idx := br.PairID - 1
				if idx < 0 || idx >= len(batch) {
					continue
				}
				c := batch[idx]
				if br.Relationship == "contradicts" || br.Relationship == "supersedes" {
					findings = append(findings, detected{
						idA: c.idA, idB: c.idB, contentA: c.contentA,
						relationship: br.Relationship,
						confidence:   br.Confidence,
						explanation:  br.Explanation,
					})
				}
			}
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
			if _, err := e.Graph().AddEdge(f.idA, f.idB, f.relationship, f.confidence, nil); err != nil {
				logger.Error("failed to add contradicts edge",
					"component", "curation", "from", f.idA, "to", f.idB, "err", err)
			}
			if _, err := e.Graph().AddEdge(f.idB, f.idA, f.relationship, f.confidence, nil); err != nil {
				logger.Error("failed to add contradicts edge (reverse)",
					"component", "curation", "from", f.idB, "to", f.idA, "err", err)
			}
			result.ContradictionsDetected++

		case "supersedes":
			// B supersedes A: A is older, B is the replacement.
			now := time.Now().UTC()
			if _, err := e.Graph().AddEdge(f.idB, f.idA, "supersedes", f.confidence, nil); err != nil {
				logger.Error("failed to add supersedes edge",
					"component", "curation", "newer", f.idB, "older", f.idA, "err", err)
			}
			e.SetProp(f.idA, "valid_until", graph.TimestampProperty(now))
			e.SetProp(f.idA, "resolution", graph.StringProperty("superseded"))
			e.SetProp(f.idA, "resolved_at", graph.TimestampProperty(now))
			result.ContradictionsDetected++
		}
	}
	if result.ContradictionsDetected > 0 {
		e.SaveOrLog("curation: contradictions")
	}
	e.Unlock()

	if result.ContradictionsDetected > 0 {
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

// batchContradictionResult is one entry from a batched contradiction call.
// PairID is 1-indexed into the input batch; the caller maps it back to the
// candidate pair.
type batchContradictionResult struct {
	PairID       int     `json:"pair_id"`
	Relationship string  `json:"relationship"`
	Confidence   float64 `json:"confidence"`
	Explanation  string  `json:"explanation"`
}

// parseContradictionBatchResult extracts per-pair results from a JSON
// array response. Handles markdown code fences and validates each entry.
// Returns an empty slice + nil error for an empty array; returns an error
// only on malformed JSON.
func parseContradictionBatchResult(resp string) ([]batchContradictionResult, error) {
	text := strings.TrimSpace(resp)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	// Be tolerant of leading narration: find the first [ and last ].
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var raw []batchContradictionResult
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("parse contradiction batch JSON: %w", err)
	}

	out := make([]batchContradictionResult, 0, len(raw))
	for _, r := range raw {
		r.Relationship = validateEnumLogged(r.Relationship,
			[]string{"contradicts", "supersedes", "related", "none"},
			"contradiction_batch.relationship", slog.Default())
		if r.Relationship == "" {
			r.Relationship = "none"
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			r.Confidence = 0.5
		}
		out = append(out, r)
	}
	return out, nil
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

	result.Relationship = validateEnumLogged(result.Relationship,
		[]string{"contradicts", "supersedes", "related", "none"},
		"contradiction.relationship", slog.Default())
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

// buildContextSignals extracts context signal properties from a node
// and formats them for the classification prompt. Returns an empty
// string if no signals are present.
func buildContextSignals(n *graph.Node) string {
	signals := []struct {
		key, label string
	}{
		{"context_source_type", "Source type"},
		{"context_time_sensitivity", "Time sensitivity"},
		{"context_reliability", "Reliability"},
		{"context_capture_reason", "Capture reason"},
		{"context_about", "About"},
		{"context_who", "Entities"},
	}
	var parts []string
	for _, s := range signals {
		if v, ok := n.Properties.GetString(s.key); ok && v != "" {
			parts = append(parts, s.label+": "+v)
		}
	}
	// Also pass agent-provided enum hints if present.
	for _, hint := range []string{"temporality", "knowledge_type", "epistemic_status"} {
		if v, ok := n.Properties.GetString(hint); ok && v != "" {
			parts = append(parts, "Agent hint ("+hint+"): "+v)
		}
	}
	if conf, ok := n.Properties.GetFloat64("confidence"); ok {
		parts = append(parts, fmt.Sprintf("Agent hint (confidence): %.2f", conf))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nContext signals:\n- " + strings.Join(parts, "\n- ") + "\n"
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

// validateEnumLogged is validateEnum with a Warn log when the LLM
// returned a value that didn't match any allowed enum. Useful for
// surfacing "near-miss" responses that the silent-default-to-zero
// path of validateEnum would otherwise drop -- e.g. relationship
// "update" being dropped to "none" without anyone seeing why.
// (Wave 6 P1-60.)
func validateEnumLogged(val string, allowed []string, field string, logger *slog.Logger) string {
	if val == "" {
		return ""
	}
	if v := validateEnum(val, allowed); v != "" {
		return v
	}
	if logger != nil {
		logger.Warn("LLM returned unrecognised enum value",
			"component", "curation",
			"field", field,
			"value", val,
			"allowed", allowed)
	}
	return ""
}
