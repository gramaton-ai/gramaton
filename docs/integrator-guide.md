# Integrator Guide

How to build agents, tools, or applications on top of Gramaton.

This is the reference for people writing system prompts, designing skills, building MCP clients, or integrating Gramaton into an existing tool. If you want to change Gramaton itself, read [Architecture](architecture.md) and [CONTRIBUTING.md](../CONTRIBUTING.md) instead.

The guide is organized around the single most important integration decision: **which of the three storage paths should this piece of knowledge land in?** Everything else — prompt design, metadata, retrieval patterns — flows from that.

## The three storage paths

Gramaton isn't one bucket of retrieved-by-similarity notes. It offers three distinct paths with different retrieval guarantees. Each has its own lifecycle, its own MCP tools, and its own mental model. Choosing wrong means knowledge lands somewhere it can't be found the way the user will look for it.

### Memory — fuzzy, semantic, ranked

Best-match retrieval ranked by composite score. Not exhaustive.

**Use for:** decisions, design rationale, research findings, user preferences, domain knowledge, "what did we figure out about X" — anything where the goal is surfacing the most relevant few results, not every result.

**Retrieval:** `gramaton_search` with optional metadata filters. Results are ranked by a weighted combination of vector similarity (fused with BM25 keyword match via RRF), knowledge freshness (time-decay keyed to temporality), access recency (activation), and confidence. Importance acts as a floor.

**Write paths:**
- `gramaton_save` — user-initiated direct save. Full control over metadata.
- `gramaton_intake` — deliberate write endpoint. Same surface, optionally lets the server classify via LLM if one is configured.
- Session commit with `promote_to_memory: true` (the default) — each committed segment also lands as a Memory record.
- `gramaton ingest` CLI — bulk-load text files.

All four paths produce records with the same shape and the same retrieval semantics.

### Sessions — automatic extraction from conversations

Per-conversation save via a two-phase extraction flow. Optionally promotes segments to Memory.

**Use for:** knowledge that emerges during a conversation without the user explicitly asking you to remember it. Architectural decisions that landed while debugging. User preferences stated in passing. Dead ends that should be searchable but shouldn't compete with real decisions.

**Retrieval:** `gramaton_search` returns Session segments alongside Memory records by default. Each result row carries a `store` field whose value is `"memory"` or `"sessions"` indicating the origin. To narrow the query, pass the `store` filter with the same vocabulary -- `"memory"`, `"sessions"`, or `"all"` (default). The filter accepts `"session"` (singular) as a back-compat alias.

**Flow:**
1. `gramaton_session_start` — begin a session bound to a client session ID. Idempotent if called on an existing session.
2. `gramaton_session_prepare(session_id)` — returns extraction instructions plus the session state so far. Must be called before commit.
3. `gramaton_session_save(session_id, segments)` — submits extracted segments. Each segment creates a Session segment node (BM25-indexed) and, by default, a linked Memory record (vector-embedded, full lifecycle).

**`promote_to_memory`:** omit (default `true`) for decisions, facts, and preferences that should compete in semantic search. Set to `false` for exploration, open questions, and dead ends — they stay findable by session-scoped search but don't pollute Memory's vector space.

**Archiving raw transcripts** is opt-in at the Gramaton layer. The shipped hooks at `hooks/claude-code/` and `hooks/kiro/` wire it up automatically at compaction boundaries (`pre-compact.sh` calls `gramaton session archive`). The resulting archive sits compressed on disk and its path is recorded on the session node; the session state returned by `gramaton_session_get` and `_prepare` points at it so an agent can decompress and read the raw transcript if extracted knowledge seems incomplete. The archive itself is not indexed for search today.

### Collections — structured, exhaustive

Named containers with optional schema. Every item returned, guaranteed complete.

**Use for:** tasks, TODOs, action items, checklists, sprint backlogs, reading lists, inventories — anything where missing an item is a failure, not an acceptable relevance tradeoff. Items are also graph nodes and can be linked to Memory records.

**Retrieval:** `gramaton_collection_items` returns every matching item. No ranking, no cutoff, no top-N. Filter by field (exact match or any-of) and project specific fields with `fields: [...]`.

