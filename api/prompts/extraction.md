# Extract knowledge from this conversation

## Locate the boundary

Scan back through this conversation for the most recent line
containing `[gramaton-save-boundary` -- that string is the
confirmation from your most recent successful `gramaton_session_save`.
Everything BEFORE that boundary has already been extracted. Extract
knowledge ONLY from content that appeared AFTER that line.

If no such line exists in your context, this is the first save of
the session (or the prior save's confirmation was compacted away) --
extract from the entire conversation.

Segments in `session_state` that pre-date `last_saved_at` arrive
without `content` to keep this response small; their `summary_short`
plus topic is enough to recognise already-covered ground.

## What to save

Review `session_state` below. Skip re-save of already-covered
topics unless the conversation added, refined, or reversed them --
when ideas evolve, save the NEW version. If a segment's Memory
promotion comes back HELD (near-verbatim of an existing record),
resolve it with `gramaton_session_resolve_held`: fold the material
into the existing record as an update, or allow the promotion if
the two are genuinely distinct knowledge. Unresolved holds
reappear on the next prepare until you act on them.

Submit segments via `gramaton_session_save`. Each segment needs:

- `content` -- unbounded, self-contained. Include rationale,
  alternatives considered, why-nots, concrete details (paths,
  numbers, names).
- `summary_short` -- ≤900 chars (target ~750). This is the
  embedding-ready semantic anchor for vector search.
- `keywords` -- 3-8 future-search terms a reader would TYPE, not
  literal phrases from the conversation.
- `topic` -- thematic cluster name. Multiple segments can share a topic.
- `temporality`, `confidence`, `knowledge_type`, `epistemic_status`
  -- chosen deliberately per segment.
- `promote_to_memory` -- omit (default true) for decisions, facts,
  preferences. Set `false` only for exploration, open questions,
  and dead ends (they stay searchable in Session without polluting
  Memory's vector space).

## Principles (must follow)

- **Synthesize, don't summarize.** Rewrite for a future reader who
  wasn't in the room. Anti-patterns: "We discussed X" (substance
  lost), "User decided Y" (the *why* lost). Be concrete.
- **Save, don't suppress.** Open questions, dead ends, in-progress
  reasoning, and joint conclusions ALL belong in the store -- just
  with honest metadata: `speculative` for uncertainty, `refuted` for
  "we tried X and it didn't work", `promote_to_memory: false` for
  pure exploration that shouldn't compete with decisions in semantic
  search.
- **Findability is prospective.** Keywords and summary_short use the
  vocabulary a future search would TYPE, not the literal language of
  the conversation. Name concepts no one said if that's what the
  future question would ask about.
- **Skip only the low-value:** greetings, pure restatements of
  already-saved segments, confused exchanges without resolution,
  mechanical tool-call narration without substantive results.

## For deeper guidance

- `gramaton_guide(topic="save")` -- field roles, synthesis
  principle, what to save vs skip in detail.
- `gramaton_guide(topic="metadata")` -- per-field classification
  heuristics and the question-type → metadata mapping.
- `gramaton_guide(topic="sessions")` -- two-tier Session/Memory model,
  session state semantics, when to call prepare/save.
