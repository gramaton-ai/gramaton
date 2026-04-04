# gramaton-search

Search the Gramaton knowledge store for relevant records.

## When to Use

- Before answering questions about past decisions, project context,
  architecture, or user preferences
- When the user references something from a prior session
- When you need context beyond the current conversation

## Steps

1. Run `gramaton search "<query>" --top 5` with the user's question
   as the query text
2. Apply metadata filters if the question implies constraints:
   - `--confidence-min 0.7` for high-confidence results
   - `--temporality durable` for stable knowledge
   - `--knowledge-type procedural` for how-to questions
3. Scan `metadata_summary` and `summary_short` in results
4. For relevant results, run `gramaton inspect <id>` for full content
5. Use the retrieved knowledge to inform your response

## Output

Returns JSON array of results sorted by effective_score:
```json
[{"id": "...", "summary_short": "...", "metadata_summary": "...",
  "confidence": 0.9, "temporality": "durable", "effective_score": 0.78}]
```
