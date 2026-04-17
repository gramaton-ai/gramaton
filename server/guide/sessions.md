# Sessions Guide

Sessions are how Gramaton captures knowledge from conversations
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
3. **Commit** (`gramaton_session_commit`): Submits extracted
   segments.
4. Sessions never end. No close or conclude state. Append-only.

## Finding the Session ID

Run `gramaton session current` -- returns `{session_id,
client_session_id}` for the session bound to the working directory.
Safe under concurrent Claude Code instances; each cwd gets its own
session file.

## Two-Tier Extraction

Every committed segment becomes a **Session segment** (BM25-indexed,
captures the conversation thread). When `promote_to_memory: true`
(the default when omitted), it ALSO becomes a **Memory record**
(vector-embedded, full lifecycle, auto-supersession at cosine ≥ 0.92).

### When to promote (true / omit)

Decision-grade knowledge worth surfacing in semantic search:

- Decisions and their rationale.
- User preferences, constraints, established facts.
- Architectural choices.
- Procedures, workflows, research findings.

### When to stay Session-only (false)

Valuable context that shouldn't pollute Memory's vector space:

- Open questions that haven't resolved (capture them so a future
  agent knows the question is still on the table, but they aren't
  Memory-grade until answered).
- Pure exploration ("we considered X but moved on").
- Conversational context useful for reading the session but not
  standalone knowledge.

Heuristic: **if a future agent searches semantically for this topic,
would this segment be a useful answer or noise?** Useful -> promote.
Noise-but-worth-finding-by-keyword -> Session-only.

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

## Question Types Sessions Serve

Sessions shine for questions Memory alone can't answer well:

- **"What did we try?"** -- dead ends captured with
  `epistemic_status: refuted`.
- **"What's still open?"** -- open questions captured as Session-only
  segments with `epistemic_status: speculative`.
- **"What was the broader conversation around X?"** -- the thread
  that produced the Memory record, including the rejected
  alternatives.
- **"When did we discuss X?"** -- session timestamps + topic names.

For "what do we know?" questions, Memory is usually the right source.

## Key Design Decisions

- Append-only. Segments are never removed. The only mutation is
  `captured_as` / `captured_at` on a promoted segment.
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
  extracting to avoid re-capturing.
- **PreCompact archive**: if uncaptured segments existed at
  compaction, the raw transcript is archived (gzipped) and the next
  `prepare` surfaces `pending_uncaptured: {count, archive_path}` so
  you can decompress and review if needed.

## Related Tools

- `gramaton_session_start`: Create or resume.
- `gramaton_session_get`: View full session state.
- `gramaton_session_prepare`: Start extraction flow; returns the
  canonical extraction prompt.
- `gramaton_session_commit`: Submit segments (with `promote_to_memory`
  per segment).
- `gramaton session current` (CLI): Resolve the session_id for this
  working directory.
- `gramaton_guide(topic="capture")`: Field roles, synthesis
  principle.
- `gramaton_guide(topic="metadata")`: Classification fields and
  their question-type mapping.
