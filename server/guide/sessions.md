# Sessions Guide

Sessions track knowledge extraction from conversations. They coordinate the two-phase prepare/commit flow.

## Session Lifecycle

1. **Start** (`gramaton_session_start`): Creates a session or resumes an existing one (idempotent via client_session_id).
2. **Extract** (`gramaton_session_prepare`): Returns extraction instructions and session state for dedup.
3. **Commit** (`gramaton_session_commit`): Submits extracted segments. Creates both Session segments and Memory records.
4. Sessions never end. No close or conclude state. Append-only.

## Data Model

- **Session**: Container node. Has client_session_id, themes.
- **Topic**: Branch within a session. Topics can branch from other topics.
- **Segment**: A piece of extracted knowledge. Linked to a Topic and to a Memory record via `extracted_as` edge.

## Key Design Decisions

- Sessions are append-only. Segments are never removed.
- Fresh sessions don't link to previous sessions. Use search for cross-session context.
- `--continue` resumes by matching client_session_id.
- Topics are created on commit when a new topic name appears.

## Related Tools

- `gramaton_session_start`: Create or resume.
- `gramaton_session_get`: View session state.
- `gramaton_session_prepare`: Start extraction.
- `gramaton_session_commit`: Submit segments.
