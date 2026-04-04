# gramaton-search

Search the Gramaton knowledge store for relevant records.

## When to Use

- Before answering questions about past decisions, project context,
  architecture, or user preferences
- When the user references something from a prior session
- When you need context beyond the current conversation

## Steps

1. Call `gramaton_search` with the query text and relevant filters:
   - `text`: the search query
   - `top`: number of results (default 10)
   - `temporality`: filter by immutable|durable|temporal|ephemeral
   - `knowledge_type`: filter by episodic|semantic|procedural|conceptual|reference
   - `confidence_min`: minimum confidence (0.0-1.0)
2. Scan `metadata_summary` and `summary_short` in results
3. For relevant results, call `gramaton_inspect` for full content
4. Use the retrieved knowledge to inform your response

## Output

Returns JSON with results sorted by effective_score:
```json
{"results": [
  {"id": "...", "summary_short": "...", "metadata_summary": "...",
   "confidence": 0.9, "temporality": "durable", "effective_score": 0.78}
]}
```
