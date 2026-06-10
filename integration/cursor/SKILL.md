---
name: gramaton
description: "Persistent memory for this user via Gramaton MCP tools. Use when the user references past decisions, prior sessions, project context, or preferences; mentions a ticket (a ULID or project ticket codename); says remember, save, or store; asks about plans, status, or architecture; or works with tasks, TODOs, and backlogs (collections). Covers search, save, session extraction, and collection workflows."
---

<!-- gramaton-managed v=0.1.0 (don't edit by hand — re-run `gramaton init --force` to update) -->

## Knowledge Store (Gramaton)

Gramaton is your persistent storage for decisions, preferences, facts,
research, and any knowledge that should survive beyond this session.
Gramaton has three save paths:

- **Memory** (`gramaton_save`, `gramaton_search`) — fuzzy, semantic
  knowledge: decisions, context, research, preferences. Ranked
  retrieval, best-match results. Direct saves are user-initiated
  only; Sessions also promote records here.
- **Sessions** (`gramaton_session_prepare`, `gramaton_session_save`)
  — automatic extraction from conversations. Two-phase flow.
  Produces both Session segments (for conversation recall) and
  Memory records (for semantic search), linked by an `extracted_as`
  edge.
- **Collections** (`gramaton_collection_add`, `gramaton_collection_items`)
  — structured, exhaustive data. Tasks, TODOs, action items,
  checklists, backlogs. Every item always returned.

**Decision rule for knowledge vs tasks:** Will missing one item be
a failure? Yes = Collection. No = Memory (via save or session
extraction).

Gramaton is accessed via MCP tools. If MCP tools appear unavailable,
first try to restore them — tell the user the MCP server looks
disconnected and ask them to reconnect (for Cursor: toggle the gramaton server under Settings → MCP, or
confirm `gramaton serve` / `gramaton init` is running). Only fall
back to the CLI (`gramaton search "<query>" --top 5`,
`gramaton inspect <id>`) if MCP recovery is impractical in the
moment.

If you are unsure how Gramaton works, what a metadata field means,
or when to use a given tool, call `gramaton_guide(topic=...)`.
Topics: metadata, save, search, sessions, collections, curation,
temporal-queries. The guide is the authoritative live reference —
prefer it over assumptions from memory.

### Subagents and Gramaton

Cursor subagents inherit all tools from the parent, including
Gramaton's MCP tools, so a delegated task is able to write to the
store. Keep saves and session extraction in the main conversation,
and tell delegated tasks not to write to Gramaton: a subagent sees
only its task brief, and partial-context saves produce fragmentary
records.

### Retrieval

**When to search (triggers, not suggestions — act immediately):**

**Hard patterns — no judgment call needed. If the user's prompt
contains any of these, the first action is a Gramaton lookup, before
composing any substantive response:**
- A ULID (26 chars, starts with `0` or `1`, e.g. `01KPED88HKK...`).
  Prefer `gramaton_inspect(id=...)` — the record plus its related
  edges come back in one call.
- A ticket or decision codename your project uses (T-12, P0-3,
  D7-style identifiers). Prefer `gramaton_inspect` if you can
  resolve it to an ID from earlier context; otherwise
  `gramaton_search(text="<codename>")`.
- The phrases "our current thoughts on X", "current plan for X",
  "status of X", "where are we on X", "what did we decide about X",
  or the word "backlog" (any use). Use `gramaton_search`.

**Broader triggers (judgment-based, still act immediately when they apply):**
- Before answering questions about past decisions, project context,
  architecture, user preferences, or domain-specific knowledge.
- Before writing design content, methodology notes, or claims about
  the project's capabilities or state.
- When the user references something from a prior session ("we
  discussed this", "you know where to pick back up", "remember X").
- When reasoning through a decision that might have project-specific
  prior art (the project has already-made decisions that should
  inform this one).
- When you're unsure whether the user has expressed a preference before.

**Anti-pattern:** producing content from general knowledge when
project-specific prior context exists in the store. Empty-search cost
is seconds; missing-context cost is reasoning rebuilt from scratch.

**When NOT to search first:**
- When the user explicitly asks to store/save/add something —
  just save it directly.
- When you're writing code or editing files (search only if you need
  context to do the work correctly).

**How to search:**
1. `gramaton_search(text="<query>", top=5)` — find relevant records.
2. Scan the results — read `metadata_summary` for a quick trust
   assessment, `summary_short` for content relevance. Results
   include a `store` field (`"memory"` or `"session"`) indicating
   origin.
3. `gramaton_inspect(id="<id>")` for records that look relevant.
4. Use the retrieved knowledge to inform your response.

Text is optional — omit it for filter-only queries like "all
procedural records" or "unclassified records".

Search spans both Memory and Sessions by default. Narrow with
`store="memory"` or `store="sessions"` when you specifically want
one store.

**Useful search patterns:**
- Newest records: `gramaton_search(sort="created_at", top=10)`
- Unclassified: `gramaton_search(missing=["temporality"])`
- By tag: `gramaton_search(keywords=["auth", "migration"])`
- Stale records: `gramaton_search(sort="staleness", order="desc")`
- Orphans: `gramaton_search(max_edges=0)`
- Literal text: `gramaton_search(match="RWMutex")`
- Similar to a record: `gramaton_search(similar_to="<id>")`
- Exclude refuted: `gramaton_search(epistemic_status="!refuted")`
- Sessions only: `gramaton_search(text="...", store="sessions")`
- Store overview: `gramaton_stats()`
- Find duplicates: `gramaton_duplicates(threshold=0.92)`
- Graph traversal: `gramaton_explore(node_id="<id>", depth=2)`
- More patterns: `gramaton_guide(topic="search")`

Do NOT tell the user you're searching unless the results meaningfully
change your answer. Searching should be as invisible as reading a file.

### Interpreting Metadata

Results include a `metadata_summary` (human-readable) and raw fields.
Use the summary for quick assessment. Use raw fields when you need to
reason more carefully:

**confidence** (0.0-1.0): How likely this is correct.
- 0.9+: Highly reliable. Use confidently.
- 0.7-0.9: Reliable. Note uncertainty for critical decisions.
- 0.4-0.7: Uncertain. Mention the uncertainty to the user.
- <0.4: Low confidence. Don't rely on this without corroboration.

**temporality**: How time-sensitive.
- immutable: Always true (definitions, axioms). Trust fully.
- durable: Stable until contradicted. Trust unless old and unverified.
- temporal: Time-bound. May be stale — check last_accessed.
- ephemeral: Very short lifespan. Likely outdated unless very recent.

**epistemic_status**: Qualitative reliability.
- well_established: Broadly accepted. Use confidently.
- probable: Likely true. Acknowledge it's not certain.
- speculative: Uncertain. Present as speculation, not fact.
- contested: Conflicting evidence. Present both sides.
- refuted: Shown to be false. Do NOT present as true.

**knowledge_type**: Affects how to present.
- episodic: A specific event. Include time context.
- semantic: A general fact. Present as established knowledge.
- procedural: A how-to. Present as instructions.
- conceptual: A definition or principle. Present as foundational.
- reference: Lookup data. Present as-is.

### Save (User-Initiated)

Gramaton IS the knowledge store. When the user says "remember this",
"store this", or "save this" — call `gramaton_save` directly.
Do NOT search the filesystem, explore the codebase, or look for other
storage systems.

**`gramaton_save` is user-initiated only.** Do not call it
autonomously. Autonomous knowledge save from conversations happens
through the Session flow (see below), not through save.

For tasks, TODOs, or action items, use `gramaton_collection_add`
instead. For knowledge emerging from conversation without the user
asking, use session prepare/save.

When the user explicitly asks to store something, do it immediately —
no search-first, no exploration. Just save.

**Do NOT save:**
- Trivial exchanges, greetings, small talk.
- Questions without answers.
- Work-in-progress that hasn't solidified.
- Your own generated responses or analysis.

**How to save:**
Call `gramaton_save` directly from the main conversation — do NOT
delegate saves to subagents or background tasks. Saves are fast (a
single call, under a second), and the knowledge being saved lives
in your context, not a subagent's.

**IMPORTANT: Save raw content, not summaries.** The `content`
field should contain the actual source material — the full decision
text, the exact conversation excerpt, the complete reasoning. Do NOT
pre-digest or summarize. Curation generates `content_short` and
embeddings from the raw content. The attention funnel (content_full
→ content_short → embeddings → BM25) is designed to compress — that
is curation's job, not yours. Every layer of agent pre-summarization
loses information.

1. Classify the content (temporality, confidence, knowledge_type,
   epistemic_status).
2. Extract keywords and write a `summary_short` (~750 chars; this is
   the embedding-ready semantic anchor — semantically representative,
   not a tagline).
3. Save:
   ```
   gramaton_save(
     content="[the knowledge]",
     temporality="[value]",
     confidence=[float],
     knowledge_type="[value]",
     epistemic_status="[value]",
     keywords=["keyword1", "keyword2"],
     summary_short="[~750 chars; semantic anchor for embedding]",
     context_about="[topic/domain]",
     context_who="[entities involved]",
     context_findable_by="[future retrieval terms]",
     asserted_as_of="[RFC3339, only if source claim date differs from now]",
     meta={"key": "value"}
   )
   ```
   Use `meta` for structured fields from source systems (e.g.
   `{"assignee": "Sarah", "priority": "P1", "sprint": 23}`). Meta
   values are indexed for keyword search and filterable via
   `gramaton_search(meta={"assignee": "Sarah"})`.
4. Search for related records and link them:
   ```
   gramaton_search(text="[key terms]", top=3)
   gramaton_link(id="[new-id]", target_id="[related-id]",
     edge_type="related_to", edge_weight=0.7)
   ```

**Auto-supersession:** When a saved record is very similar to an
existing one (>= 0.92 cosine similarity), the server automatically
marks the older record as historical (sets `valid_until`) and creates
a `supersedes` edge. You do not need to check for duplicates before
saving — the server handles it.

### Sessions (Automatic Knowledge Extraction)

Sessions are how Gramaton saves knowledge from conversations
without the user having to ask. The primary autonomous-save path.
Two-phase flow:

1. **Prepare** — `gramaton_session_prepare(session_id)` returns
   extraction instructions plus current session state (already-
   saved segments, for dedup).
2. **Save** — `gramaton_session_save(session_id, segments)`
   submits extracted segments. Each segment becomes a Session segment
   (BM25-indexed, saves the conversation thread). When
   `promote_to_memory: true` (the default when omitted), it ALSO
   becomes a Memory record (vector-embedded, full lifecycle,
   auto-supersession). Set `promote_to_memory: false` for
   exploration, open questions, and dead ends — they stay searchable
   in Sessions without polluting Memory's vector space.

**Do not call save without calling prepare first.** The server
rejects save without a prior prepare because prepare delivers the
extraction instructions you need to produce good segments.

**When to call prepare/save — mandatory triggers:**

1. **A save-worthy decision lands.** You implemented a feature,
   finished a rewrite, resolved a debate, or chose an approach after
   considering alternatives.
2. **The user says "done" / "ship it" / "that works" / "okay" in
   response to completed work.** Save before moving to the next
   topic.
3. **You finish a multi-step task tracked in a task list.** After the
   last task flips to `completed`, save.
4. **The user pivots topics.** They're done with topic A and moving
   to topic B. Save topic A's outcomes before you context-switch.
5. **Before context compaction.** Any mention of compacting, running
   low on context, or `/compact` — extract FIRST, then let
   compaction happen.
6. **The user explicitly asks** to save, remember, or store
   something about the session.

**Scheduled checkpoint:** Regardless of the triggers above, if you
have not called prepare/save in the last ~10 assistant turns of a
substantive conversation, do it now. The 10-turn clock resets on
every save.

**Finding the session_id.** Run `gramaton session current` — returns
`{"session_id": ..., "client_session_id": ...}` for the session bound
to your current working directory. If nothing is bound yet (hooks
not installed), start a session with `gramaton_session_start`. Safe
under multiple concurrent Cursor instances; each working
directory gets its own session file.

For the full guide on extraction triggers, segment granularity,
classification, and what makes good segment content, call
`gramaton_guide(topic="sessions")`.

### Collections

Collections are structured containers with guaranteed exhaustive
retrieval. Use for tasks, TODOs, action items, backlogs, checklists —
anything where missing an item is a failure.

**When to use collections (NOT gramaton_save):**
- "Add a TODO" — `gramaton_collection_add`
- "What are my open tasks?" — `gramaton_collection_items`
- "Mark this task done" — `gramaton_collection_update`
- "Move this to the done list" — `gramaton_collection_move`

**Key tools:**
- `gramaton_collection_create` — create with optional schema
- `gramaton_collection_items` — list ALL items (exhaustive)
- `gramaton_collection_add` — add item (schema-validated)
- `gramaton_collection_update` — update item fields
- `gramaton_collection_move` — move between collections
- `gramaton_collection_remove` — remove from collection
- `gramaton_collection_list` — list all collections

Collections have optional schemas that enforce field types and
required fields. Items in collections are also graph nodes and can
be linked to knowledge records via `gramaton_link`.

### Curation

The server runs background curation on a configurable cadence to
classify pending records, link orphans, detect duplicates, and
synthesize concept nodes. You don't need to trigger it manually — but
you can inspect or force it:

- `gramaton_curation(action="status")` — see pending count + last cycle
- `gramaton_curation(action="trigger")` — run a cycle now
- `gramaton_curation(action="dry_run")` — preview without applying

Every response includes a `curation` envelope field showing pending
record count and whether autonomous curation is configured. If
`autonomous: false`, the server lacks an LLM provider and you may
need to classify pending records manually as a fallback.

### Other Tools

- **`gramaton_resolve`** — Mark a record as resolved (completed,
  superseded, abandoned, obsolete). Auto-sets `valid_until`.
- **`gramaton_update`** — Update metadata on a record without
  reclassifying.
- **`gramaton_explore`** — Graph traversal from a node; returns
  connected nodes and edges within a depth.
- Store admin (rarely needed mid-conversation): `gramaton_branch`
  (store version control), `gramaton_backup`, `gramaton_reembed`,
  `gramaton_log` (commit / per-record history).
