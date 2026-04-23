# Temporal queries

Four tools answer "what happened, when, and what did it look like?"
Pick by axis, not by name-similarity.

## Axis A — commit-timeline ("what changed in the store between X and Y?")

**Tool:** `gramaton_log(since, until, actions, exclude_curation,
include_record_mutations)`

Commit-timeline queries. Use when the question is about DISCRETE
EVENTS over time, not about a specific record.

Examples:
- "What did I close yesterday?" → `gramaton_log(since=yesterday,
  until=today, actions=["resolve","collection_update"],
  include_record_mutations=true)`
- "What changed this week?" → `gramaton_log(since=monday,
  exclude_curation=true)`
- "What did curation touch overnight?" → `gramaton_log(since=last_night,
  until=this_morning)` without `exclude_curation`.

`include_record_mutations=true` inlines per-record summaries so the
answer is a single tool call — no follow-up `gramaton_inspect` per
ID.

## Axis B — record-mutation history ("when did X change?")

**Tool:** `gramaton_history(id, since, until, actions)`

Per-record change log. Use when the question pins a specific record.

Examples:
- "When was record X resolved?" → `gramaton_history(id="01K...",
  actions=["resolve"])`
- "Every modification to record Y in April" → `gramaton_history(
  id="01K...", since="2026-04-01", until="2026-04-30")`

Returns chronological transitions with the commit message per
change. Without a date range, capped at MaxLogTraversal; with a
date range, bounded by the range itself.

## Axis C — record-property queries ("which records have T?")

**Tool:** `gramaton_search` with `since` / `last_accessed_after` /
`expires_before` / `valid_before` filters.

These ask about record-level timestamps (created_at, last_accessed,
valid_until) that are indexed as record properties. Not a temporal-
queries-specific tool — the same search that finds records by
content also filters by time.

## Axis D — point-in-time state ("what did the store look like at T?")

**Tool (today):** `gramaton_collection_items(collection_id, as_of)`

Answers "what was on this list then?" Walks to `CommitAt(as_of)`,
reads the historical edge tree, returns the members that had
`member_of` edges at that commit with their state at the same
commit. Response carries `as_of` + `semantics: point_in_time` so
you know which contract produced the result.

**Tools (future — v0.1.x):** `gramaton_search(as_of=...)`,
`gramaton_inspect(as_of=...)`, `gramaton_stats(as_of=...)`. Bundled
into a coherent time-travel release.

## Anti-patterns

**Don't client-filter by date.** The range params are indexed and
complete. Fetching the last 500 commits and filtering in your own
code misses everything older than the traversal cap (5000 commits,
~17-28 hours on an active store with curation running).

**Don't use `gramaton_collection_items` without `as_of` for history
questions.** HEAD-only returns today's members; `as_of=T` returns
the members at commit CommitAt(T).

**Don't compose `search` + `inspect` per-id for "what changed
yesterday".** Use `gramaton_log(include_record_mutations=true)` —
the record summaries inline in the log response, no fan-out.

**Don't fall out of MCP into shell subprocesses for date filtering.**
The CLI (`gramaton log`, `gramaton history`, `gramaton diff`) has
the same date/action/mutation flags.

## Semantics: point_in_time vs supersede_follow

When a tool answers "records from 12 weeks ago," there are three
possible contracts:

- **point_in_time (default)**: records as they existed at the
  commit. Frozen state.
- **supersede_follow (opt-in via future flag)**: for each record
  from that window, walk `supersedes` edges forward to find the
  currently-valid successor. Useful for retrospective impact
  analysis.
- **current state of those IDs**: not exposed; too easy to
  confuse with the point-in-time answer.

Response shapes name the semantic that produced them so agents
never have to guess.
