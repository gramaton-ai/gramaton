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

## Useful Patterns

- Newest records: `sort="created_at"`
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
