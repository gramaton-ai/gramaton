package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// rerankGuidance is the shared metadata-usage block for both rerank
// prompt builders (the in-pipeline path and the lock-free
// RerankResults path); one constant keeps the two from drifting.
const rerankGuidance = "Each candidate starts with epistemic metadata: (lifecycle state; epistemic status; temporality; confidence; date; conflict marker). Relevance to the query is the primary criterion; use the metadata as a tiebreaker:\n" +
	"- Prefer current records over superseded, historical, completed, abandoned, or obsolete ones when they cover the same ground.\n" +
	"- When the query asks about status, history, or what changed, a superseding or correcting record is often the best answer even if not current.\n" +
	"- Never rank refuted records as if their claim were true; they matter only when the query is about the refuted claim itself.\n" +
	"- On time-sensitive topics, prefer recent high-confidence records over old low-confidence ones. A date labeled 'updated' is a revision date -- treat it, not the record's age, as its recency.\n" +
	"- 'in conflict with N record(s)' marks a live unresolved contradiction: when both sides of a conflict match the query, rank both highly rather than suppressing either side.\n\n"

// candidateMeta renders the compact epistemic-metadata prefix for one
// candidate line in the rerank prompt: lifecycle state, then any of
// epistemic status, temporality, confidence, date, and a live-conflict
// marker. The reranked order replaces the composite-score order
// outright, so this prefix is the only channel through which record
// lifecycle can still influence the final ordering.
func candidateMeta(props graph.Properties, now time.Time, conflicts int) string {
	parts := make([]string, 0, 6)
	state := "current"
	if res, ok := props.GetString("resolution"); ok && res != "" {
		state = res
	} else if until, ok := props.GetTimestamp("valid_until"); ok && until.Before(now) {
		state = "historical"
	}
	parts = append(parts, state)
	if es, ok := props.GetString("epistemic_status"); ok && es != "" {
		parts = append(parts, es)
	}
	if tmp, ok := props.GetString("temporality"); ok && tmp != "" {
		parts = append(parts, tmp)
	}
	if c, ok := props.GetFloat64("confidence"); ok {
		parts = append(parts, fmt.Sprintf("conf %.1f", c))
	}
	// The revision date, when present, IS the record's recency -- a
	// revised record must not read as old just because it was created
	// long ago (mirrors the scoring freshness anchor).
	if upd, ok := props.GetTimestamp("updated_at"); ok {
		parts = append(parts, "updated "+upd.UTC().Format("2006-01-02"))
	} else if created, ok := props.GetTimestamp("created_at"); ok {
		parts = append(parts, created.UTC().Format("2006-01-02"))
	}
	if conflicts > 0 {
		parts = append(parts, fmt.Sprintf("in conflict with %d record(s)", conflicts))
	}
	return "(" + strings.Join(parts, "; ") + ")"
}

// rerankWithLLM sends candidate summaries, each prefixed with its
// epistemic metadata, to the LLM and returns them reordered by
// relevance. Returns nil if the LLM call fails (caller falls back to
// original scoring).
func (t *Tool) rerankWithLLM(query string, candidates []scored) []scored {
	if len(candidates) == 0 {
		return nil
	}

	// Build candidate summaries for the prompt. Use content_short or
	// first 200 chars of content_full.
	type candidate struct {
		idx     int
		id      string
		meta    string
		summary string
	}
	now := time.Now().UTC()
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
		conflicts := len(ConflictingRecordIDs(t.graph, sr.id))
		cands = append(cands, candidate{idx: i, id: sr.id, meta: candidateMeta(n.Properties, now, conflicts), summary: summary})
	}

	if len(cands) == 0 {
		return nil
	}

	// Build the prompt.
	var b strings.Builder
	b.WriteString("Given the search query and numbered candidate results below, return the numbers of the most relevant results in order of relevance (most relevant first). Return ONLY a JSON array of numbers, nothing else.\n\n")
	b.WriteString(rerankGuidance)
	b.WriteString(fmt.Sprintf("Query: %s\n\nCandidates:\n", query))
	for i, c := range cands {
		// Truncate summary to keep prompt size reasonable.
		s := c.summary
		if len(s) > 300 {
			s = s[:300]
		}
		b.WriteString(fmt.Sprintf("[%d] %s %s\n", i+1, c.meta, s))
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

// resultMeta renders the same epistemic prefix as candidateMeta, but
// from a Result's copied fields -- no graph access, so the caller
// can hold NO engine lock. Kept field-for-field in step with
// candidateMeta; the rerank prompt tests pin both shapes.
func resultMeta(r Result, now time.Time) string {
	parts := make([]string, 0, 6)
	state := "current"
	if r.Resolution != "" {
		state = r.Resolution
	} else if r.ValidUntil != "" {
		if until, err := time.Parse(time.RFC3339, r.ValidUntil); err == nil && until.Before(now) {
			state = "historical"
		}
	}
	parts = append(parts, state)
	if r.EpistemicStatus != "" {
		parts = append(parts, r.EpistemicStatus)
	}
	if r.Temporality != "" {
		parts = append(parts, r.Temporality)
	}
	if r.Confidence != 0 {
		parts = append(parts, fmt.Sprintf("conf %.1f", r.Confidence))
	}
	if r.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil {
			parts = append(parts, "updated "+t.UTC().Format("2006-01-02"))
		}
	} else if r.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			parts = append(parts, t.UTC().Format("2006-01-02"))
		}
	}
	if r.ConflictCount > 0 {
		parts = append(parts, fmt.Sprintf("in conflict with %d record(s)", r.ConflictCount))
	}
	return "(" + strings.Join(parts, "; ") + ")"
}

