package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// rerankWithLLM sends candidate summaries to the LLM and returns them
// reordered by relevance. Returns nil if the LLM call fails (caller
// falls back to original scoring).
func (t *Tool) rerankWithLLM(query string, candidates []scored) []scored {
	if len(candidates) == 0 {
		return nil
	}

	// Build candidate summaries for the prompt. Use content_short or
	// first 200 chars of content_full.
	type candidate struct {
		idx     int
		id      string
		summary string
	}
	var cands []candidate
	for i, sr := range candidates {
		n, ok := t.graph.GetNode(sr.id)
		if !ok {
			continue
		}
		summary := ""
		if s, ok := n.Properties.GetString("content_short"); ok {
			summary = s
		} else if s, ok := n.Properties.GetString("content_full"); ok {
			summary = s
			if len(summary) > 200 {
				summary = summary[:200]
			}
		} else if s, ok := n.Properties.GetString("content"); ok {
			// Session segments use "content" property.
			summary = s
			if len(summary) > 200 {
				summary = summary[:200]
			}
		}
		if summary == "" {
			continue
		}
		cands = append(cands, candidate{idx: i, id: sr.id, summary: summary})
	}

	if len(cands) == 0 {
		return nil
	}

	// Build the prompt.
	var b strings.Builder
	b.WriteString("Given the search query and numbered candidate results below, return the numbers of the most relevant results in order of relevance (most relevant first). Return ONLY a JSON array of numbers, nothing else.\n\n")
	b.WriteString(fmt.Sprintf("Query: %s\n\nCandidates:\n", query))
	for i, c := range cands {
		// Truncate summary to keep prompt size reasonable.
		s := c.summary
		if len(s) > 300 {
			s = s[:300]
		}
		b.WriteString(fmt.Sprintf("[%d] %s\n", i+1, s))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = telemetry.WithTask(ctx, "rerank")

	resp, err := t.reranker.CompleteWithModel(ctx, t.cfg.ModelForTask(config.TaskRerank), b.String())
	if err != nil {
		slog.Warn("rerank LLM call failed, using original ranking",
			"component", "search",
			"err", err)
		return nil
	}

	// Parse the response -- expect a JSON array of 1-based indices.
	resp = strings.TrimSpace(resp)
	// Strip markdown code fences if present.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var inner []string
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				continue
			}
			inner = append(inner, line)
		}
		resp = strings.Join(inner, "\n")
	}

	var indices []int
	if err := json.Unmarshal([]byte(resp), &indices); err != nil {
		slog.Warn("rerank response parse failed",
			"component", "search",
			"err", err,
			"response", resp[:min(200, len(resp))])
		return nil
	}

	// Rebuild scored results in LLM-specified order. Track which
	// original-candidates indices we've already emitted so duplicates
	// in the LLM response don't produce duplicate entries AND so the
	// "unmentioned" pass below can use a simple origIdx lookup.
	// (The previous implementation mixed cands-index and
	// candidates-index in a single map, producing duplicates.)
	emitted := make(map[int]bool)
	var reranked []scored
	for _, idx := range indices {
		i := idx - 1 // 1-based -> 0-based cands index
		if i < 0 || i >= len(cands) {
			continue
		}
		origIdx := cands[i].idx
		if emitted[origIdx] {
			continue
		}
		emitted[origIdx] = true
		reranked = append(reranked, candidates[origIdx])
	}

	// Append any candidates the LLM didn't mention, preserving their
	// original relative order.
	for i, sr := range candidates {
		if !emitted[i] {
			reranked = append(reranked, sr)
		}
	}

	return reranked
}
