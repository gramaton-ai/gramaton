package curation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// BatchResult summarizes a batch classification run.
type BatchResult struct {
	Submitted  int    `json:"submitted"`
	Succeeded  int    `json:"succeeded"`
	Errored    int    `json:"errored"`
	Expired    int    `json:"expired"`
	Applied    int    `json:"applied"`
	BatchID    string `json:"batch_id"`
	DurationMs int64  `json:"duration_ms"`
}

// RunBatchClassification submits all pending records as a single
// Anthropic Message Batch and applies results when complete.
// Requires an Anthropic provider (for the batch API). Falls back
// to sequential processing for non-Anthropic providers.
func RunBatchClassification(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) (*BatchResult, error) {
	logger = ensureLogger(logger)
	start := time.Now()

	// Check if provider supports batch API (Anthropic only).
	anthClient, ok := extractAnthropicClient(llmProv)
	if !ok {
		return runSequentialBatch(ctx, e, llmProv, cfg, logger)
	}

	// Gather all pending records.
	e.RLock()
	pendingIDs := e.PropIdx().Lookup("processing_status", graph.StringProperty("captured"))
	type pending struct {
		id             string
		content        string
		contextSignals string
	}
	var records []pending
	for _, id := range pendingIDs {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		content, ok := n.Properties.GetString("content_full")
		if !ok || content == "" {
			continue
		}
		records = append(records, pending{
			id:             id,
			content:        content,
			contextSignals: buildContextSignals(n),
		})
	}
	e.RUnlock()

	if len(records) == 0 {
		return &BatchResult{}, nil
	}

	// Classification uses effort-based model selection. Short content
	// (below LongClassificationThreshold) runs at classification_short
	// effort; longer content at classification_long effort. Both resolve
	// to concrete model names via LLM.Models. No hardcoded model names
	// in this file -- if the config is incomplete the call will fail
	// loudly rather than silently pin to an out-of-date default.
	longThreshold := cfg.LLMCuration.LongClassificationThreshold
	if longThreshold <= 0 {
		longThreshold = 2000
	}
	shortModel := cfg.ModelForTask(config.TaskClassificationShort)
	longModel := cfg.ModelForTask(config.TaskClassificationLong)
	if shortModel == "" && longModel == "" {
		return nil, fmt.Errorf("batch classification: no model resolved for either classification tier -- configure llm.models.low/medium or llm_curation.classification_*_effort")
	}

	// Build system message for caching.
	systemBlocks := []anthropic.BatchSystemBlock{{
		Type:         "text",
		Text:         ClassifySystemPrompt,
		CacheControl: &anthropic.BatchCacheControl{Type: "ephemeral"},
	}}

	// Remember which model each record was sent to so we can attribute
	// usage correctly on the way back (short-tier vs long-tier map to
	// different models under the effort configuration).
	reqModels := make(map[string]string, len(records))

	requests := make([]anthropic.BatchRequest, len(records))
	for i, rec := range records {
		model := longModel
		if len(rec.content) < longThreshold && shortModel != "" {
			model = shortModel
		}
		if model == "" {
			// One tier resolved, the other didn't -- use whichever we have.
			if shortModel != "" {
				model = shortModel
			} else {
				model = longModel
			}
		}
		reqModels[rec.id] = model
		requests[i] = anthropic.BatchRequest{
			CustomID: rec.id,
			Params: anthropic.BatchParams{
				Model:     model,
				MaxTokens: 4096,
				System:    systemBlocks,
				Messages: []anthropic.BatchMessage{{
					Role:    "user",
					Content: fmt.Sprintf(classifyPrompt, rec.content, rec.contextSignals),
				}},
			},
		}
	}

	// Submit batch.
	logger.Info("batch: submitting", "records", len(requests))
	batchID, err := anthClient.SubmitBatch(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("batch submit: %w", err)
	}
	logger.Info("batch: submitted", "batch_id", batchID, "records", len(requests))

	// Poll for completion with exponential backoff on errors.
	result := &BatchResult{
		Submitted: len(requests),
		BatchID:   batchID,
	}
	pollInterval := 10 * time.Second
	pollErrors := 0
	const maxPollErrors = 10
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(pollInterval):
		}

		status, err := anthClient.PollBatch(ctx, batchID)
		if err != nil {
			pollErrors++
			if pollErrors >= maxPollErrors {
				return result, fmt.Errorf("batch poll: %d consecutive errors, last: %w", pollErrors, err)
			}
			// Exponential backoff: 10s, 20s, 40s, ...
			pollInterval = time.Duration(10<<uint(pollErrors)) * time.Second
			if pollInterval > 5*time.Minute {
				pollInterval = 5 * time.Minute
			}
			logger.Warn("batch: poll error, backing off", "err", err, "retry_in", pollInterval)
			continue
		}
		pollErrors = 0
		pollInterval = 10 * time.Second

		done := status.RequestCounts.Succeeded + status.RequestCounts.Errored +
			status.RequestCounts.Expired + status.RequestCounts.Canceled
		logger.Info("batch: progress",
			"succeeded", status.RequestCounts.Succeeded,
			"errored", status.RequestCounts.Errored,
			"processing", status.RequestCounts.Processing,
			"total", status.RequestCounts.Total())

		if status.ProcessingStatus == "ended" || done == status.RequestCounts.Total() {
			result.Succeeded = status.RequestCounts.Succeeded
			result.Errored = status.RequestCounts.Errored
			result.Expired = status.RequestCounts.Expired
			break
		}
	}

	// Fetch and apply results.
	batchResults, err := anthClient.FetchResults(ctx, batchID)
	if err != nil {
		return result, fmt.Errorf("batch fetch results: %w", err)
	}

	// Account for per-record token usage. The Message Batches API
	// bypasses the Metered wrapper's Complete path, so without this
	// loop classification calls are invisible to llm_usage.json and
	// evade max_calls_per_day / max_cost_usd_per_day caps. If the
	// passed provider isn't wrapped in Metered (tests, non-server
	// callers), metered == nil and recording is a silent no-op.
	metered := findMetered(llmProv)
	if metered != nil {
		for _, br := range batchResults {
			if br.Result.Type != "succeeded" || br.Result.Message == nil {
				// Errored/expired/canceled sub-requests carry no Usage;
				// the batch result counters already capture them for
				// the cycle log.
				continue
			}
			model, ok := reqModels[br.CustomID]
			if !ok {
				// Anthropic echoed a CustomID we didn't submit. Should
				// not happen; log loudly rather than fabricate an
				// attribution that would silently skew per-task totals.
				logger.Warn("batch: result custom_id not in submitted set, skipping metering",
					"component", "curation", "custom_id", br.CustomID)
				continue
			}
			task := "classification_long"
			if model == shortModel {
				task = "classification_short"
			}
			usage := telemetry.CallUsage{
				InputTokens:      br.Result.Message.Usage.InputTokens,
				OutputTokens:     br.Result.Message.Usage.OutputTokens,
				CacheReadTokens:  br.Result.Message.Usage.CacheReadInputTokens,
				CacheWriteTokens: br.Result.Message.Usage.CacheCreationInputTokens,
			}
			metered.RecordCall(model, task, usage, 0, nil)
		}
	}

	e.Lock()
	for _, br := range batchResults {
		if br.Result.Type != "succeeded" || br.Result.Message == nil {
			continue
		}

		// Extract text from response content blocks.
		var text string
		for _, block := range br.Result.Message.Content {
			if block.Type == "text" {
				text += block.Text
			}
		}

		classification, err := parseClassification(text)
		if err != nil {
			logger.Warn("batch: parse error", "record", br.CustomID, "err", err)
			continue
		}

		if _, ok := e.Graph().GetNode(br.CustomID); !ok {
			continue
		}

		applyClassification(e, br.CustomID, classification, shortModel, longModel, longThreshold)
		result.Applied++
	}
	if result.Applied > 0 {
		e.SaveOrLog("curation: batch classify")
	}
	e.Unlock()

	result.DurationMs = time.Since(start).Milliseconds()
	logger.Info("batch: complete",
		"submitted", result.Submitted,
		"succeeded", result.Succeeded,
		"applied", result.Applied,
		"errored", result.Errored,
		"expired", result.Expired,
		"duration_ms", result.DurationMs)

	return result, nil
}

