# Sessions Guide

Sessions are how Gramaton saves knowledge from conversations
automatically. They coordinate the two-phase `prepare`/`commit` flow
and hold the broader conversational thread (exploration, open
questions, dead ends) alongside decision-grade Memory records.

## Session Lifecycle

1. **Start** (`gramaton_session_start`): Creates a session or resumes
   an existing one (idempotent via `client_session_id`). SessionStart
   hook does this automatically in Claude Code.
2. **Prepare** (`gramaton_session_prepare`): Returns the canonical
   extraction instructions plus current session state. The
   instructions deliver the question-type framework, classification
   heuristics, field-role guidance, and synthesis principle -- don't
   front-run them with inline reasoning.
3. **Commit** (`gramaton_session_save`): Submits extracted
   segments.
4. Sessions never end. No close or conclude state. Append-only.

## Finding the Session ID

Run `gramaton session current` -- returns `{session_id,
client_session_id}` for the session bound to the working directory.
Safe under concurrent Claude Code instances; each cwd gets its own
session file.

## Two-Tier Extraction

Every committed segment becomes a **Session segment** (BM25-indexed,
saves the conversation thread). When `promote_to_memory: true`
(the default when omitted), it ALSO becomes a **Memory record**
(vector-embedded, full lifecycle).

### When to promote (true / omit)

Decision-grade knowledge worth surfacing in semantic search:

- Decisions and their rationale.
- User preferences, constraints, established facts.
- Architectural choices.
- Procedures, workflows, research findings.

### When to stay Session-only (false)

Valuable context that shouldn't pollute Memory's vector space:

- Open questions that haven't resolved (save them so a future
  agent knows the question is still on the table, but they aren't
  Memory-grade until answered).
- Pure exploration ("we considered X but moved on").
- Conversational context useful for reading the session but not
  standalone knowledge.

Heuristic: **if a future agent searches semantically for this topic,
would this segment be a useful answer or noise?** Useful -> promote.
Noise-but-worth-finding-by-keyword -> Session-only.

### Held promotions

The segment always lands. But when a segment's Memory promotion is
near-verbatim of an existing Memory record (cosine >= the hold
threshold, default 0.94), the promotion is HELD instead of created:
the save response lists it under `held_promotions` with the
existing record's ID, content, and similarity. The hold persists on
the segment, and unresolved holds are re-presented by the next
`session_prepare` until acted on.

Resolve with `gramaton_session_resolve_held`, per segment:

- `action: "update_target"` -- you have ALREADY folded the
  segment's material into the similar record via `gramaton_update`;
  the server wires the segment's `extracted_as` provenance edge to
  that record and no new record is created.
- `action: "allow_similar"` -- the two are genuinely distinct;
  create the Memory record now.

`session_save` also accepts `allow_similar: true` to disable
promotion holds for a whole commit -- a bulk-ingestion escape
(benchmark or migration imports), never a standing default.

## Data Model

- **Session node** (`knowledge_type: session`): Container. Properties:
  `client_session_id`, `created_at`, optional `archive_path` (from
  PreCompact hook).
- **Topic node** (`knowledge_type: topic`): A thematic cluster within
  a session. Edge: `topic_of` -> Session. Can branch from other
  topics via `branched_from`.
- **Segment node** (`knowledge_type: segment`): A piece of extracted
  knowledge. Edge: `segment_of` -> Topic. Promoted segments also
  have an `extracted_as` edge -> Memory record.

## When to Call prepare/save

Call `prepare` + `commit` EAGERLY and FREQUENTLY during a conversation.
This is the primary autonomous-save path; bundling saves at
session end is an anti-pattern because early-session context becomes
harder to reconstruct as the conversation accumulates.

**Act within the turn when any of these lands:**

- A decision is reached (design choice, architectural call, which
  library, which approach).
- The user articulates a rule, principle, or preference.
- A task in the TaskList flips to `completed`.
- The user pivots to a new topic — save the outgoing one first.
- The user says "done", "ship it", "that works", or similar closure
  signals on work that just landed.
