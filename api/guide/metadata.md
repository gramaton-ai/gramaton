# Metadata Guide

Gramaton records carry epistemic metadata. Each field is a
*dimension* of "what kind of question does this record answer?" --
not paperwork. Agents that default these fields flatline the
distribution and disable the retrieval signals that depend on them
(temporal scoring, epistemic filtering).

## Fields and the Questions They Answer

### `temporality` — "Is this still true?"

- `immutable`: Always true. Definitions, axioms, fundamental
  constraints.
- `durable`: Stable until contradicted. Most decisions.
  Architectural choices.
- `temporal`: Time-bound; may go stale. Roadmap items, version-
  specific facts, anything tied to a release cycle.
- `ephemeral`: Very short lifespan. "User is at the airport."
  In-flight questions, transient state.

### `confidence` (0.0–1.0) — "How certain is this?"

- 0.9+: Settled, reviewed, demonstrated. Use for landed decisions.
- 0.7–0.9: Strong but not bulletproof. Well-reasoned conclusions.
- 0.4–0.7: Tentative. Active debate, partial conclusions.
- <0.4: Highly uncertain. Speculation, unverified claims, recorded
  dead ends.

### `knowledge_type` — "What shape of question does this answer?"

- `episodic`: "What happened?" An event with time context.
- `semantic`: "What's true?" A general fact or established claim.
- `procedural`: "How do I X?" A how-to or process.
- `conceptual`: "What does X mean, why does X exist?" A definition
  or principle.
- `reference`: "What's the value of X?" Lookup data.

### `epistemic_status` — "How strongly should the reader rely on this?"

- `well_established`: Broadly accepted; demonstrated; landed.
- `probable`: Likely true; well-reasoned but not yet validated.
- `speculative`: Uncertain. **Use for open questions, exploration,
  and dead ends -- they belong in the store with this flag, not
  filtered out.**
- `contested`: Conflicting evidence. Present both sides.
- `refuted`: Shown to be false. Use deliberately for "we tried X
  and it didn't work" so future searches surface the dead end and
  don't re-walk it. Do NOT present as true.

### `importance` (0.0–1.0) — "How important is this relative to the store?"

Ranked boost. Use sparingly; default 0.5 unless there's a real
reason to surface this above similar-scoring records.

## Mapping Question Types to Metadata

| Question | Typical metadata |
|---|---|
| Single-fact recall ("what did we decide about X?") | `semantic`, `well_established`, `confidence: 0.9+` |
| Current position ("what's our position now?") | `temporal` or `durable`; records update in place, so the live record IS the current position |
| Decision rationale ("why did we decide Y?") | `conceptual`, `confidence: 0.7+` |
| What we tried ("did we already consider X?") | `epistemic_status: refuted`, `confidence: 0.5+` |
| What's open ("what's still unresolved?") | `ephemeral` or `temporal`, `speculative`, `confidence: 0.3–0.5` |
| What changed ("what's new since X?") | any -- queryable by `created_at` sort |
| Implicit preferences ("what does user prefer?") | `semantic`, `durable`, `confidence: 0.8+` |

## Related Tools

- `gramaton_save`: Set metadata at capture time.
- `gramaton_classify`: Set metadata on pending records.
- `gramaton_update`: Update metadata on existing records.
- See `gramaton_guide(topic="save")` for how content/summary_short/
  keywords relate to each other and retrieval.
