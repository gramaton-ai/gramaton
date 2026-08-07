# Save Guide

Knowledge enters Gramaton through two paths: user-initiated save
(`gramaton_save`) and session extraction (`gramaton_session_prepare`
/`gramaton_session_save`). Both produce records that serve the same
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

- **`summary_short`** -- Target ~750 chars, max ~900. This is
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

## Update, Don't Re-Save

Records are mutable. When knowledge evolves -- a decision is
refined, a fact changes, new detail lands -- update the existing
record with `gramaton_update` (metadata, `content`, or
`content_append`) instead of saving a near-copy. Search stays clean
because each fact has one live record; the commit log preserves
every prior version for history queries (`gramaton_history`).

The server enforces this. A save that is near-verbatim of an
existing record (cosine >= the configured hold threshold, default
0.94) is refused with a HELD response carrying the existing
record's ID, content, and version token. Two exits:

- Fold your material into the existing record via
  `gramaton_update(id=..., expected_version=...)`.
- If the two are genuinely distinct knowledge, re-send the save
  with `allow_similar` naming the held record's ID.

Saves in the advisory band below the hold threshold (default
0.85-0.94) succeed and carry a one-line notice naming the most
similar existing record -- a nudge to consider updating, not an
error.

## User-Initiated Save (`gramaton_save`)

Use ONLY when the user explicitly asks you to remember, save, or
save something. Do NOT call this tool autonomously -- session
extraction is the autonomous path.

Classify deliberately using the question-type-driven heuristics in
`gramaton_guide(topic="metadata")`. Defaults flatline the
classification distribution and disable retrieval signals
(temporal scoring, epistemic filtering).

## Session Extraction (`gramaton_session_prepare`/`commit`)

The primary autonomous-save path.

1. **Prepare**: Returns the canonical extraction instructions plus
   current session state (for dedup). Call EAGERLY within the turn
   when a decision lands, a rule or principle is articulated, a task
   completes, or the user pivots topics. Call at least every ~10
   substantive turns even without an explicit trigger. Also call
   before context compaction or when the user asks. Bundling saves
   at session end is an anti-pattern -- see
   `gramaton_guide(topic="sessions")` for the full trigger list.
2. **Commit**: Submit extracted segments. Each becomes a Session
   segment (BM25-indexed). When `promote_to_memory: true` (default
   when omitted) it ALSO becomes a Memory record (vector-embedded,
   full lifecycle). Set `promote_to_memory: false`
   for exploration, open questions, and dead ends -- they stay
   searchable in Sessions without polluting Memory's vector space.

See `gramaton_guide(topic="sessions")` for the full flow and the
question-type framework that drives extraction.

## What to Save

- Decisions and their rationale.
- Design choices and trade-offs (including the rejected options).
- User preferences and constraints.
- Facts, insights, research findings.
- Procedures and workflows.
- In-progress reasoning, open questions, dead ends -- saved with
  appropriate metadata (speculative / refuted / low confidence), and
  often `promote_to_memory: false` for the session flow.

## What NOT to Save

Be narrow. The metadata system handles uncertainty; only skip
content with no future value:

- Greetings, small talk, pleasantries.
- Restatements that add nothing to an already-saved record.
- Confused exchanges that produced no question and no answer.
- Mechanical tool-call narration unless the result itself is the
  knowledge.

## Related Tools

- `gramaton_save`: User-initiated save to Memory.
- `gramaton_session_prepare`: Start extraction flow.
- `gramaton_session_save`: Submit extracted segments.
- `gramaton_guide(topic="metadata")`: Classification fields and
  question-type mapping.
- `gramaton_guide(topic="sessions")`: Session model and two-tier
  extraction.
