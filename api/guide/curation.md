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
- **Concept candidate detection**: surface recurring keywords that
  could graduate to concept nodes.
- **Store manifest computation**: topic/theme summary across the
  store.

Curation never merges or supersedes records. Near-duplicate
prevention happens at save time (the save-guard hold; see
`gramaton_guide(topic="save")`), and consolidation of existing
records is a deliberate agent action via `gramaton_update` +
`gramaton_resolve`.

## Autonomous Pipeline (when LLM is configured)

- **Record classification**: captured -> processed for records
  lacking full metadata. Uses the question-type-driven classification
  from `gramaton_guide(topic="metadata")`.
- **Contradiction detection**: flags semantically similar records
  that contradict each other. A confirmed contradiction creates a
  `contradicts` edge and sets `epistemic_status: contested` on both
  records (never downgrading `well_established`); search and inspect
  surface a "CONFLICTS with record(s)" line in `metadata_summary`.
  A content update to either record reopens the question: the edges
  are cleared and the pair is re-evaluated.
- **Concept synthesis**: promote candidates to concept nodes and
  link evidence.
- **Auto-summarization**: generates `content_short` (the embedding-
  ready semantic anchor, ~750 chars) for records missing it, and
  refreshes summaries flagged stale by content updates
  (`summary_refresh_pending`).
- **Observation extraction**: pulls structured observations from
  long records.

## Stage → knob mapping

Each pipeline stage reads exactly one collection-level knob to
decide whether to run on a given record. The knobs are documented
in `gramaton_guide(topic="collections")` under "Behaviour fields".
Memory orphan records resolve to the memory-store defaults
(`curation=standard, contradictions=on`).

| Stage | Reads knob | Runs when |
|---|---|---|
| classify | `curation` | standard |
| summarize | `curation` | standard (and `content_short` is missing or flagged for refresh) |
| observation_extract | `curation` | standard |
| concept synthesis | `curation` | standard |
| contradictions | `contradictions` | on |
| embed | (always) | always |

`gramaton_inspect` returns a record's resolved knob values as
`effective_curation: {curation, contradictions}` so you can see
exactly which stages will run on a given record before the next
cycle fires.

## Scope: What Curation Touches

- **Memory records**: orphan defaults apply (curation=standard,
  contradictions=on). Full pipeline runs unless the user has wired
  the record into a collection that resolves differently.
- **Collection items**: governed by the collection's three knobs.
  `processing_status` is set at insert time based on the
  `curation` knob -- standard items enter the autonomous pipeline,
  none items bypass it. The text the LLM stages read is built from
  the schema's `content_fields` list (see
  `gramaton_guide(topic="collections")`); items in collections
  without `content_fields` declared can't participate in
  `curation=standard` (refused at create time). Editing a field
  named in `content_fields` via `gramaton_collection_update`
  refreshes the vector + BM25 indexes and re-flags the item for
  reclassify in the next cycle.
- **Session segments** (`knowledge_type="segment"`): deterministic
  lifecycle only. Skipped for TF-IDF observation extraction (they
  were already extracted by the session LLM; re-extracting produces
  extraction-of-extraction noise).
- **Container nodes** (Session, Topic, Collection): excluded from
  search and from curation lifecycle transitions.

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
- `gramaton_duplicates`: find near-duplicate pairs for manual
  review (read-only; consolidate via `gramaton_update` +
  `gramaton_resolve` if warranted).
- `gramaton_guide(topic="metadata")`: field-by-field classification
  heuristics.
- `gramaton_guide(topic="collections")`: the three behaviour knobs
  and their template defaults.