- The user explicitly asks to save.
- Before context compaction: any mention of compacting, running low,
  or needing to compress.

**Scheduled cadence:** even without an explicit trigger, call
prepare/save at least every ~10 substantive turns of a real-dev
conversation. Reset the clock at each commit.

**What counts as "substantive":** turns that produced decisions,
preferences, design rationale, dead ends, research, architectural
choices, cost estimates, or any non-trivial reasoning. Routine Q&A
and small edits don't reset the clock.

**Anti-pattern to avoid:** "I'll save at the end of this big
task." By the time the big task completes, you've blown past multiple
natural breakpoints and the earliest reasoning is harder to recover.
Save at each landing, not at the end.

## Question Types Sessions Serve

Sessions shine for questions Memory alone can't answer well:

- **"What did we try?"** -- dead ends saved with
  `epistemic_status: refuted`.
- **"What's still open?"** -- open questions saved as Session-only
  segments with `epistemic_status: speculative`.
- **"What was the broader conversation around X?"** -- the thread
  that produced the Memory record, including the rejected
  alternatives.
- **"When did we discuss X?"** -- session timestamps + topic names.

For "what do we know?" questions, Memory is usually the right source.

## Key Design Decisions

- Append-only. Segments are never removed. The only mutations are
  `captured_as` / `captured_at` on a promoted segment and the
  persisted hold state on a segment whose promotion was held.
- No end state. Sessions just stop being updated.
- Fresh sessions don't auto-link to previous sessions; cross-session
  knowledge continuity goes through search of Memory, not through
  session linking.
- `--continue` resumes by `client_session_id`.
- Topics are created on commit when a new topic name appears.
- Session-to-session chains happen only on explicit resume
  (`source="resume"`), creating a `continues_from` edge for
  navigation.

## Hook-Driven Nudges

- **PostCompact flag**: after compaction, the next `prepare` surfaces
  `recent_compaction: {at: ...}` and prepends a note to the
  extraction instructions. Review session state carefully before
  extracting to avoid re-saving.
- **PreCompact archive**: if uncaptured segments existed at
  compaction, the raw transcript is archived (gzipped) and the next
  `prepare` surfaces `pending_uncaptured: {count, archive_path}` so
  you can decompress and review if needed.

## Save Boundaries

Every successful `session_save` returns a `boundary` object:

```json
{
  "marker": "[gramaton-save-boundary T=2026-05-23T15:30:00Z]",
  "timestamp": "2026-05-23T15:30:00Z",
  "session_id": "..."
}
```

The bracketed `marker` is the LLM-friendly scoping primitive for
subsequent cycles in the same conversation. Substring-scan your
conversation history for `[gramaton-save-boundary` and treat
content appearing AFTER the most recent match as the extraction
scope -- everything before has already been saved.

`session_prepare` carries the matching `session_state.last_saved_at`
watermark (RFC3339, omitted on the first prepare of a session) and
returns segments older than that watermark in a lean shape: `id` +
`summary_short` + topic + timestamps, no `content`. The
`summary_short` plus topic name is enough to recognise already-saved
ground without re-shipping the full content. If no
`[gramaton-save-boundary` line exists in your context, treat the
whole conversation as in scope -- the prior save's confirmation may
have been compacted away.

## Related Tools

- `gramaton_session_start`: Create or resume.
- `gramaton_session_get`: View full session state.
- `gramaton_session_prepare`: Start extraction flow; returns the
  canonical extraction prompt.
- `gramaton_session_save`: Submit segments (with `promote_to_memory`
  per segment).
- `gramaton_session_resolve_held`: Resolve held Memory promotions
  (update the similar existing record, or allow the new one).
- `gramaton session current` (CLI): Resolve the session_id for this
  working directory.
- `gramaton_guide(topic="save")`: Field roles, synthesis
  principle.
- `gramaton_guide(topic="metadata")`: Classification fields and
  their question-type mapping.
