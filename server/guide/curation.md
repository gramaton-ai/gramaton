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
- **Duplicate consolidation**: auto-supersession for records where
  cosine ≥ 0.92, gated by the per-record effective `supersession`
  knob (see "Stage → knob mapping" below).
- **Concept candidate detection**: surface recurring keywords that
  could graduate to concept nodes.
- **Store manifest computation**: topic/theme summary across the
  store.

## Autonomous Pipeline (when LLM is configured)

- **Record classification**: captured -> processed for records
  lacking full metadata. Uses the question-type-driven classification
  from `gramaton_guide(topic="metadata")`.
- **Contradiction detection**: flags semantically similar records
  that contradict each other.
- **Concept synthesis**: promote candidates to concept nodes and
  link evidence.
- **Auto-summarization**: generates `content_short` (the embedding-
  ready semantic anchor, ~750 chars) for records missing it.
- **Observation extraction**: pulls structured observations from
  long records.

## Stage → knob mapping

Each pipeline stage reads exactly one collection-level knob to
decide whether to run on a given record. The knobs are documented
in `gramaton_guide(topic="collections")` under "Behaviour fields".
Memory orphan records resolve to the memory-store defaults
(`curation=standard, supersession=store, contradictions=on`).

| Stage | Reads knob | Runs when |
|---|---|---|
| classify | `curation` | standard |
| summarize | `curation` | standard (and `content_short` is missing) |
| observation_extract | `curation` | standard |
| concept synthesis | `curation` | standard |
| contradictions | `contradictions` | on |
| supersession | `supersession` | per-pair: both `store` always fires; `collection` requires a shared `member_of` collection; `none` on either side blocks |
| embed | (always) | always |

`gramaton_inspect` returns a record's resolved knob values as
`effective_curation: {curation, supersession, contradictions}` so
you can see exactly which stages will run on a given record before
the next cycle fires.

## Scope: What Curation Touches

- **Memory records**: orphan defaults apply (curation=standard,
  supersession=store, contradictions=on). Full pipeline runs
  unless the user has wired the record into a collection that
  resolves differently.
- **Collection items**: governed by the collection's three knobs.
  `processing_status` is set at insert time based on the
  `curation` knob -- standard items enter the autonomous pipeline,
  none items bypass it.
- **Session segments** (`knowledge_type="segment"`): deterministic
  lifecycle only. Skipped for:
  - TF-IDF observation extraction (they were already extracted by
    the session LLM; re-extracting produces extraction-of-extraction
    noise).
  - Auto-supersession (Session segments are append-only snapshots;
    they don't supersede each other).
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
- `gramaton_duplicates`: preview what auto-supersession will catch.
- `gramaton_guide(topic="metadata")`: field-by-field classification
  heuristics.
- `gramaton_guide(topic="collections")`: the three behaviour knobs
  and their template defaults.
