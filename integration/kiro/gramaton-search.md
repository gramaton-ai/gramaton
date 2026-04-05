# gramaton-search

Search the Gramaton knowledge store for relevant records.

## When to Use

- Before answering questions about past decisions, project context,
  architecture, or user preferences
- When the user references something from a prior session
- When you need context beyond the current conversation

## Steps

1. Call `gramaton_search` with the query text and relevant filters:
   - `text`: search query (optional -- omit for filter-only queries)
   - `top`: number of results (default 10, max 1000)
   - `sort`: created_at|last_accessed|access_count|confidence|importance|content_length|edge_count|staleness
   - `order`: asc or desc (default: desc)
   - `temporality`: filter by immutable|durable|temporal|ephemeral (prefix ! to exclude)
   - `knowledge_type`: filter by episodic|semantic|procedural|conceptual|reference
   - `epistemic_status`: filter by well_established|probable|speculative|contested|refuted
   - `confidence_min`/`confidence_max`: range filter (0.0-1.0)
   - `importance_min`/`importance_max`: range filter (0.0-1.0)
   - `keywords`: array of exact-match tags (all must be present)
   - `missing`: array of field names that must be unset
   - `match`: literal substring search (case-insensitive)
   - `similar_to`: record ID to find similar records
   - `random`: true for random sample
   - `min_edges`/`max_edges`: edge count filter (0 = orphans)
   - `since`: created after date (YYYY-MM-DD)
   - `last_accessed_after`/`last_accessed_before`: access date filter
   - `expires_before`/`expires_after`: validity window filter
2. Scan `metadata_summary` and `summary_short` in results
3. For relevant results, call `gramaton_inspect` for full content
4. Use the retrieved knowledge to inform your response

## Useful Patterns

- Newest: `sort="created_at", top=10`
- Unclassified: `missing=["temporality"]`
- By tag: `keywords=["auth"]`
- Stale: `sort="staleness", order="desc"`
- Orphans: `max_edges=0`
- Literal match: `match="RWMutex"`
- Similar: `similar_to="<id>"`
- Random: `random=true, top=3`
- Store overview: use `gramaton_stats`
- Find duplicates: use `gramaton_duplicates`

## Output

Returns JSON with results and faceted counts:
```json
{"results": [
  {"id": "...", "summary_short": "...", "metadata_summary": "...",
   "confidence": 0.9, "temporality": "durable", "effective_score": 0.78,
   "created_at": "...", "access_count": 5, "edge_count": 3, "staleness": 0.1}
],
 "facets": {"temporality": {"durable": 5}, "knowledge_type": {"episodic": 3}}}
```
