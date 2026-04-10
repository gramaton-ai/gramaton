package curation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
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

	// Build batch requests with model tiering.
	lightModel := cfg.LLMCuration.LightModel
	lightThreshold := cfg.LLMCuration.LightModelThreshold
	if lightThreshold <= 0 {
		lightThreshold = 2000
	}
	defaultModel := cfg.LLM.Model
	if defaultModel == "" {
		defaultModel = "claude-sonnet-4-6"
	}

	// Build system message for caching.
	systemBlocks := []anthropic.BatchSystemBlock{{
		Type:         "text",
		Text:         ClassifySystemPrompt,
		CacheControl: &anthropic.BatchCacheControl{Type: "ephemeral"},
	}}

	requests := make([]anthropic.BatchRequest, len(records))
	for i, rec := range records {
		model := defaultModel
		if lightModel != "" && len(rec.content) < lightThreshold {
			model = lightModel
		}
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
	batchID, err := anthClient.SubmitBatch(requests)
	if err != nil {
		return nil, fmt.Errorf("batch submit: %w", err)
	}
	logger.Info("batch: submitted", "batch_id", batchID, "records", len(requests))

	// Poll for completion.
	result := &BatchResult{
		Submitted: len(requests),
		BatchID:   batchID,
	}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(10 * time.Second):
		}

		status, err := anthClient.PollBatch(batchID)
		if err != nil {
			logger.Warn("batch: poll error", "err", err)
			continue
		}

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
	batchResults, err := anthClient.FetchResults(batchID)
	if err != nil {
		return result, fmt.Errorf("batch fetch results: %w", err)
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

		applyClassification(e, br.CustomID, classification, defaultModel, lightModel, lightThreshold)
		result.Applied++
	}
	if result.Applied > 0 {
		e.Save("curation: batch classify")
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

// runSequentialBatch processes all pending records sequentially for
// non-batch providers (CLI providers, etc.). Uses the same prompts
// and tiering as batch mode, just one call at a time.
func runSequentialBatch(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) (*BatchResult, error) {
	logger.Info("batch: sequential mode (provider does not support batch API)")
	start := time.Now()

	// Run autonomous classification with a very large batch size.
	savedBatch := cfg.LLMCuration.BatchSize
	savedMax := cfg.LLMCuration.MaxCallsPerRun
	cfg.LLMCuration.BatchSize = 100000
	cfg.LLMCuration.MaxCallsPerRun = 100000

	ar := runAutonomousInner(ctx, e, llmProv, cfg, logger, false, nil)

	cfg.LLMCuration.BatchSize = savedBatch
	cfg.LLMCuration.MaxCallsPerRun = savedMax

	return &BatchResult{
		Submitted:  ar.Classified + ar.Errors,
		Succeeded:  ar.Classified,
		Errored:    ar.Errors,
		Applied:    ar.Classified,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// applyClassification sets classification properties on a record.
// Caller must hold the engine write lock.
func applyClassification(e *core.Engine, id string, data *classificationResult, defaultModel, lightModel string, threshold int) {
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
	if data.SummaryMedium != "" {
		e.SetContentProp(id, "content_medium", data.SummaryMedium)
	}

	// Determine which model classified this record.
	n, ok := e.Graph().GetNode(id)
	if ok {
		content, _ := n.Properties.GetString("content_full")
		model := defaultModel
		if lightModel != "" && len(content) < threshold {
			model = lightModel
		}
		e.SetProp(id, "classified_by", graph.StringProperty(model))
	}

	e.SetProp(id, "processing_status", graph.StringProperty("processed"))
}

// extractAnthropicClient unwraps rate-limited or other wrappers to
// find an Anthropic client that supports the batch API.
func extractAnthropicClient(p llm.Provider) (*anthropic.Client, bool) {
	// Direct Anthropic client.
	if c, ok := p.(*anthropic.Client); ok {
		return c, true
	}
	// Unwrap rate-limited wrapper.
	if rl, ok := p.(*llm.RateLimited); ok {
		return extractAnthropicClient(rl.Inner())
	}
	return nil, false
}

// ensureLogger is defined in autonomous.go but we re-declare for
// compilation safety. This is a no-op if autonomous.go is present.
func init() {
	_ = strings.TrimSpace // avoid unused import warning
}
