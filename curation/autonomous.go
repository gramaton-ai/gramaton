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
	"github.com/gramaton-ai/gramaton/internal/sanitize"
	"github.com/gramaton-ai/gramaton/internal/strutil"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// AutonomousResult summarizes what an LLM curation cycle did.
type AutonomousResult struct {
	Classified             int                             `json:"classified"`
	SummariesGenerated     int                             `json:"summaries_generated"`
	ConceptsCreated        int                             `json:"concepts_created"`
	ContradictionsDetected int                             `json:"contradictions_detected"`
	// NoContradictionEdges counts pairs the LLM affirmatively said are not
	// contradicting/superseding. Each such pair gets a "no_contradiction"
	// edge so subsequent cycles don't re-ask. Without this counter (and
	// its underlying edge) the candidate pool does not drain on negative
	// results -- see design-decisions.md D38.
	NoContradictionEdges   int                             `json:"no_contradiction_edges"`
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
	LastRunPaused bool   `json:"last_run_paused,omitempty"`
	PauseReason   string `json:"pause_reason,omitempty"`
}

// ManifestCache holds the last-computed manifest state-fingerprint hash
// and the associated qualitative summary. Passed by pointer into
// RunAutonomous so the cache persists across cycles. Empty fields mean
// "no cached value"; the next call populates them.
//
// LastFailedHash + FailedAttempts implement a negative cache: a
// fingerprint that consistently fails to produce a usable summary
// stops calling the LLM after MaxManifestAttempts cycles. The
// negative cache clears automatically when (a) the fingerprint
// changes (store state moved), or (b) a successful synthesis lands
// for any fingerprint (model behavior likely improved).
type ManifestCache struct {
	Hash           string
	Summary        string
	LastFailedHash string
	FailedAttempts int
}

// lastClassifyErrorMaxRunes caps the size of the per-record
// last_classify_error property. Provider errors occasionally embed
// echoed prompt fragments or transport URLs; the cap bounds how much
// of that lands on a record visible through gramaton_inspect.
const lastClassifyErrorMaxRunes = 200

// lastSummaryErrorMaxRunes caps the size of the per-record
// last_summary_error property. Same rationale as
// lastClassifyErrorMaxRunes; kept as a distinct constant in case the
// summary-failure error shapes diverge from classify in future.
const lastSummaryErrorMaxRunes = 200

// lastSynthesisErrorMaxRunes caps the size of the per-concept
// last_synthesis_error property. Concept synthesis failures are
// often batch-level (one LLM error or parse failure affects all N
// concepts in the batch), so the same reason can land on multiple
// nodes; the cap keeps each individual record-level write bounded.
const lastSynthesisErrorMaxRunes = 200

// lastContradictionErrorMaxRunes caps the size of the per-edge
// last_error property on contradiction_check_skipped edges. Same
// rationale as the per-record counterparts: provider errors may
// embed prompt fragments or transport URLs, the cap bounds what
// lands on the edge.
const lastContradictionErrorMaxRunes = 200

// contradictionCheckSkippedEdge is the edge type written on a pair
// whose contradiction-check failed (LLM transport error or parse
// error). The edge carries attempts (Int64), last_error (String),
// and checked_at (Timestamp) properties. The read-phase hasEdge
// guard treats this edge as a SOFT skip when attempts <
// MaxContradictionAttempts (pair stays in candidate pool, retried
// next time it surfaces) and a HARD skip when attempts >= max
// (pair locked out until an operator unlinks the edge). Distinct
// from no_contradiction (which is a real LLM affirmation) because
// the epistemic state differs: we tried and couldn't determine.
const contradictionCheckSkippedEdge = "contradiction_check_skipped"

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
	maxCalls := cfg.LLM.Curation.MaxCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 20
	}
	maxCostUSD := cfg.LLM.CostLimits.MaxCostUSDPerRun // 0 = no cost cap

	// Cycle-scoped usage recorder. All LLM calls in this cycle
	// accumulate tokens + cost here; the "autonomous curation complete"
	// log reads totals and per-task breakdown. Metered still records
	// into its own tracker too; this is additional per-cycle view.
	cycleUsage := &telemetry.UsageRecorder{}
	ctx = telemetry.WithUsageRecorder(ctx, cycleUsage)

	taskTimeout := cfg.LLM.Curation.TaskTimeout

	runTaskWithTimeout(ctx, "classify", taskTimeout, logger, func(c context.Context) {
		classifyPending(c, e, llmProv, cfg, result, maxCalls, maxCostUSD, logger, dryRun)
	})
	runtime.Gosched() // yield so other goroutines can acquire the lock
	runTaskWithTimeout(ctx, "summarize", taskTimeout, logger, func(c context.Context) {
		generateSummaries(c, e, llmProv, cfg, result, maxCalls, maxCostUSD, logger, dryRun)
	})
	runtime.Gosched()
	runTaskWithTimeout(ctx, "concept", taskTimeout, logger, func(c context.Context) {
		enrichConceptSyntheses(c, e, llmProv, cfg, result, maxCalls, maxCostUSD, logger, dryRun)
	})
	runtime.Gosched()
	runTaskWithTimeout(ctx, "contradict", taskTimeout, logger, func(c context.Context) {
		detectContradictions(c, e, llmProv, cfg, result, maxCalls, maxCostUSD, logger, dryRun)
	})

	// Generate manifest qualitative summary if we have a manifest from
	// the last deterministic run and haven't used too many LLM calls.
	if !dryRun && !cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
		runTaskWithTimeout(ctx, "manifest", taskTimeout, logger, func(c context.Context) {
			generateManifestSummary(c, e, llmProv, cfg, result, manifestCache, logger)
		})
	}

	// Attach the cycle-level usage totals and per-task breakdown to the
	// AutonomousResult so callers (and the log below) can read them.
	result.TokenUsage = cycleUsage.Total()
	result.TokenUsageByTask = cycleUsage.ByTask()
	// Per-task cost via the pricing table. We don't have the real model
	// per-call here (the cycle recorder holds only task labels), so we
	// use the effort-to-model mapping from cfg -- the cost number is
	// accurate when a task used its configured model, approximate
	// otherwise.
	perTaskCost := make(map[string]float64, len(result.TokenUsageByTask))
	perModelCost := make(map[string]float64)
	cycleCost := 0.0
	for task, u := range result.TokenUsageByTask {
		model := modelForTaskLabel(cfg, task)
		c := llm.EstimateCost(model, u)
		perTaskCost[task] = c
		perModelCost[model] += c
		cycleCost += c
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
		// Sort model names for deterministic log output -- map iteration
		// order otherwise interleaves keys differently per cycle, making
		// grep-after-the-fact investigations harder.
		modelKeys := make([]string, 0, len(result.ModelCounts))
		for m := range result.ModelCounts {
			modelKeys = append(modelKeys, m)
		}
		sort.Strings(modelKeys)
		for _, model := range modelKeys {
			label := model
			if label == "" {
				label = "default"
			}
			logArgs = append(logArgs, "model:"+label, result.ModelCounts[model])
		}
		// Per-model cost: "which model burned the most" is the single
		// question operators ask most often after a surprising bill.
		costKeys := make([]string, 0, len(perModelCost))
		for m := range perModelCost {
			costKeys = append(costKeys, m)
		}
		sort.Strings(costKeys)
		for _, model := range costKeys {
			if perModelCost[model] == 0 {
				continue // skip untracked (CLI providers, unknown pricing)
			}
			logArgs = append(logArgs, "cost:"+model, fmt.Sprintf("$%.4f", perModelCost[model]))
		}
		// Per-task token + cost breakdown (compact form).
		taskKeys := make([]string, 0, len(result.TokenUsageByTask))
		for t := range result.TokenUsageByTask {
			taskKeys = append(taskKeys, t)
		}
		sort.Strings(taskKeys)
		for _, task := range taskKeys {
			u := result.TokenUsageByTask[task]
			logArgs = append(logArgs,
				"tokens:"+task,
				fmt.Sprintf("in=%d/out=%d/cache=%d/cost=$%.4f",
					u.InputTokens, u.OutputTokens, u.CacheReadTokens, perTaskCost[task]),
			)
		}
		logger.Info("autonomous curation complete", logArgs...)
	}

	return result
}

