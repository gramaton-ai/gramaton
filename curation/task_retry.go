package curation

import (
	"log/slog"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// taskRetryPolicy describes the per-task retry-bound write contract
// shared by classify, summarize, concept-synthesis, and any future
// LLM-driven curation phase that picks records by a property and
// transitions on success. It captures the four pieces of state each
// phase needs to track persistent failures:
//
//   - AttemptsKey: the Int64 property that holds the consecutive-failure
//     count (e.g. "classify_attempts", "summary_attempts").
//   - ErrorKey: the String property that holds the truncated last
//     failure reason (e.g. "last_classify_error", "last_summary_error").
//   - StatusKey + StatusValueAtMax: the optional terminal-state flip.
//     When both are set, recordTaskFailure flips StatusKey =
//     StatusValueAtMax once attempts >= Max -- typically the same
//     property the phase's selection guard tests, so a flipped record
//     auto-excludes from future cycles. Empty values mean the phase
//     uses a numeric attempts threshold for its selection guard
//     instead of a status flip (e.g. summarize, where the guard is
//     `summary_attempts >= max` directly).
//   - Max: the threshold; 0 disables the entire counter (legacy
//     infinite-retry behavior, preserved for opt-out).
//   - TaskName: log-line context.
type taskRetryPolicy struct {
	AttemptsKey      string
	ErrorKey         string
	StatusKey        string
	StatusValueAtMax string
	Max              int
	TaskName         string
}

// recordTaskFailure is called inside the engine write lock for a
// record whose curation task failed. It increments AttemptsKey,
// stores ErrorKey = reason, and (when StatusKey/StatusValueAtMax are
// set) flips the status field at threshold. No-op when policy.Max <=
// 0, preserving the legacy infinite-retry behavior for operators who
// want it.
//
// Safe to call when the node has been deleted between failure capture
// and the write phase -- the missing-node case logs at Debug and
// returns without writing.
func recordTaskFailure(e *core.Engine, policy taskRetryPolicy, id, reason string, logger *slog.Logger) {
	if policy.Max <= 0 {
		return
	}
	n, ok := e.Graph().GetNode(id)
	if !ok {
		logger.Debug("task failure: node gone",
			"component", "curation",
			"task", policy.TaskName,
			"record", id)
		return
	}
	var attempts int64
	if v, ok := n.Properties.GetInt64(policy.AttemptsKey); ok {
		attempts = v
	}
	attempts++
	e.SetProp(id, policy.AttemptsKey, graph.Int64Property(attempts))
	e.SetProp(id, policy.ErrorKey, graph.StringProperty(reason))

	if attempts < int64(policy.Max) {
		return
	}
	if policy.StatusKey != "" && policy.StatusValueAtMax != "" {
		e.SetProp(id, policy.StatusKey, graph.StringProperty(policy.StatusValueAtMax))
		logger.Warn("curation: marking record stuck after repeated failures",
			"component", "curation",
			"task", policy.TaskName,
			"record", id,
			"attempts", attempts,
			"max_attempts", policy.Max,
			"last_error", reason)
		return
	}
	logger.Warn("curation: record will be skipped after repeated failures",
		"component", "curation",
		"task", policy.TaskName,
		"record", id,
		"attempts", attempts,
		"max_attempts", policy.Max,
		"last_error", reason)
}

// recordTaskSuccess clears the AttemptsKey counter on a record whose
// task has just succeeded. The check-then-set guard avoids index
// churn for happy-path records that never failed (the property has
// never been written, so no SetProp is needed).
//
// Safe to call inside the engine write lock. Caller passes the node
// already in hand -- the success loop's outer existence guard has
// proved the node exists, so we don't re-fetch.
func recordTaskSuccess(e *core.Engine, n *graph.Node, attemptsKey string) {
	if _, has := n.Properties.GetInt64(attemptsKey); has {
		e.SetProp(n.ID, attemptsKey, graph.Int64Property(0))
	}
}
