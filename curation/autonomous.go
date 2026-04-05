package curation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/llm"
)

// AutonomousResult summarizes what an LLM curation cycle did.
type AutonomousResult struct {
	Classified         int `json:"classified"`
	SummariesGenerated int `json:"summaries_generated"`
	ConceptsCreated    int `json:"concepts_created"`
	Errors             int `json:"errors"`
	LLMCalls           int `json:"llm_calls"`
}

// RunAutonomous performs LLM-powered curation tasks.
// Caller must NOT hold any lock.
func RunAutonomous(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, logger *slog.Logger) *AutonomousResult {
	result := &AutonomousResult{}
	maxCalls := cfg.LLMCuration.MaxCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 20
	}

	classifyPending(ctx, e, llmProv, cfg, result, maxCalls, logger)
	generateSummaries(ctx, e, llmProv, cfg, result, maxCalls, logger)

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
func classifyPending(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger) {
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
		id   string
		data *classificationResult
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

		ready = append(ready, classified{id: rec.id, data: classification})
	}

	// Batch write: one lock acquisition, one commit.
	if len(ready) > 0 {
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
}

// generateSummaries adds summary_short to records that lack one.
func generateSummaries(ctx context.Context, e *core.Engine, llmProv llm.Provider, cfg config.Config, result *AutonomousResult, maxCalls int, logger *slog.Logger) {
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

		readySummaries = append(readySummaries, summarized{id: rec.id, summary: summary})
	}

	if len(readySummaries) > 0 {
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
