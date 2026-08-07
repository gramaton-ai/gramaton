---
name: curation-sweep
description: Use to run piggyback curation on pending records when the server has no LLM configured. Self-triggered by the `curation` field in any MCP response showing `{overdue: true, autonomous: false}` with `pending_count > 0`. User-triggered by "classify pending records", "run curation", "clean up the pending queue". Runs once per session at a natural breakpoint. Walks each pending record: inspect → classify → search related → link. Not needed when the server's autonomous curation is configured (autonomous: true in the piggyback field).
---

# curation-sweep

The server runs autonomous curation every 5 minutes IF an LLM provider is configured. If not, classification falls to the agent through this piggyback flow.

## When to run

- `curation` field in an MCP response shows `{overdue: true, autonomous: false}` AND `pending_count > 0` — self-triggered.
- User explicitly asks to run curation or classify pending records.

**Do NOT run if `autonomous: true`** — duplicates server work and burns tokens.

**At most once per session.** At a natural breakpoint (task completion, topic pivot), not mid-task.

## Before you start

Tell the user you're doing a curation sweep and how many records are pending. Keep it one line.

## Per-record loop

```
pending = gramaton_pending()
```

For each pending id (work in order returned):

### 1. Inspect

```
record = gramaton_inspect(id=<id>)
```

Read `content`, any existing partial metadata, existing edges, source (memory vs session segment).

### 2. Classify

Decide the four core fields:

- **temporality** — `immutable` / `durable` / `temporal` / `ephemeral`
  - immutable: definitions, axioms, physical constants
  - durable: stable until contradicted (most knowledge records)
  - temporal: time-bound (deadlines, current states, versions)
  - ephemeral: very short lifespan (in-progress work, session-local notes)

- **confidence** — 0.0–1.0
  - 0.9+ for observed facts or explicit user statements
  - 0.7–0.9 for reasoned conclusions
  - 0.4–0.7 for uncertain inferences
  - Below 0.4: question whether this should be a record at all

- **knowledge_type** — `episodic` / `semantic` / `procedural` / `conceptual` / `reference`
  - episodic: specific event with time context
  - semantic: general fact
  - procedural: how-to / instructions
  - conceptual: definition or principle
  - reference: lookup data

- **epistemic_status** — `well_established` / `probable` / `speculative` / `contested` / `refuted`
  - Default to `probable` unless there's signal to go higher or lower
  - `well_established` only for widely accepted facts with corroboration

Apply:

```
gramaton_classify(
    id=<id>,
    temporality=<value>,
    confidence=<float>,
    knowledge_type=<value>,
    epistemic_status=<value>,
)
```

### 3. Link related records

```
related = gramaton_search(text=<key phrase from content>, top=3, id_not=<id>)
```

For each related record with high relevance:

```
gramaton_link(
    id=<id>,
    target_id=<related_id>,
    edge_type=<type>,       # related_to, refutes, elaborates, etc.
    edge_weight=<0.0-1.0>,
)
```

Pick edge type based on actual relationship, not defaulting to `related_to`:
- `refutes` — this record contradicts the other
- `elaborates` — this record is a deeper treatment of the other
- `derived_from` — this record was extracted from the other
- `related_to` — genuinely just related, no stronger relationship
- `supersedes` — this record replaces the other. Rare under mutable
  records: a revision should be folded into the existing record with
  `gramaton_update`, not saved beside it. Reserve this edge for when
  the lineage itself is knowledge (a decision formally reversing an
  earlier one).

Skip linking if nothing is actually related — a sparse graph beats a noisy one.

### 4. Continue

Move to the next pending record. Don't pause to report per-record; the summary at the end is what matters.

## When to stop

- All pending processed, OR
- You've processed ~25 records (diminishing returns beyond this in one session — the server will pick up the rest on its next schedule).

Never leave a record half-processed — if you classified but didn't link, that's fine (links are additive). Don't start classifying a record you won't finish.

## Summary

One concise report at the end:

```
Curation sweep complete
  Classified: <N>
  Linked: <M> edges across <K> records
  Skipped: <X> (reason)
  Remaining pending: <Y>
```

If remaining > 0 and > 25, note that the server will process the rest on its next 5-minute tick (if autonomous becomes available) or flag that another sweep is warranted.

## Fallback

If MCP tools are unavailable: `gramaton pending`, `gramaton inspect <id>`, `gramaton classify <id> --temporality=...`, `gramaton link ...` (assuming `gramaton` is on PATH). Slower but functional.
