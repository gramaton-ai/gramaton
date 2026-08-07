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
- "What did this record used to say, and why did it change?" → read
  the response's `versions` list.

The response carries two views. `versions` is the logical-version
timeline: one entry per real knowledge change (bookkeeping like
re-embeds never mints a version), newest first, each with its
author, the optional `change_note` the writer left, and the
mechanical field diff against the previous version. When
`version_coverage` is present, history may predate the changelog
index -- take it at face value and suggest
`gramaton backfill changelog` if full history matters. `changes` is
the raw commit walk (message-level). Without a date range the walk
is capped at MaxLogTraversal; with a range, bounded by the range.

## Axis C — record-property queries ("which records have T?")

**Tool:** `gramaton_search` with `since` / `last_accessed_after` /
`expires_before` / `valid_before` filters.

These ask about record-level timestamps (created_at, last_accessed,
valid_until) that are indexed as record properties. Not a temporal-
queries-specific tool — the same search that finds records by
content also filters by time.

## Axis D — point-in-time state ("what did the store look like at T?")

**Tools (today):** `gramaton_inspect(id, as_of)` and
`gramaton_collection_items(collection_id, as_of)`

`gramaton_inspect(id, as_of=<date|full commit hash>)` returns the
record's frozen reality at that commit: its properties then, its
one-hop edges then. The commit must be on the current branch's
ancestry -- an off-branch resolution is refused explicitly rather
than silently serving another lineage's state. The response names
its contract (`semantics: point_in_time`, `as_of` carrying the
resolved commit); the live record may say something else NOW.

`gramaton_collection_items(collection_id, as_of)` answers "what was
on this list then?" the same way: historical edge tree, members and
their state at that commit, `semantics: point_in_time` in the
response.

**Still future:** `gramaton_search(as_of=...)` and
`gramaton_stats(as_of=...)`.

## Axis E — content history search ("did we ever know X?")

**Tool:** `gramaton_history_search(text, id?, scope?, budget?,
since?, until?)`

Lexical search over PAST VERSIONS -- knowledge that has since been
revised or deleted. Live search cannot answer "what did we used to
believe about X?"; this axis can. Matching covers each version's
content AND its commit's `change_note`, so the reason a revision
happened is as findable as the revision itself.

Three scopes form a cost ladder -- spend deliberately:

1. **`id` given** -- one record's versions, milliseconds. "What did
   this record say before?"
2. **`candidates` (default)** -- live retrieval nominates the top
   matching records, then their histories are scanned. Sub-second.
   Finds past versions of records that still exist and still match.
3. **`scope: "store"`** -- budgeted scan of EVERY logical version.
   The only scope that finds knowledge revised away entirely
   (including versions of deleted records). Tens of seconds on
   large stores; the response reports coverage honestly ("scanned N
   of M versions; truncated at budget").

Every hit is loudly a past version: labeled `PAST VERSION from
<date>` (or `CURRENT VERSION`), carrying the version commit ready
for `gramaton_inspect(as_of=...)`, the record's live summary for
contrast, and `record_since_deleted` when the record no longer
exists at HEAD.

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

## Semantics: point_in_time vs live state

When a tool answers "records from 12 weeks ago," two contracts
exist:

- **point_in_time (default)**: records as they existed at the
  commit. Frozen state.
- **live state**: what those records say NOW. Records are mutable
  -- content evolves in place -- so the two answers genuinely
  differ. `gramaton_inspect(id)` is the live axis;
  `gramaton_history(id)` shows the transitions between then and
  now.

Response shapes name the semantic that produced them so agents
never have to guess.
