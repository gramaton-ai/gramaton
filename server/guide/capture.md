# Capture Guide

Knowledge enters Gramaton through two paths: user-initiated capture
(`gramaton_capture`) and session extraction (`gramaton_session_prepare`
/`gramaton_session_commit`). Both produce records that serve the same
future-agent question types, but they differ in who triggers them and
when.

## Field Roles (Read This First)

The three content fields serve **three different parts of retrieval**.
They are NOT nested compressions of the same text.

- **`content`** -- Unbounded. Full self-contained reading. MUST
  include the rationale, alternatives considered, why-nots, concrete
  details (file paths, numbers, names, IDs), and constraints that
  shaped the decision. Read by humans/agents *after* a search match.
  Anti-patterns: "We discussed X" (substance lost), "User decided Y"
  (why lost), "The team chose Z" (everything lost).

- **`summary_short`** -- Up to ~750 chars (hard cap 1000). This is
  what gets vector-embedded -- the **embedding-ready semantic anchor**
  of the record. Treat as the canonical semantic representation, not
  a tagline. Fill it with substance.

- **`keywords`** -- 3-8 search terms a **future agent would type**,
  not literal phrases from the conversation. BM25 weighting boost.

Different fields feed different searches. Semantic recall hits
`summary_short`; keyword recall hits `keywords` + BM25 of `content`;
comprehension reads `content`.

## Synthesis, Not Summarization

Rewrite knowledge in the form most useful to a future reader who
wasn't in the room. Do NOT pick the most distinctive sentence. Do NOT
compress by shortening. Reorganize, restate, and synthesize so the
record stands alone. Findability metadata is what someone would
search *for later*, not what was literally said.

## User-Initiated Capture (`gramaton_capture`)

Use ONLY when the user explicitly asks you to remember, save, or
capture something. Do NOT call this tool autonomously -- session
extraction is the autonomous path.

Classify deliberately using the question-type-driven heuristics in
`gramaton_guide(topic="metadata")`. Defaults flatline the
classification distribution and disable retrieval signals
(supersession, temporal scoring, epistemic filtering).

## Session Extraction (`gramaton_session_prepare`/`commit`)

The primary autonomous-capture path.

1. **Prepare**: Returns the canonical extraction instructions plus
   current session state (for dedup). Call at natural breakpoints,
   before context compaction, on long runs periodically, or when the
   user asks you to capture.
2. **Commit**: Submit extracted segments. Each becomes a Session
   segment (BM25-indexed). When `promote_to_memory: true` (default
   when omitted) it ALSO becomes a Memory record (vector-embedded,
   full lifecycle, auto-supersession). Set `promote_to_memory: false`
   for exploration, open questions, and dead ends -- they stay
   searchable in Sessions without polluting Memory's vector space.

See `gramaton_guide(topic="sessions")` for the full flow and the
question-type framework that drives extraction.

## What to Capture

- Decisions and their rationale.
- Design choices and trade-offs (including the rejected options).
- User preferences and constraints.
- Facts, insights, research findings.
- Procedures and workflows.
- In-progress reasoning, open questions, dead ends -- captured with
  appropriate metadata (speculative / refuted / low confidence), and
  often `promote_to_memory: false` for the session flow.

## What NOT to Capture

Be narrow. The metadata system handles uncertainty; only skip
content with no future value:

- Greetings, small talk, pleasantries.
- Restatements that add nothing to an already-captured record.
- Confused exchanges that produced no question and no answer.
- Mechanical tool-call narration unless the result itself is the
  knowledge.

## Deprecated: `gramaton_observe`

Soft-deprecated. Still works but will be removed once session
extraction benchmarks confirm quality. Use
`gramaton_session_prepare/commit` instead.

## Related Tools

- `gramaton_capture`: User-initiated capture to Memory.
- `gramaton_session_prepare`: Start extraction flow.
- `gramaton_session_commit`: Submit extracted segments.
- `gramaton_guide(topic="metadata")`: Classification fields and
  question-type mapping.
- `gramaton_guide(topic="sessions")`: Session model and two-tier
  extraction.
