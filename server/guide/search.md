# Search Guide

Gramaton search spans both Memory and Sessions by default.

## How Search Works

1. **Text query**: Vector similarity (Memory only) + BM25 keyword matching (both stores), fused via RRF.
2. **Filter-only**: Omit text to query by metadata fields only.
3. **Results**: Ranked by composite score incorporating similarity, freshness, confidence, and activation.

## Store Origin

Each result includes a `store` field: `"memory"` or `"session"`.
- Memory results have vector + BM25 matching (stronger ranking signal).
- Session results have BM25 only (keyword matching).

## Store Filter

Use `store` parameter to filter by store:
- `"all"` (default): Search both Memory and Sessions.
- `"memory"`: Memory records only.
- `"sessions"`: Session segments only.

## Useful Patterns

- Newest records: `sort="created_at"`
- By tag: `keywords=["auth", "migration"]`
- Stale records: `sort="staleness", order="desc"`
- Orphans: `max_edges=0`
- Literal text: `match="RWMutex"`
- Similar to: `similar_to="<id>"`

## Related Tools

- `gramaton_search`: Primary search tool.
- `gramaton_inspect`: Full record details.
- `gramaton_explore`: Graph traversal from a node.