// RerankResults reorders the head of an assembled result set via the
// LLM, using ONLY data already copied into Result -- deliberately
// lock-free so the api layer can run the network call after
// releasing the engine lock (the in-pipeline rerank stage holds the
// read lock for its whole duration, which is exactly the
// lock-across-I/O shape the caller is avoiding by setting SkipRerank
// and invoking this instead). Returns results unchanged when
// reranking is disabled, no reranker is wired, the call fails, or
// there is nothing to rank.
func (t *Tool) RerankResults(query string, results []Result) []Result {
	if query == "" || t.reranker == nil || !t.cfg.LLM.Rerank.Enabled || len(results) == 0 {
		return results
	}
	n := t.cfg.LLM.Rerank.Candidates
	if n <= 0 {
		n = 50
	}
	if n > len(results) {
		n = len(results)
	}
	head := results[:n]

	now := time.Now().UTC()
	type candidate struct {
		idx     int
		meta    string
		summary string
	}
	var cands []candidate
	for i, r := range head {
		// buildResult fills RerankSnippet from summary or content;
		// fall back to SummaryShort for Results constructed elsewhere.
		s := r.RerankSnippet
		if s == "" {
			s = r.SummaryShort
		}
		if s == "" {
			continue
		}
		if len(s) > 300 {
			s = s[:300]
		}
		cands = append(cands, candidate{idx: i, meta: resultMeta(r, now), summary: s})
	}
	if len(cands) == 0 {
		return results
	}

	var b strings.Builder
	b.WriteString("Given the search query and numbered candidate results below, return the numbers of the most relevant results in order of relevance (most relevant first). Return ONLY a JSON array of numbers, nothing else.\n\n")
	b.WriteString(rerankGuidance)
	b.WriteString(fmt.Sprintf("Query: %s\n\nCandidates:\n", query))
	for i, c := range cands {
		b.WriteString(fmt.Sprintf("[%d] %s %s\n", i+1, c.meta, c.summary))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = telemetry.WithTask(ctx, "rerank")
	resp, err := t.reranker.CompleteWithModel(ctx, t.cfg.ModelForTask(config.TaskRerank), b.String())
	if err != nil {
		slog.Warn("rerank LLM call failed, using original ranking", "component", "search", "err", err)
		return results
	}
	indices, ok := parseRerankIndices(resp)
	if !ok {
		return results
	}

	emitted := make(map[int]bool)
	reranked := make([]Result, 0, len(results))
	for _, idx := range indices {
		i := idx - 1
		if i < 0 || i >= len(cands) {
			continue
		}
		orig := cands[i].idx
		if emitted[orig] {
			continue
		}
		emitted[orig] = true
		reranked = append(reranked, head[orig])
	}
	for i, r := range head {
		if !emitted[i] {
			reranked = append(reranked, r)
		}
	}
	return append(reranked, results[n:]...)
}

// parseRerankIndices extracts the 1-based index array from an LLM
// response, tolerating markdown fences.
func parseRerankIndices(resp string) ([]int, bool) {
	resp = strings.TrimSpace(resp)
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
		slog.Warn("rerank response parse failed", "component", "search",
			"err", err, "response", resp[:min(200, len(resp))])
		return nil, false
	}
	return indices, true
}
