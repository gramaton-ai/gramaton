# Curation Guide

The server runs background curation automatically every 5 minutes.

## Deterministic Pipeline (always runs)

- Lifecycle transitions (expire stale ephemeral/temporal records)
- Orphan linking (connect unlinked records via similarity)
- Duplicate consolidation
- Concept candidate detection
- Store manifest computation

## Autonomous Pipeline (when LLM configured)

- Record classification (pending -> processed)
- Contradiction detection between similar records
- Concept synthesis
- Auto-summarization (generates content_short)

## Tools

- `gramaton_curation(action="status")`: Check curation state.
- `gramaton_curation(action="trigger")`: Trigger a cycle manually.
- `gramaton_curation(action="dry_run")`: Preview changes without applying.
- `gramaton_curation(action="batch")`: Batch-classify all pending records via LLM.

## Session Segments

Curation skips TF-IDF observation extraction for Session segment nodes (knowledge_type="segment"). These were already extracted by the session LLM -- re-extracting would produce extraction-of-extraction noise.

## Piggyback Curation

When `curation.overdue=true` and `autonomous=false` (no server LLM), agents can classify pending records directly using `gramaton_pending` + `gramaton_classify`.