// runSequentialBatch processes pending records sequentially for
// non-batch providers (CLI providers, etc.). Uses the same prompts
// and tiering as batch mode, one call at a time.
//
// Bounded by cfg.LLMCuration.MaxCallsPerRun. Earlier versions
// silently raised this to 100,000, which bypassed the circuit
// breaker and let an admin trigger run unbounded LLM calls against
// a paid provider. Operators who want a larger
// run must raise the configured cap (clamped to 10,000 in
// config.Load).
//
// BatchSize is irrelevant in sequential mode (one call per
// record), but we raise it locally so the inner loop's "select up
// to BatchSize candidates" pre-filter doesn't artificially
// constrain how many records this cycle processes within the cap.
func runSequentialBatch(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) (*BatchResult, error) {
	logger.Info("batch: sequential mode (provider does not support batch API)",
		"max_calls", cfg.LLMCuration.MaxCallsPerRun)
	start := time.Now()

	// BatchSize raise is safe (sequential processing makes it a
	// candidate-selection knob, not a per-call batch). MaxCallsPerRun
	// is honoured as configured.
	savedBatch := cfg.LLMCuration.BatchSize
	if cfg.LLMCuration.MaxCallsPerRun > savedBatch {
		cfg.LLMCuration.BatchSize = cfg.LLMCuration.MaxCallsPerRun
	}

	ar := runAutonomousInner(ctx, e, llmProv, cfg, nil, logger, false)

	cfg.LLMCuration.BatchSize = savedBatch

	return &BatchResult{
		Submitted:  ar.Classified + ar.Errors,
		Succeeded:  ar.Classified,
		Errored:    ar.Errors,
		Applied:    ar.Classified,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// applyClassification sets classification properties on a record.
// Caller must hold the engine write lock. shortModel and longModel are
// the concrete models resolved by the effort tiers; threshold is the
// byte cutoff between short and long.
func applyClassification(e *core.Engine, id string, data *classificationResult, shortModel, longModel string, threshold int) {
	if data.Temporality != "" {
		e.SetProp(id, "temporality", graph.StringProperty(data.Temporality))
	}
	if data.Confidence > 0 {
		e.SetProp(id, "confidence", graph.Float64Property(data.Confidence))
	}
	if data.KnowledgeType != "" {
		e.SetProp(id, "knowledge_type", graph.StringProperty(data.KnowledgeType))
	}
	if data.EpistemicStatus != "" {
		e.SetProp(id, "epistemic_status", graph.StringProperty(data.EpistemicStatus))
	}
	if len(data.Keywords) > 0 {
		e.SetProp(id, "content_keywords", graph.StringListProperty(data.Keywords))
	}
	if data.SummaryShort != "" {
		e.SetContentProp(id, "content_short", data.SummaryShort)
	}
	// content_medium generation removed (D12: single BM25 layer).

	// Record which model classified this record for audit.
	n, ok := e.Graph().GetNode(id)
	if ok {
		content, _ := n.Properties.GetString("content_full")
		model := longModel
		if len(content) < threshold && shortModel != "" {
			model = shortModel
		}
		if model == "" {
			if shortModel != "" {
				model = shortModel
			} else {
				model = longModel
			}
		}
		if model != "" {
			e.SetProp(id, "classified_by", graph.StringProperty(model))
		}
	}

	e.SetProp(id, "processing_status", graph.StringProperty("processed"))
}

// extractAnthropicClient unwraps metered, rate-limited, or other
// wrappers to find an Anthropic client that supports the batch API.
func extractAnthropicClient(p llm.Provider) (*anthropic.Client, bool) {
	// Direct Anthropic client.
	if c, ok := p.(*anthropic.Client); ok {
		return c, true
	}
	// Unwrap metered wrapper.
	if m, ok := p.(*llm.Metered); ok {
		return extractAnthropicClient(m.Inner())
	}
	// Unwrap rate-limited wrapper.
	if rl, ok := p.(*llm.RateLimited); ok {
		return extractAnthropicClient(rl.Inner())
	}
	return nil, false
}

// findMetered walks the provider wrapper chain to locate the Metered
// wrapper so bypass paths (batch API, future streaming) can self-
// report usage. Returns nil when no Metered wrapper is in the chain
// (tests passing raw providers, or server constructed without a
// tracker) -- callers treat nil as "metering not available, skip".
func findMetered(p llm.Provider) *llm.Metered {
	if m, ok := p.(*llm.Metered); ok {
		return m
	}
	if rl, ok := p.(*llm.RateLimited); ok {
		return findMetered(rl.Inner())
	}
	return nil
}

