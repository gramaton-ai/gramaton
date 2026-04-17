# Curation Guide

The server runs background curation on a configurable interval
(`curation.interval` in config, default 1 minute). The pipeline has
two halves: a deterministic one that always runs, and an autonomous
one that runs when an LLM is configured.

## Deterministic Pipeline (always runs)

- **Lifecycle transitions**: expire stale ephemeral/temporal records,
  archive concluded ones.
- **Orphan linking**: connect unlinked records to similar ones via
  auto-generated edges.
- **Duplicate consolidation**: auto-supersession for Memory records
  where cosine ≥ 0.92.
- **Concept candidate detection**: surface recurring keywords that
  could graduate to concept nodes.
- **Store manifest computation**: topic/theme summary across the
  store.

## Autonomous Pipeline (when LLM is configured)

- **Record classification**: captured -> processed for records
  lacking full metadata. Uses the question-type-driven classification
  from `gramaton_guide(topic="metadata")`.
- **Contradiction detection**: flags semantically similar Memory
  records that contradict each other.
- **Concept synthesis**: promote candidates to concept nodes and
  link evidence.
- **Auto-summarization**: generates `content_short` (the embedding-
  ready semantic anchor, ~750 chars) for records missing it. The
  summarize prompt is tuned to produce substance, not taglines.

## Scope: What Curation Touches

- **Memory records**: full pipeline applies. Classification,
  supersession, contradiction detection, concept synthesis.
- **Session segments** (`knowledge_type="segment"`): deterministic
  lifecycle only. Skipped for:
  - TF-IDF observation extraction (they were already extracted by
    the session LLM; re-extracting produces extraction-of-extraction
    noise).
  - Auto-supersession (Session segments are append-only snapshots;
    they don't supersede each other).
- **Collection items** and **container nodes** (Session, Topic,
  Collection): excluded from search and from curation lifecycle
  transitions.

## Tools

- `gramaton_curation(action="status")`: Check curation state,
  pending counts, last cycle timestamp.
- `gramaton_curation(action="trigger")`: Trigger a cycle manually.
- `gramaton_curation(action="dry_run")`: Preview changes without
  applying. Useful for reviewing classification decisions before
  they land.
- `gramaton_curation(action="batch")`: Batch-classify all pending
  records via LLM in one call.

## Piggyback Curation

Every response includes a `curation` field:

```json
{"curation": {"pending_count": 14, "overdue": true, "autonomous": false}}
```

When `autonomous: false` (no server LLM configured) and
`overdue: true`, agents can classify pending records directly once
per session at a natural breakpoint:

1. `gramaton_pending()` -- get the list.
2. For each record: `gramaton_inspect` -> read content ->
   `gramaton_classify` with metadata chosen using the
   `gramaton_guide(topic="metadata")` heuristics.
3. Search for related records and link them via `gramaton_link`.

When `autonomous: true`, the server handles this. Do not duplicate.

## Related Tools and Topics

- `gramaton_pending`: list records awaiting classification.
- `gramaton_classify`: classify a pending record.
- `gramaton_duplicates`: preview what auto-supersession will catch.
- `gramaton_guide(topic="metadata")`: field-by-field classification
  heuristics.