// cycleBudgetExceeded returns true when the cycle has hit either its
// count cap (result.LLMCalls >= maxCalls) or its cost cap (accumulated
// per-task token cost >= maxCostUSD). maxCostUSD <= 0 disables the
// cost check; maxCalls is always enforced. Cost is estimated from the
// cycle-scoped recorder in ctx using the pricing table and per-task
// model lookup -- unknown models contribute 0, so the count cap is the
// real backstop in that regime. Post-call check: the cycle may exceed
// the cost cap by one in-flight call before the next iteration breaks.
// runTaskWithTimeout runs fn under a per-task sub-context that
// expires after `timeout`. When the timeout fires, the in-flight LLM
// call is cancelled (via the sub-context) and fn returns; the next
// task in the cycle starts with a fresh sub-context derived from the
// parent. Without this, one stuck LLM call (e.g. a 120s HTTP timeout)
// could starve every downstream task in a 1-minute curation cycle.
//
// Bails immediately when the parent ctx is already cancelled (server
// shutdown / cycle cancellation) so a cancelled cycle doesn't pay
// per-task setup cost across N remaining tasks. `timeout <= 0`
// disables the per-task cap and runs fn under the parent ctx (legacy
// behavior).
func runTaskWithTimeout(parentCtx context.Context, name string, timeout time.Duration, logger *slog.Logger, fn func(context.Context)) {
	if err := parentCtx.Err(); err != nil {
		return
	}
	if timeout <= 0 {
		fn(parentCtx)
		return
	}
	taskCtx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	start := time.Now()
	fn(taskCtx)
	if taskCtx.Err() == context.DeadlineExceeded {
		logger.Warn("curation task hit per-task timeout",
			"component", "curation",
			"task", name,
			"timeout", timeout,
			"elapsed", time.Since(start))
	}
}

func cycleBudgetExceeded(ctx context.Context, cfg config.Config, result *AutonomousResult, maxCalls int, maxCostUSD float64) bool {
	if result.LLMCalls >= maxCalls {
		return true
	}
	if maxCostUSD > 0 {
		if cost := cycleCostSoFar(ctx, cfg); cost >= maxCostUSD {
			return true
		}
	}
	return false
}

// cycleCostSoFar reads the per-task token counts from the cycle
// recorder attached to ctx and sums llm.EstimateCost across them.
// Returns 0 when no recorder is attached (shouldn't happen in the
// normal autonomous path) or when all models are missing from the
// pricing table.
func cycleCostSoFar(ctx context.Context, cfg config.Config) float64 {
	recorder := telemetry.RecorderFromContext(ctx)
	if recorder == nil {
		return 0
	}
	total := 0.0
	for task, u := range recorder.ByTask() {
		model := modelForTaskLabel(cfg, task)
		total += llm.EstimateCost(model, u)
	}
	return total
}

// modelForTaskLabel maps the string task label emitted by curation
// code back to a concrete model name via config. Labels that don't
// correspond to a curation task (or the contradiction_batch synonym)
// fall back to the medium tier via ModelAtEffort so the direct
// tier-map access stays confined to config.go.
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
	return cfg.ModelAtEffort(config.EffortMedium)
}

