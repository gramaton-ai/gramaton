# Metadata Guide

Gramaton records carry epistemic metadata that describes the nature, reliability, and lifecycle of knowledge.

## Fields

- **temporality**: How time-sensitive the knowledge is.
  - `immutable`: Always true (definitions, axioms).
  - `durable`: Stable until contradicted.
  - `temporal`: Time-bound, may become stale.
  - `ephemeral`: Very short lifespan.

- **confidence** (0.0-1.0): How likely this is correct.
  - 0.9+: Highly reliable.
  - 0.7-0.9: Reliable with minor uncertainty.
  - 0.4-0.7: Uncertain, mention to user.
  - <0.4: Low confidence, corroborate first.

- **knowledge_type**: What kind of knowledge.
  - `episodic`: A specific event with time context.
  - `semantic`: A general fact or established knowledge.
  - `procedural`: A how-to or process.
  - `conceptual`: A definition or principle.
  - `reference`: Lookup data.

- **epistemic_status**: Qualitative reliability.
  - `well_established`: Broadly accepted.
  - `probable`: Likely true.
  - `speculative`: Uncertain.
  - `contested`: Conflicting evidence.
  - `refuted`: Shown to be false.

- **importance** (0.0-1.0): How important this knowledge is relative to the store.

## Related Tools

- `gramaton_capture`: Set metadata at capture time.
- `gramaton_classify`: Set metadata on pending records.
- `gramaton_update`: Update metadata on existing records.
