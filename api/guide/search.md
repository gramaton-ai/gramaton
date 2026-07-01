# Search Guide

Gramaton search spans both Memory and Sessions by default. Choose
your parameters based on the **question you're asking**, not just
by defaulting.

## How Search Works

1. **Text query**: Vector similarity (Memory only) + BM25 keyword
   matching (both stores), fused via RRF.
2. **Filter-only**: Omit text to query by metadata fields only.
3. **Results**: Ranked by composite score combining similarity,
   freshness, confidence, and activation.
4. **Store origin**: Each result includes a `store` field --
   `"memory"` (decision-grade, vector-searchable) or `"session"`
   (broader conversational thread, BM25 only).

## Choosing the Store Filter by Question Type

| You're asking... | Best store filter |
|---|---|
| "What did we decide about X?" | default (both) or `"memory"` |
| "What's our current position?" | default (supersession edges in Memory handle this) |
| "Why did we decide Y?" | default, then `gramaton_explore` the match |
| "What did we try that didn't work?" | `"sessions"` or default -- dead ends captured with `epistemic_status: refuted` |
| "What's still open?" | `"sessions"` -- open questions stay there as Session-only |
| "When did we discuss X?" | `"sessions"` -- session segments carry timestamps + topic names |
| "What does user prefer?" | `"memory"` -- preferences are durable knowledge |

Default = both. Narrow only when you know what you want.

## Choosing the Query Type

- **Text** (vector + BM25, RRF-fused): for semantic questions. Best
  recall on "the idea of X" even when the exact words differ.
- **`match`** (literal substring, case-insensitive): for "I know the
  exact string appears somewhere." Faster, no embedding.
- **`similar_to`** (ID-based): "records like this one." Reuses the
  stored embedding as the query vector.
- **Filter-only** (no text): for "show me everything that's
  speculative" or "records missing a classification" -- structural
  queries.

## Pagination

Search returns a paged response keyed to a snapshot of the ranked
candidate set:

- `page`, `page_size` -- the slice you got back. Default page_size
  is 20; max is 100.
- `total` -- size of the candidate set (capped server-side at
  `search.pagination.candidate_cap`, default 500, hard ceiling 1000).
- `next_cursor` -- opaque token; pass back as `cursor` to get the
  next page.
- `query_id` -- ULID identifying the snapshot; use for telemetry
  or session-attached pagination flows.
- `pages` -- a table of `{page, cursor}` entries for every page in
  the snapshot. Random access (jump to page 5) and parallel
  fan-out (subagents process non-adjacent pages) both work via
  this table.

Snapshots evict after `search.pagination.snapshot_ttl` (default
20m). An expired cursor returns
`{error: "snapshot_expired"}` -- re-run the original query to
materialize a new snapshot.

When you call back with `cursor`, the cursor wins: any `text`,
`match`, `filter`, etc. you pass alongside is ignored. The response
echoes back `ignored_params` listing what was dropped so the caller
knows. To change the filters, omit `cursor` and run a fresh query.

If you genuinely need every record (more than `candidate_cap`),
switch to `gramaton_export` -- same filter set, no candidate cap,
exhaustive. See "Exporting" below.

## Exporting

`gramaton_export` runs the same filters as `gramaton_search`
(text, match, store, keywords, temporality, knowledge_type, since,
etc.) but is exhaustive: no candidate cap, no top-N truncation, no
snapshot TTL.

Format is controlled by `--format`:

- `jsonl` (default) -- one JSON object per line; streaming-friendly.
- `json` -- a single parseable JSON array. Useful for `jq` and
  one-shot tools.
- `csv` -- comma-separated with a header row.
- `markdown` -- human-readable.

With no filters, the export is a full-store dump. Use this when
search pagination's `total` indicates the full result set is too
large to page through, or when the caller wants an offline
snapshot.

## Useful Patterns

- Newest records: `sort="created_at"`
- Created in a window: `since="2026-04-01", until="2026-04-30"`
- Recently accessed: `last_accessed_after="2026-04-15"`
- Asserted before a date: `valid_before="2026-04-01"` (when the source claim was made)
- Unclassified records: `missing=["temporality"]`
- By tag: `keywords=["auth", "migration"]`
- Stale (unaccessed): `sort="staleness", order="desc"`
- Orphans (no edges): `max_edges=0`
- Literal substring: `match="RWMutex"`
- Similar to a record: `similar_to="<id>"`
- Random sample: `random=true, top=3`
- Heavily connected: `min_edges=3, sort="edge_count"`
- Expiring soon: `expires_before="2026-04-30"`
- Exclude refuted: `epistemic_status="!refuted"`
- Session-only: `store="sessions"`
- Recent compaction nudges: filter by `created_at` after the nudge
  timestamp

## Filtering by Confidence and Status

Use these filters alongside text to weight quality:

- `confidence_min=0.7`: trust the top-tier results.
- `epistemic_status="!refuted"`: exclude known-wrong claims.
- `resolution="unresolved"`: still-open items only.
- `include_historical=false` (default): superseded records are
  excluded unless you ask for them.

Agents that don't filter on these end up with refuted and stale
content competing for top-K slots.

## Related Tools

- `gramaton_search`: Primary search tool.
- `gramaton_inspect`: Full record details for a match.
- `gramaton_explore`: Graph traversal from a match (follow
  `justifies`, `discusses`, `supersedes` edges for context).
- `gramaton_duplicates`: Find near-duplicate records (cosine ≥ 0.92).
- `gramaton_guide(topic="metadata")`: Which metadata to filter on
  for which question types.
- `gramaton_guide(topic="temporal-queries")`: When you need "what
  changed", "when did X happen", or "what did the store look like
  at time T", switch to `gramaton_log` / `gramaton_diff` /
  `gramaton_history`. Search itself is the live (latest-only) axis.