// classifyPending classifies records with processing_status="captured".
func classifyPending(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, maxCostUSD float64, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)

	// Early ctx-cancel check: bail before grabbing RLock and walking the
	// pending list. Pre-fix this check was after the read phase, so a
	// cancelled cycle still iterated every pending record under RLock
	// before noticing.
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Each tier (short / long) sets its own system prompt per pass.
	// Ensure the provider's cached prompt is cleared on exit.
	if setter, ok := llmProv.(llm.SystemPromptSetter); ok {
		defer setter.SetSystemPrompt("")
	}
	batchSize := cfg.LLM.Curation.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// Read phase: gather pending record IDs and content.
	// Sort pendingIDs by created_at ascending so older captures
	// are classified first. PropIdx.Lookup returns IDs in
	// map-iteration order, which is quasi-stable but not FIFO --
	// without the sort, a 50-record burst could starve behind
	// later trickle captures depending on hash collisions.
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
		// Effective curation gate: skip if the record's collection
		// memberships resolve to curation=none. Memory orphans get
		// the standard default and pass through.
		if EffectiveCurationFor(e.Graph(), id).Curation == "none" {
			continue
		}
		// LLM input text. RecordContentFor returns content_full for
		// Memory records and content_fields-driven text for collection
		// items (or the wide-concat fallback for schemaless). Empty
		// result means nothing classifiable -- skip.
		content := RecordContentFor(e.Graph(), id)
		if content == "" {
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

	// Gate on count and cost caps before doing any work. The batch
	// trim below still uses the count cap -- the cost cap only gates
	// "should we run this phase at all", since mid-phase cost tracking
	// would have to pre-estimate token counts.
	if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
		return
	}
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

	// failedClassify carries an id + truncated error reason for a record
	// whose classification attempt did not produce a usable result.
	// Surfaces in the write phase as a classify_attempts increment and
	// (at threshold) processing_status="stuck".
	type failedClassify struct {
		id     string
		reason string
	}

	type classifyOutcome struct {
		succeeded []classified
		failed    []failedClassify
	}

	// Assign model per record: effort-based (short vs long classification).
	longThreshold := cfg.LLM.Curation.LongClassificationThreshold
	if longThreshold <= 0 {
		longThreshold = 2000
	}
	shortModel := cfg.ModelForTask(config.TaskClassificationShort)
	longModel := cfg.ModelForTask(config.TaskClassificationLong)

	setter, hasSystemPrompt := llmProv.(llm.SystemPromptSetter)
	useCache := hasSystemPrompt && cfg.LLM.Curation.PromptCachingEnabled

	// Pick the short-tier system prompt. When
	// ClassifyShortPromptCompressed is true (default), short records
	// use the condensed ClassifySystemPromptShort; when false, they get
	// the full ClassifySystemPrompt identical to long-tier records.
	shortSystemPrompt := ClassifySystemPromptShort
	if !cfg.LLM.Curation.ClassifyShortPromptCompressed {
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

	runPass := func(sub []pending, systemPrompt, model string) classifyOutcome {
		if len(sub) == 0 {
			return classifyOutcome{}
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
				// Schema-enforced output when the provider supports
				// it (Anthropic via tool-use today). Providers without
				// structured-output capability ignore schema and route
				// through Complete + parseClassification as before.
				schema: classificationSchema,
			}
		}

		llmResults := parallelLLM(ctx, llmProv, work, 4)
		result.LLMCalls += len(llmResults)

		var out classifyOutcome
		for i, lr := range llmResults {
			if lr.err != nil {
				result.Errors++
				logger.Warn("classify LLM error", "component", "curation", "record", sub[i].id, "err", lr.err)
				out.failed = append(out.failed, failedClassify{
					id:     sub[i].id,
					reason: strutil.TruncateRunes(lr.err.Error(), lastClassifyErrorMaxRunes),
				})
				continue
			}

			classification, err := parseClassification(lr.response)
			if err != nil {
				result.Errors++
				logger.Warn("classify parse error", "component", "curation", "record", sub[i].id, "err", err)
				out.failed = append(out.failed, failedClassify{
					id:     sub[i].id,
					reason: strutil.TruncateRunes("parse: "+err.Error(), lastClassifyErrorMaxRunes),
				})
				continue
			}

			usedModel := work[i].model
			if usedModel == "" {
				usedModel = llmProv.ModelID()
			}
			out.succeeded = append(out.succeeded, classified{id: sub[i].id, content: sub[i].content, model: usedModel, data: classification})
		}
		return out
	}

	shortOut := runPass(shortBatch, shortSystemPrompt, shortModel)
	longOut := runPass(longBatch, ClassifySystemPrompt, longModel)
	ready := append(shortOut.succeeded, longOut.succeeded...)
	failed := append(shortOut.failed, longOut.failed...)

	if len(ready) == 0 && len(failed) == 0 {
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
	var classifyActions []graph.CommitAction
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
		// Successful classification clears any prior failure tracking
		// so an operator-fixed record can pass cleanly. The outer
		// `if !ok { continue }` guard above already proved the node
		// exists.
		n, _ := e.Graph().GetNode(r.id)
		recordTaskSuccess(e, n, "classify_attempts")
		result.Classified++
		if result.ModelCounts == nil {
			result.ModelCounts = make(map[string]int)
		}
		result.ModelCounts[r.model]++
		classifyActions = append(classifyActions, graph.CommitAction{
			Kind: graph.ActionCurationClassify, RecordID: r.id,
		})
	}

	// Failed records: bump attempts counter, capture the reason for
	// triage, and mark stuck once the threshold is reached. Skipped
	// when MaxClassifyAttempts is 0 (legacy infinite-retry behavior).
	maxAttempts := cfg.LLM.Curation.Retries.MaxClassifyAttempts
	classifyRetry := taskRetryPolicy{
		AttemptsKey:      "classify_attempts",
		ErrorKey:         "last_classify_error",
		StatusKey:        "processing_status",
		StatusValueAtMax: "stuck",
		Max:              maxAttempts,
		TaskName:         "classify",
	}
	for _, f := range failed {
		recordTaskFailure(e, classifyRetry, f.id, f.reason, logger)
	}

	if result.Classified > 0 || (maxAttempts > 0 && len(failed) > 0) {
		e.SaveOrLog("curation: classify", classifyActions...)
	}
	e.Unlock()
}

