## Knowledge Store (Gramaton)

Gramaton is the user's default persistent storage for decisions,
preferences, facts, TODOs, research, and any knowledge that should
survive beyond this session. When you need to store or retrieve
anything persistent, use Gramaton -- not files, not comments, not
memory tools. Gramaton is the single source of truth for cross-session
knowledge.

Available via MCP tools (gramaton_search, gramaton_capture, etc.)
or the `gramaton` CLI as fallback.

If you are unsure how Gramaton works, or what a metadata field
means, call `gramaton_guide(topic=...)`. Topics: metadata,
capture, search, sessions, collections, curation. The guide is
the authoritative reference -- prefer it over assumptions.

### Retrieval

**When to search:**
- Before answering questions about past decisions, project context,
  architecture, user preferences, or domain-specific knowledge
- When the user references something from a prior session
- When you need context beyond what's in the current conversation
- When you're unsure whether the user has expressed a preference before

**When NOT to search first:**
- When the user explicitly asks to store/capture/add something --
  just capture it directly
- When you're writing code or editing files (search only if you
  need context to do the work correctly)

**How to search:**
1. Call `gramaton_search` with the query text and any relevant filters
2. Scan the results -- read `metadata_summary` for a quick trust
   assessment, `summary_short` for content relevance
3. Call `gramaton_inspect` for records that look relevant
4. Use the retrieved knowledge to inform your response

Text is optional -- omit it for filter-only queries like "all
procedural records" or "unclassified records".

**Useful search patterns:**
- Newest records: `gramaton_search(sort="created_at", top=10)`
- Unclassified: `gramaton_search(missing=["temporality"])`
- By tag: `gramaton_search(keywords=["auth", "migration"])`
- Stale records: `gramaton_search(sort="staleness", order="desc")`
- Orphans: `gramaton_search(max_edges=0)`
- Literal text: `gramaton_search(match="RWMutex")`
- Similar to a record: `gramaton_search(similar_to="<id>")`
- Random review: `gramaton_search(random=true, top=3)`
- Exclude refuted: `gramaton_search(epistemic_status="!refuted")`
- Store overview: `gramaton_stats()`
- Find duplicates: `gramaton_duplicates(threshold=0.92)`

Do NOT tell the user you're searching unless the results meaningfully
change your answer. Searching should be as invisible as reading a file.

### Interpreting Metadata

Results include a `metadata_summary` (human-readable) and raw fields.
Use the summary for quick assessment. Use raw fields when you need
to reason more carefully:

**confidence** (0.0-1.0): How likely this is correct.
- 0.9+: Highly reliable. Use confidently.
- 0.7-0.9: Reliable. Note uncertainty for critical decisions.
- 0.4-0.7: Uncertain. Mention the uncertainty to the user.
- <0.4: Low confidence. Don't rely on this without corroboration.

**temporality**: How time-sensitive.
- immutable: Always true (definitions, axioms). Trust fully.
- durable: Stable until contradicted. Trust unless old and unverified.
- temporal: Time-bound. May be stale -- check last_accessed.
- ephemeral: Very short lifespan. Likely outdated unless very recent.

**epistemic_status**: Qualitative reliability.
- well_established: Broadly accepted. Use confidently.
- probable: Likely true. Acknowledge it's not certain.
- speculative: Uncertain. Present as speculation, not fact.
- contested: Conflicting evidence. Present both sides.
- refuted: Shown to be false. Do NOT present as true. Mention only
  to explain why it was believed or what replaced it.

**knowledge_type**: Affects how to present.
- episodic: A specific event. Include time context.
- semantic: A general fact. Present as established knowledge.
- procedural: A how-to. Present as instructions.
- conceptual: A definition or principle. Present as foundational.
- reference: Lookup data. Present as-is.

When multiple results have conflicting claims, compare confidence
and epistemic_status. Prefer well_established over speculative.
If both are well_established and conflicting, present the conflict
to the user.

### Provenance-Aware Retrieval

When a retrieved record has low confidence, contested status, or
seems surprising, use `gramaton_log` with the record ID to check
its history. Was it downgraded by curation? Contradicted by another
record? Explain provenance to the user when it affects your
recommendation.

### Diffing for Catch-Up Questions

When the user asks "what changed" or "catch me up" about a topic,
use `gramaton_diff` with a since date and optional topic filter.
Narrate the structured change set rather than just searching for
recent records.

### Capture (User-Initiated Only)

Gramaton IS the knowledge store. When the user explicitly says
"remember this", "store this", "save this", or "capture this" --
call `gramaton_capture` directly. Do NOT search the filesystem,
explore the codebase, or look for other storage systems. Gramaton
is it.