**Key distinction from Memory:** if you ask "what are my open tasks?" and the answer is "the three highest-relevance ones," that's a bug. Collections exist so that kind of bug can't happen.

### The decision rule

> *Will missing one item be a failure?*
> **Yes** → Collection.
> **No** → Memory (direct via save/intake, or via session extraction).

If the answer is ambiguous, start with Memory. It's easier to escalate a Memory record into a Collection item later than to back-fill a Collection with records that were originally saved as fuzzy memory.

## Memory — depth

### What to save

- Decisions with reasoning ("chose Kafka because we need replay")
- Facts that will still matter in future sessions (user preferences, constraints, "how we do X here")
- Research findings, domain knowledge, architectural context
- User statements you'll want to look up later ("the deadline is the 15th")

### What NOT to save

- Trivial exchanges, greetings, confirmation messages
- Information already in the codebase or git history ("file X exists", "function Y is called from Z")
- Temporary debugging state
- Intermediate reasoning that got superseded inside the same session
- Near-duplicates of existing records (the server auto-supersedes at ≥0.92 cosine — don't pre-check)

### Save raw content, not summaries

The `content` field should hold the actual source material — the full decision text, the exact reasoning, the literal user statement. Don't pre-digest. Gramaton's attention funnel (`content_full` → `content_short` → `content_keywords` → `embedding_*` → BM25) is designed to compress; that's curation's job, not yours. Every layer of agent-side summarization loses information that can't be reconstructed.

### Metadata classification

Set metadata at save time when you know it. Leave fields empty when uncertain — curation will classify pending records.

| Field | Set it when… | Leave empty when… |
|-------|--------------|-------------------|
| `temporality` | You know the time horizon (see below) | Unclear how long it's relevant |
| `confidence` | You have a basis for the number | Guessing |
| `knowledge_type` | It's clearly episodic / semantic / procedural / conceptual / reference | Ambiguous |
| `epistemic_status` | It's clearly established / probable / speculative / contested | Uncertain |
| `keywords` | You can name 3–8 specific, searchable terms a future question would TYPE | You can't think of good ones |
| `summary_short` | You can write a ~750-char semantic anchor | Content is already short |

**Temporality quick guide:**
- `immutable` — definitional truths that never change ("2+2=4")
- `durable` — stable until contradicted (architecture decisions, preferences, domain facts)
- `temporal` — time-bound, will decay ("sprint 23 is focusing on auth")
- `ephemeral` — minutes/hours lifespan ("the current debug branch is foo-bar")

### The context envelope

Content is what was said. The context envelope is everything else that was true when it was said. Five domain-neutral fields make a record findable by context, not just by the literal words:

| Field | What to put there |
|-------|------|
| `context_about` | Topic, domain, subject area |
| `context_who` | People, organizations, entities, systems involved |
| `context_prompted` | What prompted this knowledge to emerge *now* |
| `context_findable_by` | Terms, names, IDs someone might search for later |
| `context_related` | Known related records or topics |

Filling these deliberately at save time produces records that are retrievable by project, ticket ID, or team — not just by the exact words in `content`. The `gramaton_intake` tool expects them explicitly.

### `meta` for structured source data

The `meta` field stores structured key-value data from external systems. Values are flat (string / number / bool / string array) and indexed for exact-match search:

```json
{
  "content": "Sprint planning outcome: ship auth rewrite by 15th.",
  "meta": {
    "project": "auth-rewrite",
    "sprint": 23,
    "assignee": "Sara",
    "priority": "P1"
  }
}
```

Search by meta:
```json
{"meta": {"project": "auth-rewrite"}}
```

Good uses: project names, ticket IDs, source systems, status values, categories. Bad uses: anything that needs fuzzy matching — use `text` for that.

## Sessions — depth

### When to commit (triggers)

Session commit is the primary autonomous-save path. Call `prepare` then `commit` when any of the following happens:

- A save-worthy decision lands. A feature ships. A debate resolves. An approach gets chosen after considering alternatives.
- The user signals closure — "done", "ship it", "that works", "okay" in response to completed work.
- A multi-step task tracked in a plan or TODO list reaches its last completed step.
- The user pivots topics. Extract the outgoing topic's outcomes before context-switching.
- You're about to hit context compaction. Extract first, let compaction happen after.
- The user explicitly asks you to save, remember, or store something about the session.

**Regardless of those triggers,** if you've gone ~10 substantive conversation turns without calling commit, call it now. Don't wait for a "natural" breakpoint — by the time the natural breakpoint arrives you've blown past the window where the early reasoning is easy to reconstruct.

### What a good segment looks like

- `content` — unbounded, self-contained. Include rationale, alternatives, why-nots, concrete details (paths, numbers, names). Write it for a future reader who wasn't in the room.
- `summary_short` — ≤1000 chars (target ~750). This is the embedding-ready semantic anchor.
- `keywords` — 3–8 terms a future search would TYPE, not verbatim phrases from the conversation.
- `topic` — thematic cluster name. Multiple segments can share a topic.
- `temporality`, `confidence`, `knowledge_type`, `epistemic_status` — chosen per segment.

Segments with `promote_to_memory: false` stay in the Sessions store only. Use that for pure exploration and dead ends — they're still findable by session-scoped search, they just don't compete in Memory's vector space.

### Session state and the `recent_compaction` nudge

`gramaton_session_prepare` returns the session state: what's been committed so far. If the response includes a `recent_compaction` field, your context was just compacted — review the returned state carefully before extracting so you don't re-save knowledge that's already there.

If the session node has an archived transcript, the prepare response surfaces the archive path. Decompressing and reading it is an option of last resort when the in-session context is missing what you need to extract well.

### Save boundaries on multi-cycle sessions

Every successful `gramaton_session_save` returns a `boundary` object containing a bracketed `marker` string of the form `[gramaton-save-boundary T=<RFC3339>]`. On subsequent cycles in the same conversation, substring-scan your context for `[gramaton-save-boundary` and scope extraction to content appearing AFTER the most recent match — everything before has already been saved. `gramaton_session_prepare` carries the matching `session_state.last_saved_at` watermark (omitted on the first prepare of a session) and returns pre-boundary segments without their `content` field; `summary_short` plus topic context is enough to recognise already-saved ground without re-shipping the full content.

### Sessions don't close collection items

If the conversation worked through (and finished) a set of collection items — open tickets in a backlog, action items on a checklist — the session saves the *conversation* but does not flip those items' status. Closure is an explicit `gramaton_resolve` call per item; see "Closing items is an explicit action" under Collections below for the agent pattern. Skipping closure is the most common visible-drift bug: post-commit Memory records describe finished work while the underlying collection still lists those items as open.

## Collections — depth

### Creating a collection

Without a schema — simplest, good for informal lists:
```json
{"name": "Reading List", "description": "Papers and articles to read"}
```

With a schema — enforces typed fields and required fields:
```json
{
  "name": "Sprint Backlog",
  "schema": {
    "fields": [
      {"name": "title", "type": "string", "required": true},
      {"name": "status", "type": "enum", "required": true, "values": ["open", "in_progress", "done", "blocked"]},
      {"name": "priority", "type": "enum", "required": false, "values": ["P0", "P1", "P2", "P3"]},
      {"name": "assignee", "type": "string", "required": false},
      {"name": "due", "type": "date", "required": false}
    ]
  }
}
```

### Schema field types

| Type | Wire value | Typical use |
|------|-----------|-------------|
| `string` | `"text"` | title, assignee, notes |
| `number` | `42` or `3.14` (finite only — no NaN/Inf) | estimate, position |
| `boolean` | `true` / `false` | blocked, reviewed |
| `date` | `"2026-04-20"` or RFC3339 | due_date, started_at |
| `enum` | one of `values` | status, priority |
| `enum[]` | array of values | labels, tags |

Field names must match `^[a-zA-Z_][a-zA-Z0-9_]*$` — they become property keys on the underlying graph node. See `api/collection_schema.go` for the full validation rules.

### `content_fields` — what the LLM treats as the item's content

Schemas may declare an ordered `content_fields` list naming the fields that constitute the canonical text representation of an item. When set, `content_fields` drives both BM25 indexing and the vector embedding for that item: the server joins those field values (in the declared order) and the joined string is what gets embedded and tokenized. Editing a `content_fields`-listed field flips `processing_status` back to `saved` and queues a re-embed; editing other fields (status, dates, assignee) doesn't touch the index.

```json
{
  "schema": {
    "fields": [
      {"name": "title", "type": "string", "required": true},
      {"name": "details", "type": "string", "required": false},
      {"name": "status", "type": "enum", "required": true, "values": ["open", "in_progress", "done"]}
    ],
    "content_fields": ["title", "details"]
  }
}
```

Each name must reference a declared `type=string` field; non-string types are rejected at schema validation. The standard templates ship with explicit declarations (e.g. `backlog: ["title", "details"]`, `todo: ["title", "notes"]`, `reading-list: ["title", "author", "notes"]`, `journal: ["title", "entry"]`, `references: ["title", "description", "notes"]`).

`curation: standard` requires `content_fields` to be declared (via template or custom schema). `gramaton_collection_create` rejects schemaless `curation: standard` requests with a clear input error — the LLM has nothing structured to summarize against. `curation: none` collections (`shopping-list`, `packing-list`, ad-hoc schemaless) don't need `content_fields`; their items skip the LLM pipeline entirely.

### Adding, listing, updating, moving

- `gramaton_collection_create` — create a collection. Optional `schema` for field types and required fields (with optional `content_fields` declaration; see above), optional `template` (one of `backlog`, `todo`, `reading-list`, `shopping-list`, `packing-list`, `journal`, `references`) to seed schema + behavior fields, optional behavior fields. The four orthogonal knobs:
  - `curation` — `none` (skip the LLM pipeline; default for ad-hoc collections) or `standard` (classify, summarize, observation_extract, concept synthesis on items; requires `content_fields`).
  - `supersession` — `off` (no auto-supersede), `on` (auto-supersede within the collection at ≥0.92 cosine), `store` (auto-supersede against the whole store).
  - `contradictions` — `off` or `on` (gate this collection's items into the LLM contradiction-detection pipeline).
  - `clear_mode` — `resolve` (default; `gramaton_resolve` flips item status) or `unlink` (item is removed from the collection rather than resolved; useful for transient lists).
- `gramaton_collection_add` — validates fields against schema, creates an item node, returns its ID.
- `gramaton_collection_add_batch` — up to 500 items in one call. Schema-validated and dedup-checked per item; passing items commit atomically in one engine save, failing items are reported in the `Failed` array with per-item `{index, client_ref, code, message}`. Use instead of repeated `_add` when loading more than ~10 items.
- `gramaton_collection_items` — exhaustive list. `fields: [...]` projects a subset; `filter: {...}` narrows by exact field match or any-of. Pass `as_of=T` (RFC3339 or `YYYY-MM-DD`) to return membership at a historical commit; the response carries `semantics: point_in_time`.
- `gramaton_collection_update` — partial update, preserves unspecified fields.
- `gramaton_collection_move` — move between collections.
- `gramaton_collection_remove` — remove from collection (the underlying item node stays in the graph; it's just not a member of this collection anymore).
- `gramaton_collection_delete` — retire a whole collection.

### Dedup behavior

Duplicate-title handling depends on the collection's `curation` profile:

- **`curation: none`** (default; shopping-list / packing-list shape): a duplicate returns the existing item's id with `deduplicated: true` in the response — idempotent add, no error.
- **`curation: standard`** (declared via template or explicit `content_fields`; backlog / todo / reading-list / journal / references shape): a duplicate returns `ErrConflict`:

```
item with title "Buy milk" already exists in this collection (existing id: 01ABC...)
```

The server rejects the duplicate. The caller decides what to do: update the existing item via `_update`, add under a different title, or skip. The error message carries the existing item's ID so the caller doesn't need a second lookup.

### Patterns that work well

- **PARA** (Projects / Areas / Resources / Archive): four collections, one per category. Projects have a deadline; Areas are ongoing; Resources are reference; Archive holds retired items. Use `_move` between them.
- **Kanban**: collections for "Todo", "Doing", "Done". Move items with `_move`.
- **Named backlogs**: one collection per project or product surface, schema-enforced.
- **Link items to Memory records**: `gramaton_link` an item to a related decision or research record. Items are graph nodes; edges work across the Memory/Collection boundary.

### Closing items is an explicit action — sessions don't do it

Resolving a collection item (marking it `completed` / `superseded` / `abandoned` / `obsolete`) requires an explicit `gramaton_resolve` call. Session prepare/save saves the conversation that led to the work being finished but does NOT touch the item — `field.status` stays whatever it was, no `valid_until`, no resolution edge.

This is intentional. Closure is a deliberate state change with audit consequences (write to `valid_until`, `resolution`, optional `resolution_note`, plus an edge for the audit trail). Inferring closure from "the session said we shipped it" produces false positives that silently lose state on partial work.

The pattern for an agent wrapping up a topic:

1. Call `gramaton_resolve` for each completed item explicitly. When the resolved record is a collection item AND the schema declares an enum field named `status`, the server auto-flips that field to a closed-equivalent value (first `resolved` / `done` / `finished` / `abandoned` in the schema's enum, whichever appears first). This is `auto_close_collection_status: true` by default; pass `false` to skip the collection-side write and only stamp `valid_until` + `resolution`. Records in collections whose status field has no closed-equivalent value emit `CollectionWarning` so the agent can prompt the operator to call `gramaton_collection_update` manually.
2. THEN call `gramaton_session_prepare` + `gramaton_session_save`.

Order doesn't matter mechanically (independent subgraphs, engine serializes commits), but closure-first gives the session a clean view of the post-state. If you forget closure entirely, the user has visible drift: session-extracted Memory records say the work shipped, the collection still shows the items open.

## Search and retrieval

### Basic search

```json
{"text": "event pipeline architecture"}
```

Text is optional — omit for filter-only queries like "all procedural records" or "everything unclassified".

### Filtered search

```json
{
  "text": "caching strategy",
  "temporality": "durable",
  "confidence_min": 0.7,
  "epistemic_status": "!refuted"
}
```

`!value` negates — `epistemic_status: "!refuted"` excludes records marked as refuted while accepting everything else.

### Useful patterns

| Goal | Filters |
|------|---------|
| Newest records | `sort: "created_at", order: "desc"` |
| Open items awaiting action | `resolution: "unresolved"` |
| Unclassified records | `missing: ["temporality"]` |
| Orphans | `max_edges: 0` |
| Stale records | `sort: "staleness", order: "desc"` |
| Heavily connected | `min_edges: 3, sort: "edge_count"` |
| Related to a known record | `similar_to: "<id>"` |
| By keyword | `keywords: ["kafka", "replay"]` |
| Literal substring | `match: "RWMutex"` |
| High-importance only | `importance_min: 0.7, sort: "importance"` |
| Recently accessed | `sort: "last_accessed", order: "desc"` |
| Expiring soon | `expires_before: "2026-05-01"` |
| Sessions only | `store: "sessions"` (plural in the filter) |
| Memory only | `store: "memory"` |

### The retrieval funnel

The three retrieval tools are shaped as a funnel, cheap to expensive:

1. **`gramaton_search`** (cheap, broad) — returns lightweight results with raw metadata and a human-readable `metadata_summary` per row. Scan these first.
2. **`gramaton_inspect`** (medium) — full content, all properties, one-hop related edges for a single record. Call this when a `_search` hit looks promising.
3. **`gramaton_explore`** (graph traversal) — returns a subgraph fragment from a node, with edge-type and weight filters. For "what else is connected to this decision?"

Use the cheapest tier that answers the question. `_explore` on every hit is wasteful; `_search` with no follow-up leaves relationships on the table.

### Temporal queries

For "what changed", "when did this happen", or "what did the store look like at time T", use the temporal-query surface:

- **`gramaton_log`** — commit history. Filters: `actions` (semantic activity-level filter), `exclude_curation` (drop autonomous-curation noise), `include_record_mutations` (CRUD-level commit shapes). Date-range params: `since`, `until`.
- **`gramaton_diff`** — what changed between two commits or two dates. Accepts `since`/`until` for date-bounded queries.
- **`gramaton_history`** — per-record change history.
- **`gramaton_collection_items(as_of=T)`** — membership at a historical commit.

Search itself is the live (latest-only) axis. Call `gramaton_guide(topic="temporal-queries")` for the full taxonomy and patterns.

### CLI parity

Every MCP tool has a corresponding CLI subcommand. `gramaton --help` lists the surface; `gramaton <subcommand> --help` shows flags. Use this when you want to pipe to a script, run a one-off operation outside an agent context, or verify behaviour without standing up an MCP client.

## Agent prompt guidance

When writing system prompts or agent instructions for Gramaton integration:

1. **Separate the three storage paths in the prompt.** Don't mix "search Memory for context" with "check the task collection" with "commit session segments." They're different operations with different triggers.
2. **Be specific about when to search.** "Before answering questions about past decisions, project context, architecture, preferences, or domain knowledge" beats "search when relevant."
3. **Be specific about when to save.** For Memory, save only when the user explicitly asks. For Sessions, commit at the triggers listed above. For Collections, add when the user describes a task / backlog item / checklist entry.
4. **Don't tell the agent to classify everything at save time.** Let curation handle unclassified records. Classify only when the agent is confident about metadata.
5. **For collections, be explicit about the target.** "Add this to the Sprint Backlog collection" beats "save this task."
6. **Trust the dedup.** Don't instruct agents to pre-check for duplicates before saving Memory records — the server handles auto-supersession at ≥0.92 cosine, scoped per the collection's `supersession` knob. For Collections with `curation: standard` (declared via curation=standard templates or explicit `content_fields`), the server returns `ErrConflict` on duplicate titles; the agent decides what to do in response. For `curation: none` collections (the default for ad-hoc collections, plus shopping-list / packing-list templates), a duplicate returns the existing id with `deduplicated: true` — idempotent adds, no retry logic needed.
7. **Point the agent at `gramaton_guide`.** It's the live topic-addressable reference for save / search / sessions / collections / metadata / curation. Tell the agent to call it when unsure rather than guessing.

Working examples: [Claude Code integration](../integration/claude-code/CLAUDE.md), [Kiro specs](../integration/kiro/), and [custom agent frameworks](../integration/docs/custom-agents.md).

### Verifying the install

Run `gramaton preflight` after a fresh install (or after any major config change) to verify daemon health, embedding model availability, LLM connectivity, and config sanity. The command exits non-zero if anything blocks normal operation, so it's a fit for shell scripts or first-run docs in a downstream tool. To re-run the interactive setup wizard on an existing install (preserving API keys), use `gramaton init --force`.

## What NOT to build on Gramaton

Gramaton is infrastructure, not intelligence. It provides storage primitives and retrieval guarantees. Build workflow and judgment in your agent or application layer.

**Don't build in Gramaton:**
- Workflow automation (reminders, notifications, escalations)
- Personal assistant logic (task detection, scheduling suggestions)
- Routing intelligence (auto-detecting what belongs where)
- Cross-system sync (Jira, Linear, GitHub integration as a Gramaton feature)
- UI (it's an MCP server + CLI; build your UI in your client)

**Do build on Gramaton:**
- Storage for your assistant's task tracking (Collections)
- Knowledge base for your agent's context retrieval (Memory)
- Automatic extraction of conversation outcomes (Sessions)
- Structured data store for your tool's state (Collections with schema)
- Version-controlled config/reference data (branches + commits)

The boundary: **Gramaton stores and retrieves. Your agent thinks and decides.**

## Live reference

`gramaton_guide(topic=...)` is the authoritative in-MCP reference. Topics as of this writing: `save`, `search`, `sessions`, `collections`, `metadata`, `curation`, `temporal-queries`. The guide content lives in the repo at `server/guide/*.md` and ships in the binary — it updates with each release, so it's always in sync with the running server's behavior.

When you're unsure about a field, a trigger, or a flow, call the guide rather than guessing. That's its job.