// generateSummaries adds summary_short to records that lack one.
func generateSummaries(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, maxCostUSD float64, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)

	// Cache the invariant instructions on providers that support it so
	// subsequent calls within the 5-minute TTL reuse the cached block.
	// Falls back to concatenation if caching is disabled or the provider
	// lacks SystemPromptSetter.
	userPromptTemplate := summarizePrompt
	setter, hasSetter := llmProv.(llm.SystemPromptSetter)
	if hasSetter && cfg.LLM.Curation.PromptCachingEnabled {
		setter.SetSystemPrompt(SummarizeSystemPrompt)
		defer setter.SetSystemPrompt("")
	} else {
		userPromptTemplate = SummarizeSystemPrompt + "\n\n" + summarizePrompt
	}

	batchSize := cfg.LLM.Curation.BatchSize
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

	maxSummaryAttempts := cfg.LLM.Curation.Retries.MaxSummaryAttempts
	sumIt := g.NodeIterator()
	for sumIt.Next() {
		n := sumIt.Node()
		id := n.ID
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}
		// Effective curation gate: skip records whose collection
		// memberships resolve to curation=none.
		if EffectiveCurationFor(g, id).Curation == "none" {
			continue
		}
		content := RecordContentFor(g, id)
		summary, hasSummary := n.Properties.GetString("content_short")
		if content == "" {
			continue
		}
		// Skip records that have exhausted their summary retry budget.
		// The selection here is "needs a summary" -- without this guard,
		// a record whose content the LLM consistently can't summarize
		// (oversized, content-policy refusal, persistent empty-after-trim)
		// re-enters every cycle and bills input tokens forever.
		if maxSummaryAttempts > 0 {
			if attempts, ok := n.Properties.GetInt64("summary_attempts"); ok && attempts >= int64(maxSummaryAttempts) {
				continue
			}
		}

		// Single edge walk per node (was: two — once in isChunkNode for
		// Priority 1, again for the section check in Priority 2). Now
		// we enumerate edges once and capture both signals.
		isStructural := false
		isSection := false
		for _, edge := range g.EdgesFrom(id) {
			if !graph.IsStructuralEdge(edge.Type) {
				continue
			}
			isStructural = true
			if edge.Type == "section_of" {
				isSection = true
				break // section_of implies structural; no need to keep scanning
			}
		}

		// Priority 1: non-structural record with no summary.
		if !isStructural && !hasSummary && len(batch) < batchSize {
			batch = append(batch, needsSummary{id: id, content: content})
			continue
		}

		// Priority 2: section node with a truncated summary (existing
		// short is just a content-prefix slice, not a real summary).
		// Chunk nodes hit isStructural but not isSection, so they
		// fall through with no work.
		if isSection && hasSummary && len(summary) >= 150 && len(content) > len(summary) && strings.HasPrefix(content, summary) {
			sectionCandidates = append(sectionCandidates, needsSummary{id: id, content: content})
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

	// Gate on count and cost caps before doing any work. The batch
	// trim below still uses the count cap -- the cost cap only gates
	// "should we run this phase at all", since mid-phase cost tracking
	// would have to pre-estimate token counts.
	if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
		return
	}
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
	type summaryFailure struct {
		id     string
		reason string
	}
	var readySummaries []summarized
	var failedSummaries []summaryFailure

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
			failedSummaries = append(failedSummaries, summaryFailure{
				id:     batch[i].id,
				reason: strutil.TruncateRunes(lr.err.Error(), lastSummaryErrorMaxRunes),
			})
			continue
		}

		summary := strings.TrimSpace(lr.response)
		runes := []rune(summary)
		if len(runes) > 200 {
			summary = string(runes[:200])
		}
		if summary == "" {
			result.Errors++
			failedSummaries = append(failedSummaries, summaryFailure{
				id:     batch[i].id,
				reason: "empty summary after trim",
			})
			continue
		}

		readySummaries = append(readySummaries, summarized{id: batch[i].id, content: batch[i].content, summary: summary})
	}

	if len(readySummaries) == 0 && len(failedSummaries) == 0 {
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
	var summaryActions []graph.CommitAction
	for _, s := range readySummaries {
		n, ok := e.Graph().GetNode(s.id)
		if !ok {
			logger.Debug("summarize node gone", "component", "curation", "record", s.id)
			continue
		}
		e.SetContentProp(s.id, "content_short", s.summary)
		recordTaskSuccess(e, n, "summary_attempts")
		result.SummariesGenerated++
		summaryActions = append(summaryActions, graph.CommitAction{
			Kind: graph.ActionCurationSummary, RecordID: s.id,
		})
	}

	// Failed records: bump attempts counter and capture the reason for
	// triage. Records past MaxSummaryAttempts are skipped at selection
	// time (above) -- no terminal status flip; the selection guard
	// handles exclusion. Skipped when MaxSummaryAttempts is 0 (legacy
	// infinite-retry behavior).
	summarizeRetry := taskRetryPolicy{
		AttemptsKey: "summary_attempts",
		ErrorKey:    "last_summary_error",
		Max:         maxSummaryAttempts,
		TaskName:    "summarize",
	}
	for _, f := range failedSummaries {
		recordTaskFailure(e, summarizeRetry, f.id, f.reason, logger)
	}

	if result.SummariesGenerated > 0 || (maxSummaryAttempts > 0 && len(failedSummaries) > 0) {
		e.SaveOrLog("curation: summarize", summaryActions...)
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
	epistemicMap := make(map[string]int)
	temporalityMap := make(map[string]int)
	// Confidence is a continuous [0,1] field; bucket into low (<0.4),
	// mid (0.4-0.7), high (>=0.7), and "unset" for records that omit
	// confidence entirely. Quartile-style bucketing keeps the
	// fingerprint stable to small drift while still surfacing the
	// kind of bulk reclassification (50 records sliding from
	// speculative-low to well_established-high) that should
	// invalidate the cached manifest summary.
	confidenceMap := make(map[string]int)
	// kwCounts is built inline from the SAME live-record loop that
	// produces totalRecords/typeMap/etc., not from the unfiltered
	// PropIdx().KeywordCounts() — that index includes historical
	// (valid_until-past) records and would defeat the historical-
	// filter cache stability guarantee.
	kwCounts := make(map[string]int)
	var earliest, latest time.Time

	now := time.Now().UTC()
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
		// Skip historical records (valid_until set + in the past).
		// The manifest summarises the CURRENT state of the store;
		// counting superseded records inflates the totals and muddies
		// the per-classification breakdowns. The fingerprint cache
		// remains correctly invalidated when the *current* set
		// changes — supersession adds valid_until, which moves the
		// record out of this count, which changes the fingerprint.
		if vu, ok := n.Properties.GetTimestamp("valid_until"); ok && vu.Before(now) {
			continue
		}
		totalRecords++
		if kt, ok := n.Properties.GetString("knowledge_type"); ok {
			typeMap[kt]++
		}
		if es, ok := n.Properties.GetString("epistemic_status"); ok && es != "" {
			epistemicMap[es]++
		}
		if tp, ok := n.Properties.GetString("temporality"); ok && tp != "" {
			temporalityMap[tp]++
		}
		if cf, ok := n.Properties.GetFloat64("confidence"); ok {
			switch {
			case cf < 0.4:
				confidenceMap["low"]++
			case cf < 0.7:
				confidenceMap["mid"]++
			default:
				confidenceMap["high"]++
			}
		} else {
			confidenceMap["unset"]++
		}
		if kws, ok := n.Properties.GetStringList("content_keywords"); ok {
			for _, kw := range kws {
				kwCounts[kw]++
			}
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

	// histogramString sorts the map by key and renders "k(v),k(v)..."
	// for canonical fingerprinting. Stable across cycles given the
	// same population.
	histogramString := func(m map[string]int) string {
		if len(m) == 0 {
			return ""
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s(%d)", k, m[k]))
		}
		return strings.Join(parts, ",")
	}
	typesStr := histogramString(typeMap)
	epistemicStr := histogramString(epistemicMap)
	temporalityStr := histogramString(temporalityMap)
	confidenceStr := histogramString(confidenceMap)

	earliestStr := "N/A"
	latestStr := "N/A"
	if !earliest.IsZero() {
		earliestStr = earliest.Format("2006-01-02")
	}
	if !latest.IsZero() {
		latestStr = latest.Format("2006-01-02")
	}

	// Compute the state fingerprint hash. Same inputs -> same summary,
	// so we can skip the LLM call when nothing has changed. The
	// epistemic / temporality / confidence histograms are part of the
	// fingerprint because bulk reclassification (e.g. 50 records moving
	// speculative -> well_established) is exactly the kind of store
	// shift the cached manifest summary should NOT survive.
	fp := fmt.Sprintf("records=%d|types=%s|epistemic=%s|temporality=%s|confidence=%s|keywords=%s|span=%s..%s",
		totalRecords,
		typesStr,
		epistemicStr,
		temporalityStr,
		confidenceStr,
		strings.Join(kwStrs, ","),
		earliestStr, latestStr,
	)
	sum := sha256.Sum256([]byte(fp))
	currentHash := hex.EncodeToString(sum[:])

	cacheEnabled := cfg.LLM.Curation.ManifestCacheEnabled
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

	// Negative cache: skip the LLM call when this exact fingerprint
	// has already failed MaxManifestAttempts consecutive cycles. The
	// negative cache clears automatically when the fingerprint
	// changes (store state moved) or when any later success lands.
	maxAttempts := cfg.LLM.Curation.Retries.MaxManifestAttempts
	if cacheEnabled && cache != nil && maxAttempts > 0 &&
		cache.LastFailedHash == currentHash && cache.FailedAttempts >= maxAttempts {
		logger.Info("manifest summary skipped: prior failures on same fingerprint",
			"component", "curation",
			"hash", currentHash[:8],
			"attempts", cache.FailedAttempts,
			"max_attempts", maxAttempts,
		)
		return
	}

	// recordManifestFailure increments the negative-cache counter for
	// `currentHash`. If the previous failure was on a different hash,
	// reset the counter to 1 (fresh budget for the new state).
	recordManifestFailure := func() {
		if !cacheEnabled || cache == nil || maxAttempts <= 0 {
			return
		}
		if cache.LastFailedHash == currentHash {
			cache.FailedAttempts++
			return
		}
		cache.LastFailedHash = currentHash
		cache.FailedAttempts = 1
	}

	// Cache the invariant summarize-the-store instructions.
	userPromptTemplate := manifestSummaryPrompt
	setter, hasSetter := llmProv.(llm.SystemPromptSetter)
	if hasSetter && cfg.LLM.Curation.PromptCachingEnabled {
		setter.SetSystemPrompt(ManifestSystemPrompt)
		defer setter.SetSystemPrompt("")
	} else {
		userPromptTemplate = ManifestSystemPrompt + "\n\n" + manifestSummaryPrompt
	}

	prompt := fmt.Sprintf(userPromptTemplate,
		totalRecords,
		strings.ReplaceAll(typesStr, ",", ", "),
		strings.Join(kwStrs, ", "),
		earliestStr, latestStr,
	)

	model := cfg.ModelForTask(config.TaskManifest)
	resp, err := completeWithModelOrDefault(ctx, llmProv, "manifest", model, prompt)
	result.LLMCalls++
	if err != nil {
		result.Errors++
		logger.Warn("manifest summary LLM error", "component", "curation", "err", err)
		recordManifestFailure()
		return
	}

	summary := strings.TrimSpace(resp)
	// Rune-safe truncation to 500 characters.
	runes := []rune(summary)
	if len(runes) > 500 {
		summary = string(runes[:500])
	}
	if summary == "" {
		// Empty-after-trim is a failure mode of its own: caching an
		// empty summary fails the cache-hit guard at line 1047
		// (`cache.Summary != ""`), so the LLM would be called again
		// next cycle on the same fingerprint. Treat it identically to
		// an LLM error and advance the negative-cache counter.
		result.Errors++
		logger.Warn("manifest summary empty after trim", "component", "curation")
		recordManifestFailure()
		return
	}
	result.ManifestSummary = summary

	// Update the cache so the next cycle with the same fingerprint can
	// skip the LLM call. Clear the negative cache too -- a success on
	// any fingerprint signals the model behaviour is healthy.
	if cacheEnabled && cache != nil {
		cache.Hash = currentHash
		cache.Summary = summary
		cache.LastFailedHash = ""
		cache.FailedAttempts = 0
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
func enrichConceptSyntheses(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, maxCostUSD float64, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)
	if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
		return
	}

	batchSize := cfg.LLM.Curation.Concept.SynthesisBatchSize
	if batchSize <= 0 {
		batchSize = 5
	}
	maxInputTokens := cfg.LLM.Curation.Concept.SynthesisMaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = 8000
	}

	// Cap to remaining LLM budget.
	remaining := maxCalls - result.LLMCalls
	maxConcepts := cfg.LLM.Curation.Concept.MaxPerRun
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
	useCache := hasSystemPrompt && cfg.LLM.Curation.PromptCachingEnabled
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

	coherenceMin := cfg.LLM.Curation.Concept.CoherenceMin

	for _, pc := range pending {
		// Optional coherence pre-filter: skip clusters whose members
		// don't embed near a common centroid. Incoherent clusters tend
		// to produce low-quality syntheses, so filtering them out
		// preserves LLM budget for clusters that benefit. Default
		// threshold is 0 (no filter) so behavior is unchanged unless
		// the user opts in.
		if coherenceMin > 0 {
			cos, n, dimMismatched := meanCosineToCentroid(g, pc.memberIDs)
			if dimMismatched > 0 {
				logger.Warn("concept members skipped due to embedding dimension mismatch",
					"component", "curation",
					"keyword", pc.keyword,
					"mismatched", dimMismatched,
					"used", n,
					"hint", "embedding model likely changed mid-store; run gramaton reembed")
			}
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
			} else if s := RecordContentFor(g, mid); s != "" {
				if len(s) > 200 {
					s = s[:200]
				}
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
		if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
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

		// Determine the batch-level outcome. A non-empty batchFailReason
		// means EVERY concept in this batch failed for the same
		// underlying cause (LLM transport error or top-level parse
		// failure). A successful batch may still have per-concept
		// failures handled in the loop below (short response, empty
		// synthesis at a position).
		var batchFailReason string
		var syntheses []string
		if err != nil {
			result.Errors++
			logger.Warn("concept synthesis batch failed",
				"component", "curation",
				"batch_size", len(batch.concepts),
				"err", err)
			batchFailReason = strutil.TruncateRunes(err.Error(), lastSynthesisErrorMaxRunes)
		} else {
			syntheses = parseBatchSynthesis(resp)
			if syntheses == nil {
				result.Errors++
				logger.Warn("concept synthesis parse failed",
					"component", "curation",
					"batch_size", len(batch.concepts),
					"response_len", len(resp))
				batchFailReason = "parse: response was not a valid JSON array"
			}
		}

		synthesisRetry := taskRetryPolicy{
			AttemptsKey:      "synthesis_attempts",
			ErrorKey:         "last_synthesis_error",
			StatusKey:        "synthesis_status",
			StatusValueAtMax: "stuck",
			Max:              cfg.LLM.Curation.Retries.MaxSynthesisAttempts,
			TaskName:         "synthesize",
		}

		// Phase 1: classify each concept's outcome (success / failReason)
		// without touching the engine. Build the list of texts to embed.
		type conceptApply struct {
			id         string
			keyword    string
			synthesis  string
			shortSum   string
			failReason string
		}
		applies := make([]conceptApply, 0, len(batch.concepts))
		var embedTexts []string
		var embedTargets []int // index into applies for each text
		for i, pc := range batch.concepts {
			a := conceptApply{id: pc.id, keyword: pc.keyword}
			switch {
			case batchFailReason != "":
				a.failReason = batchFailReason
			case i >= len(syntheses):
				a.failReason = "short response: missing synthesis at position"
			case syntheses[i] == "":
				a.failReason = "empty synthesis"
			}
			if a.failReason == "" {
				synthesis := syntheses[i]
				runes := []rune(synthesis)
				if len(runes) > 500 {
					synthesis = string(runes[:500])
				}
				a.synthesis = synthesis
				a.shortSum = conceptShortSummary(synthesis, 200)
				embedTargets = append(embedTargets, len(applies))
				embedTexts = append(embedTexts, synthesis)
			}
			applies = append(applies, a)
		}

		// Phase 2: embed syntheses outside the engine lock so I/O does
		// not stall writers. Concepts otherwise have no embedding until
		// `gramaton reembed` catches up, leaving concept-embedding
		// telemetry and PRF blind.
		var vecs [][]float32
		var modelID string
		if emb := e.Embedder(); emb != nil && len(embedTexts) > 0 {
			modelID = emb.ModelID()
			embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			var embedErr error
			vecs, embedErr = emb.Embed(embedCtx, embedTexts)
			cancel()
			if embedErr != nil {
				logger.Warn("concept synthesis embedding failed; concepts will be back-filled by reembed",
					"component", "curation",
					"batch_size", len(embedTexts),
					"err", embedErr)
				vecs = nil
			}
		}

		// Phase 3: apply syntheses (and embeddings when available)
		// under the engine lock.
		e.Lock()
		hadFailures := false
		var enrichActions []graph.CommitAction
		for ai, a := range applies {
			if a.failReason != "" {
				recordTaskFailure(e, synthesisRetry, a.id, a.failReason, logger)
				hadFailures = true
				continue
			}
			e.SetContentProp(a.id, "content_full", a.synthesis)
			e.SetContentProp(a.id, "content_short", a.shortSum)
			e.SetProp(a.id, "synthesis_status", graph.StringProperty("complete"))
			if vecs != nil {
				for ti, target := range embedTargets {
					if target == ai && ti < len(vecs) {
						vec := vecs[ti]
						e.SetProp(a.id, "embedding_full", graph.VectorProperty(vec))
						e.SetProp(a.id, "embedding_model", graph.StringProperty(modelID))
						e.VecIdx().Add(a.id, vec)
						break
					}
				}
			}
			n, _ := e.Graph().GetNode(a.id)
			recordTaskSuccess(e, n, "synthesis_attempts")
			result.ConceptsCreated++
			enrichActions = append(enrichActions, graph.CommitAction{
				Kind: graph.ActionCurationConceptEnrich, RecordID: a.id,
			})

			logger.Info("concept enriched",
				"component", "curation",
				"keyword", a.keyword,
				"node_id", a.id)
		}
		if result.ConceptsCreated > 0 || (synthesisRetry.Max > 0 && hadFailures) {
			e.SaveOrLog("curation: enrich concepts", enrichActions...)
		}
		e.Unlock()
	}
}

// meanCosineToCentroid computes the mean cosine similarity of member
// records to their centroid, using `embedding_full` as the vector.
// Members without an embedding are skipped silently. Members with a
// dimension mismatch (e.g. embedding model changed mid-store) are
// skipped and counted in `dimMismatched` so the caller can surface it
// — silently dropping mismatched members produced misleadingly-low n
// counts at scale.
//
// Returns (0, n, dimMismatched) when fewer than 2 members have valid
// embeddings (meaningful coherence requires at least two vectors).
// Assumes embeddings are already L2-normalized (which the current
// embedding pipelines produce); the centroid is re-normalized
// defensively.
func meanCosineToCentroid(g *graph.Graph, memberIDs []string) (cos float64, used int, dimMismatched int) {
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
			dimMismatched++
			continue
		}
		vecs = append(vecs, emb)
	}
	if len(vecs) < 2 {
		return 0, len(vecs), dimMismatched
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
		return 0, len(vecs), dimMismatched
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
	return sum / float64(len(vecs)), len(vecs), dimMismatched
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
// Takes the first sentence, capped at maxRunes user-visible characters.
// All bounds are in runes -- the byte-indexed pre-image could split
// multi-byte characters mid-rune for CJK or accented input.
func conceptShortSummary(synthesis string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	// Walk runes, tracking both the rune count and the byte index of
	// each rune so we can return clean substrings on rune boundaries.
	runeIdx := 0
	for i, r := range synthesis {
		if r == '.' && runeIdx > 20 && runeIdx < maxRunes {
			// Include the period. byte position of next rune is
			// i + utf8.RuneLen(r), but for '.' that's i+1.
			return synthesis[:i+1]
		}
		runeIdx++
	}
	// No sentence boundary within limit; cap at rune limit.
	if runeIdx <= maxRunes {
		return synthesis
	}
	// runeIdx > maxRunes: cap at maxRunes runes and trim back to a
	// word boundary if possible.
	capped := strutil.TruncateRunes(synthesis, maxRunes)
	if idx := strings.LastIndexByte(capped, ' '); idx > 0 {
		return capped[:idx]
	}
	return capped
}

// detectContradictions finds records with moderate similarity and uses the
// LLM to determine if they contradict or supersede each other.
func detectContradictions(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, maxCostUSD float64, logger *slog.Logger, dryRun bool) {
	logger = ensureLogger(logger)
	maxChecks := cfg.LLM.Curation.Contradiction.MaxChecks
	if maxChecks <= 0 {
		maxChecks = 5
	}
	minSim := cfg.LLM.Curation.Contradiction.MinSimilarity
	if minSim <= 0 {
		minSim = 0.5
	}
	maxSim := cfg.LLM.Curation.Contradiction.MaxSimilarity
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
	// behavior for the same store across restarts.
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
		contentA := RecordContentFor(e.Graph(), idA)
		if contentA == "" {
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
			//
			// A contradiction_check_skipped edge with attempts <
			// MaxContradictionAttempts is a SOFT skip: the pair stays
			// in the candidate pool, gets retried until it either
			// succeeds (edge replaced by a normal contradicts /
			// supersedes / no_contradiction edge) or hits the threshold
			// (edge becomes a hard skip). Any other edge type is a
			// hard skip immediately.
			maxContradictionAttempts := cfg.LLM.Curation.Retries.MaxContradictionAttempts
			isHardSkip := func(edge *graph.Edge) bool {
				if edge.Type != contradictionCheckSkippedEdge {
					return true
				}
				if maxContradictionAttempts <= 0 {
					// Counter disabled -> the soft-skip edge effectively
					// hard-skips at first failure (legacy behavior).
					return true
				}
				attempts, _ := edge.Properties.GetInt64("attempts")
				return attempts >= int64(maxContradictionAttempts)
			}
			hasEdge := false
			for _, edge := range e.Graph().EdgesFrom(idA) {
				if edge.TargetID == sr.NodeID && isHardSkip(edge) {
					hasEdge = true
					break
				}
			}
			if !hasEdge && cfg.LLM.Curation.Contradiction.CheckReverseEdges {
				for _, edge := range e.Graph().EdgesFrom(sr.NodeID) {
					if edge.TargetID == idA && isHardSkip(edge) {
						hasEdge = true
						break
					}
				}
			}
			if hasEdge {
				continue
			}

			contentB := RecordContentFor(e.Graph(), sr.NodeID)
			if contentB == "" {
				continue
			}

			// Effective contradictions gate: skip the pair if either
			// record's collection memberships resolve to
			// contradictions=off. The knob is additive (creates
			// contradicts edges); most-restrictive on the pair means
			// if either side opts out, no edge is generated.
			if EffectiveCurationFor(e.Graph(), idA).Contradictions == "off" ||
				EffectiveCurationFor(e.Graph(), sr.NodeID).Contradictions == "off" {
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

	// Pairs the LLM successfully checked but reported NO contradiction
	// or supersession. Each gets a "no_contradiction" edge written in
	// the write phase so the hasEdge guard in the next cycle's read
	// phase skips them, allowing the candidate pool to drain. See
	// design-decisions.md D38 for the bug this addresses.
	type checkedNegative struct{ idA, idB string }
	var noContradictions []checkedNegative

	// Pairs whose LLM check failed (transport error or parse error).
	// Each gets a contradiction_check_skipped edge in the write phase
	// with an attempts counter, which the read-phase hasEdge guard
	// honors as a soft-skip until the threshold (then hard-skip).
	type checkedFailure struct {
		idA, idB string
		reason   string
	}
	var failedChecks []checkedFailure

	batchSize := cfg.LLM.Curation.Contradiction.BatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	model := cfg.ModelForTask(config.TaskContradiction)

	if batchSize == 1 {
		// Cache the single-pair instructions. User body is the two records.
		userPromptTemplate := contradictionPrompt
		setter, hasSetter := llmProv.(llm.SystemPromptSetter)
		if hasSetter && cfg.LLM.Curation.PromptCachingEnabled {
			setter.SetSystemPrompt(ContradictionSystemPrompt)
			defer setter.SetSystemPrompt("")
		} else {
			userPromptTemplate = ContradictionSystemPrompt + "\n\n" + contradictionPrompt
		}

		for _, c := range candidates {
			if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
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
				failedChecks = append(failedChecks, checkedFailure{
					idA: c.idA, idB: c.idB,
					reason: strutil.TruncateRunes(err.Error(), lastContradictionErrorMaxRunes),
				})
				continue
			}

			cr, err := parseContradictionResult(resp)
			if err != nil {
				result.Errors++
				logger.Warn("contradiction parse error", "component", "curation", "err", err)
				failedChecks = append(failedChecks, checkedFailure{
					idA: c.idA, idB: c.idB,
					reason: strutil.TruncateRunes("parse: "+err.Error(), lastContradictionErrorMaxRunes),
				})
				continue
			}

			if cr.Relationship == "contradicts" || cr.Relationship == "supersedes" {
				findings = append(findings, detected{
					idA: c.idA, idB: c.idB, contentA: c.contentA,
					relationship: cr.Relationship,
					confidence:   cr.Confidence,
					explanation:  cr.Explanation,
				})
			} else {
				// LLM explicitly evaluated the pair and reported no
				// contradiction/supersession. Mark so next cycle skips it.
				noContradictions = append(noContradictions, checkedNegative{idA: c.idA, idB: c.idB})
			}
		}
	} else {
		// Batched mode: N pairs per LLM call. Use the batch system prompt,
		// which instructs the LLM to return a JSON array with pair_id.
		includeSystemInline := true
		setter, hasSetter := llmProv.(llm.SystemPromptSetter)
		if hasSetter && cfg.LLM.Curation.PromptCachingEnabled {
			setter.SetSystemPrompt(ContradictionBatchSystemPrompt)
			defer setter.SetSystemPrompt("")
			includeSystemInline = false
		}

		for start := 0; start < len(candidates); start += batchSize {
			if cycleBudgetExceeded(ctx, cfg, result, maxCalls, maxCostUSD) {
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
				// Whole batch failed -- every pair in this batch counts
				// as a failed check, sharing the same reason.
				reason := strutil.TruncateRunes(err.Error(), lastContradictionErrorMaxRunes)
				for _, c := range batch {
					failedChecks = append(failedChecks, checkedFailure{
						idA: c.idA, idB: c.idB, reason: reason,
					})
				}
				continue
			}

			batchResults, err := parseContradictionBatchResult(resp)
			if err != nil {
				result.Errors++
				logger.Warn("contradiction batch parse error", "component", "curation", "batch_size", len(batch), "err", err)
				reason := strutil.TruncateRunes("parse: "+err.Error(), lastContradictionErrorMaxRunes)
				for _, c := range batch {
					failedChecks = append(failedChecks, checkedFailure{
						idA: c.idA, idB: c.idB, reason: reason,
					})
				}
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
				} else {
					// LLM explicitly evaluated the pair and reported no
					// contradiction/supersession. Mark so next cycle skips it.
					noContradictions = append(noContradictions, checkedNegative{idA: c.idA, idB: c.idB})
				}
			}
		}
	}

	if len(findings) == 0 && len(noContradictions) == 0 && len(failedChecks) == 0 {
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
		// noContradictions would-be edges: noted in the result but not
		// listed individually in PlannedChanges (deterministic, low-signal).
		result.NoContradictionEdges = len(noContradictions)
		return
	}

	// Write phase: create edges and mark superseded records.
	e.Lock()
	var contradictionActions []graph.CommitAction
	for _, f := range findings {
		if _, ok := e.Graph().GetNode(f.idA); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(f.idB); !ok {
			continue
		}

		// Every checked pair emits two contradiction_check actions
		// (one per endpoint) so gramaton_log filtering by either
		// record finds the commit. The supersedes outcome
		// additionally emits supersede actions so a filter on
		// curation:supersede finds LLM-driven supersessions too.
		contradictionActions = append(contradictionActions,
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: f.idA},
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: f.idB},
		)

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
			contradictionActions = append(contradictionActions,
				graph.CommitAction{Kind: graph.ActionCurationSupersede, RecordID: f.idA},
				graph.CommitAction{Kind: graph.ActionCurationSupersede, RecordID: f.idB},
			)
		}
	}
	// Write no_contradiction edges for pairs the LLM successfully
	// evaluated and found to be non-contradicting. The hasEdge guard in
	// the read phase treats any inter-pair edge as "already handled," so
	// these marks drain the candidate pool across cycles. Without this,
	// negative results leave the pool untouched and the LLM re-asks the
	// same pairs indefinitely. See design-decisions.md D38.
	checkedAt := time.Now().UTC()
	for _, nc := range noContradictions {
		if _, ok := e.Graph().GetNode(nc.idA); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(nc.idB); !ok {
			continue
		}
		props := graph.Properties{
			"checked_at": graph.TimestampProperty(checkedAt),
		}
		if _, err := e.Graph().AddEdge(nc.idA, nc.idB, "no_contradiction", 1.0, props); err != nil {
			logger.Error("failed to add no_contradiction edge",
				"component", "curation", "from", nc.idA, "to", nc.idB, "err", err)
			continue
		}
		result.NoContradictionEdges++
		contradictionActions = append(contradictionActions,
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: nc.idA},
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: nc.idB},
		)
	}

	// Failed-check pairs: increment-or-create a contradiction_check_skipped
	// edge with the attempts counter. The read-phase hasEdge guard above
	// treats this edge as a soft skip until attempts reach the threshold,
	// then a hard skip. Skipped when MaxContradictionAttempts is 0
	// (legacy behavior: pairs re-enter the pool until they succeed).
	maxContradictionAttempts := cfg.LLM.Curation.Retries.MaxContradictionAttempts
	for _, fc := range failedChecks {
		if maxContradictionAttempts <= 0 {
			break
		}
		if _, ok := e.Graph().GetNode(fc.idA); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(fc.idB); !ok {
			continue
		}
		// Look for an existing soft-fail edge between this pair so we
		// can increment its counter rather than stacking duplicate
		// edges. Walk both directions because the hasEdge guard above
		// allowed the pair through if no hard-skip edge existed in
		// EITHER direction; a soft-fail edge could live on A->B or
		// B->A from a prior cycle where the candidate-iteration order
		// happened to land the other way.
		var existingEdge *graph.Edge
		for _, edge := range e.Graph().EdgesFrom(fc.idA) {
			if edge.TargetID == fc.idB && edge.Type == contradictionCheckSkippedEdge {
				existingEdge = edge
				break
			}
		}
		if existingEdge == nil {
			for _, edge := range e.Graph().EdgesFrom(fc.idB) {
				if edge.TargetID == fc.idA && edge.Type == contradictionCheckSkippedEdge {
					existingEdge = edge
					break
				}
			}
		}

		if existingEdge != nil {
			// Increment attempts on the existing edge.
			attempts, _ := existingEdge.Properties.GetInt64("attempts")
			attempts++
			if err := e.Graph().SetEdgeProperty(existingEdge.ID, "attempts", graph.Int64Property(attempts)); err != nil {
				logger.Error("contradiction_check_skipped: SetEdgeProperty attempts",
					"component", "curation", "edge", existingEdge.ID, "err", err)
				continue
			}
			_ = e.Graph().SetEdgeProperty(existingEdge.ID, "last_error", graph.StringProperty(fc.reason))
			_ = e.Graph().SetEdgeProperty(existingEdge.ID, "checked_at", graph.TimestampProperty(checkedAt))
			if attempts >= int64(maxContradictionAttempts) {
				logger.Warn("contradiction: pair locked out after repeated check failures",
					"component", "curation",
					"from", fc.idA, "to", fc.idB,
					"attempts", attempts,
					"max_attempts", maxContradictionAttempts,
					"last_error", fc.reason)
			}
			continue
		}

		// First failure for this pair: create the soft-fail edge.
		props := graph.Properties{
			"attempts":   graph.Int64Property(1),
			"last_error": graph.StringProperty(fc.reason),
			"checked_at": graph.TimestampProperty(checkedAt),
		}
		if _, err := e.Graph().AddEdge(fc.idA, fc.idB, contradictionCheckSkippedEdge, 1.0, props); err != nil {
			logger.Error("failed to add contradiction_check_skipped edge",
				"component", "curation", "from", fc.idA, "to", fc.idB, "err", err)
			continue
		}
	}

	if result.ContradictionsDetected > 0 || result.NoContradictionEdges > 0 ||
		(maxContradictionAttempts > 0 && len(failedChecks) > 0) {
		e.SaveOrLog("curation: contradictions", contradictionActions...)
	}
	e.Unlock()

	if result.ContradictionsDetected > 0 || result.NoContradictionEdges > 0 {
		logger.Info("contradiction detection complete",
			"component", "curation",
			"detected", result.ContradictionsDetected,
			"no_contradiction_edges", result.NoContradictionEdges)
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
			// Byte cap as a fast path; rune-safe trim follows in
			// case the cap landed mid-rune for multi-byte input.
			result.Keywords[i] = strutil.TrimToValidUTF8(kw[:100])
		}
	}

	// Strip LLM tool-use-format leakage from summary_short. The
	// model occasionally emits `</summary_short>` / `<parameter>`
	// fragments inside the JSON string value; see api.SanitizeSummary
	// for the full pattern list. Applied before length truncation so
	// we don't waste budget on garbage bytes. If sanitization empties
	// the field (pure-contamination output), drop it rather than
	// overwrite a potentially-good existing value downstream.
	if result.SummaryShort != "" {
		sanitized := sanitize.Field(result.SummaryShort)
		if sanitized == "" {
			slog.Warn("classify: summary_short was pure structured-output tokens, dropping",
				"component", "curation")
			result.SummaryShort = ""
		} else {
			result.SummaryShort = sanitized
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