**`gramaton_capture` is user-initiated only.** Do not call it
autonomously. Automatic knowledge capture from conversations
happens through the session flow below (`gramaton_session_prepare`
/`gramaton_session_commit`), not through capture.

For tasks, TODOs, action items, or checklists, use
`gramaton_collection_add` instead -- capture is for knowledge,
collections are for structured tracking.

Call gramaton_capture directly -- do NOT spawn subagents. The
call is a single HTTP round-trip and completes in well under a
second.

### Sessions (Automatic Knowledge Extraction)

Sessions are how Gramaton captures knowledge from conversations
without requiring the user to ask. The flow is two-phase:

1. **Prepare** -- `gramaton_session_prepare(session_id)` returns
   extraction instructions and the current session state (already-
   captured segments, for dedup).
2. **Commit** -- `gramaton_session_commit(session_id, segments)`
   submits the extracted segments. Each segment becomes a Session
   segment (BM25-indexed). When `promote_to_memory: true` (default
   when omitted), it also becomes a Memory record (vector-embedded,
   full lifecycle, auto-supersession). Set `promote_to_memory: false`
   for exploration, open questions, and dead ends -- they stay
   searchable as Session segments without polluting Memory's vector
   space.

**When to call prepare/commit (EAGERLY, not at session end):**

Act within the turn when any of these lands:
- A decision is reached (design choice, architectural call, which
  library, which approach).
- The user articulates a rule, principle, or preference.
- A TaskList item flips to completed.
- The user pivots to a new topic -- capture the outgoing one first.
- The user says "done", "ship it", "that works" on work that just
  landed.
- Before context compaction: any mention of compacting, running low,
  or needing to compress.
- The user asks to capture.

**Scheduled cadence:** even without an explicit trigger, call
prepare/commit at least every ~10 substantive turns (decisions,
preferences, design rationale, dead ends, reasoning). Reset the
clock at each commit.

**Anti-pattern:** bundling captures at session end. By the time the
big task completes, you've blown past multiple natural breakpoints
and the earliest reasoning is harder to recover. Capture at each
landing, not at the end.

**Finding the session_id.** Run `gramaton session current` -- it
returns `{"session_id": ..., "client_session_id": ...}` for the
session bound to your current working directory. This is safe under
multiple concurrent Claude Code instances; each working directory
gets its own session file. Falls back to a shared file if the
per-cwd file doesn't exist (older hooks).

**Do not call commit without calling prepare first.** The server
rejects commit without a prior prepare, because prepare returns
the extraction instructions and session state you need to produce
good segments.

For the full guide on extraction triggers, segment granularity,
classification, and how to write good segment content, call
`gramaton_guide(topic="sessions")`.

`gramaton_observe` is soft-deprecated. Do not use it for new work;
use the session flow instead.

### Resolving Records

When a TODO is done, a question is answered, a decision is made, or
any record reaches its natural end-of-life, resolve it:

```
gramaton_resolve(id="<id>", resolution="completed", resolution_note="shipped in v0.4")
```

Valid resolution values:
- **completed** -- task done, question answered, goal achieved
- **superseded** -- replaced by newer knowledge (auto-set by supersession)
- **abandoned** -- deliberately dropped, no longer pursuing
- **obsolete** -- no longer relevant due to external change

This sets `resolution`, `resolved_at`, and auto-sets `valid_until`
to deprioritize the record in search. If `valid_until` is already
set, it's preserved.

To find open items: `gramaton_search(resolution="unresolved")`
To find completed items: `gramaton_search(resolution="completed")`

### Curation

The server runs background curation automatically every 5 minutes:
- **Deterministic** (always): lifecycle transitions, orphan linking,
  duplicate consolidation, concept enrichment (evidence_count,
  last_evidence_at), store manifest computation.
- **Autonomous** (when LLM configured): classifies pending records,
  generates missing summaries, detects semantic contradictions,
  generates qualitative store manifest summary.

Check status: `gramaton_curation(action="status")`
Trigger manually: `gramaton_curation(action="trigger")`
Preview without applying: `gramaton_curation(action="dry_run")`

**Piggyback curation (agent fallback):** When any response includes
`"curation": {"overdue": true, "autonomous": false}`, spawn a
subagent once per session to classify pending records:

1. Call `gramaton_pending` to get unclassified records
2. Process them in order, classifying each via `gramaton_classify`
3. Search for related records and link them via `gramaton_link`

When `autonomous: true`, the server handles classification. Do not
duplicate its work.
5. Do not interrupt the user. Run entirely in the background.
