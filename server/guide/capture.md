# Capture Guide

Knowledge enters Gramaton through two paths: user-initiated capture and session extraction.

## User-Initiated Capture (gramaton_capture)

Use `gramaton_capture` ONLY when the user explicitly asks you to remember, save, or capture something. Do not call this tool autonomously.

Capture raw content, not summaries. The curation pipeline generates `content_short` and embeddings from the raw content.

## Session Extraction (gramaton_session_prepare/commit)

The primary path for automatic knowledge capture. The two-phase flow:

1. **Prepare** (`gramaton_session_prepare`): Returns extraction instructions and current session state (for dedup). Call at natural breakpoints.
2. **Commit** (`gramaton_session_commit`): Submit extracted segments. Each segment becomes both a Session record and a Memory record.

## What to Capture

- Decisions and their rationale
- Design choices and trade-offs
- User preferences and constraints
- Facts, insights, research findings
- Procedures and workflows

## What NOT to Capture

- Greetings, small talk
- Questions without answers
- Work-in-progress that hasn't solidified

## Deprecated: gramaton_observe

`gramaton_observe` is soft-deprecated. It still works but will be removed once session extraction benchmarks confirm quality (target: 80-90% R@5). Use `gramaton_session_prepare/commit` instead.

## Related Tools

- `gramaton_capture`: User-initiated capture to Memory.
- `gramaton_session_prepare`: Start extraction flow.
- `gramaton_session_commit`: Submit extracted segments.
- `gramaton_observe`: DEPRECATED -- use session extraction instead.
- See `gramaton_guide(topic="metadata")` for classification fields.
