package search

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// Decomposer splits complex queries into sub-queries using an LLM.
type Decomposer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// DecomposeQuery splits a complex query into sub-queries using an LLM.
// Returns nil if the query doesn't need decomposition (single concept).
func DecomposeQuery(ctx context.Context, llm Decomposer, query string) []string {
	if llm == nil || query == "" {
		return nil
	}

	prompt := `Split this search query into 2-3 independent sub-queries that each focus on a single concept. If the query is already simple (single concept), return an empty array.

Query: ` + query + `

Respond with JSON only: {"sub_queries": ["query1", "query2"]}
If no decomposition needed: {"sub_queries": []}`

	ctx = telemetry.WithTask(ctx, "decompose")
	resp, err := llm.Complete(ctx, prompt)
	if err != nil {
		return nil
	}

	// Parse response.
	var result struct {
		SubQueries []string `json:"sub_queries"`
	}
	resp = strings.TrimSpace(resp)
	// Strip markdown fences if present.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var clean []string
		for _, l := range lines {
			if strings.HasPrefix(l, "```") {
				continue
			}
			clean = append(clean, l)
		}
		resp = strings.Join(clean, "\n")
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil
	}

	if len(result.SubQueries) <= 1 {
		return nil // No decomposition needed
	}

	return result.SubQueries
}

// MergeResults combines results from multiple sub-queries using RRF.
// Each result set contributes rank-based scores; duplicates are merged.
func MergeResults(resultSets [][]Result, topK int) []Result {
	if len(resultSets) == 0 {
		return nil
	}
	if len(resultSets) == 1 {
		if topK > 0 && topK < len(resultSets[0]) {
			return resultSets[0][:topK]
		}
		return resultSets[0]
	}

	const rrfK = 60

	// Accumulate RRF scores.
	scores := make(map[string]float64)
	records := make(map[string]Result)

	for _, results := range resultSets {
		for rank, r := range results {
			scores[r.ID] += 1.0 / float64(rrfK+rank+1)
			if _, exists := records[r.ID]; !exists {
				records[r.ID] = r
			}
		}
	}

	// Build merged results.
	merged := make([]Result, 0, len(scores))
	maxScore := 0.0
	for _, s := range scores {
		if s > maxScore {
			maxScore = s
		}
	}

	for id, score := range scores {
		r := records[id]
		if maxScore > 0 {
			r.EffectiveScore = score / maxScore
		}
		merged = append(merged, r)
	}

	// Sort by score descending.
	for i := 0; i < len(merged); i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j].EffectiveScore > merged[i].EffectiveScore {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}

	if topK > 0 && topK < len(merged) {
		merged = merged[:topK]
	}

	return merged
}

// ShouldDecompose returns true if the initial results suggest the query
// would benefit from decomposition. Heuristic: if the top result score
// is below a threshold, the query may be multi-concept.
func ShouldDecompose(results []Result, threshold float64) bool {
	if len(results) == 0 {
		return true
	}
	return results[0].EffectiveScore < threshold
}
