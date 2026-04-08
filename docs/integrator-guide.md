# Integrator Guide

How to build agents and tools that use Gramaton effectively.

## Two Retrieval Modes

Gramaton provides two complementary storage systems. Choosing the right one is the most important integration decision.

### Knowledge Graph

Semantic, fuzzy retrieval. Best matches ranked by relevance.

**Use when:** you want the most relevant context for a question. Missing a low-relevance result is acceptable.

**Examples:** architecture decisions, research findings, user preferences, design rationale, meeting notes, domain knowledge.

**How it works:** Records are stored with optional metadata (confidence, temporality, epistemic status). Search combines vector similarity and BM25 keywords, then scores by similarity, freshness, activation, and confidence. Results are ranked, not enumerated.

**Key tools:** `gramaton_capture`, `gramaton_search`, `gramaton_inspect`, `gramaton_explore`, `gramaton_observe`

### Collections

Structured, exhaustive retrieval. Every item, guaranteed complete.

**Use when:** missing an item is a failure. You need to see ALL items, not the best ones.

**Examples:** task lists, sprint backlogs, reading lists, checklists, schedules, inventories.

**How it works:** Collections are named containers with optional schema enforcement. Items are validated on entry. Listing a collection returns every item -- no ranking, no cutoff, no "top N."

**Key tools:** `gramaton_collection_create`, `gramaton_collection_items`, `gramaton_collection_add`, `gramaton_collection_update`, `gramaton_collection_move`, `gramaton_collection_remove`

### Decision Rule

> Will missing one item be a failure?
> **Yes** -- use a collection. **No** -- use the knowledge graph.

## Knowledge Graph Best Practices

### Capturing Knowledge

**What to capture:**
- Decisions with reasoning ("chose Kafka because we need replay")
- Facts that will be useful in future sessions
- Preferences and constraints
- Research findings and domain knowledge

**What NOT to capture:**
- Trivial exchanges, greetings, confirmation messages
- Information already in the codebase or git history
- Temporary debugging state
- Exact duplicates of existing records (auto-supersession handles near-duplicates)

**Capture raw content, not summaries.** The `content` field should contain the actual source material -- the full decision text, the exact reasoning. Curation generates summaries and embeddings from the raw content. Pre-digesting loses information.

### Metadata Classification

Set metadata at capture time when you know it. Leave fields empty when uncertain -- curation will classify pending records.

| Field | When to set | When to leave empty |
|-------|-------------|---------------------|
| `temporality` | You know the time horizon | Unclear how long it's relevant |
| `confidence` | You have a basis for the number | Guessing |
| `knowledge_type` | It's clearly episodic/semantic/etc. | Ambiguous |
| `epistemic_status` | It's clearly established/speculative/etc. | Uncertain |
| `keywords` | You can name 3-8 specific, searchable terms | Can't think of good ones |
| `summary_short` | You can write a concise summary | Content is already short |

### Search Patterns

**Basic search:**
```json
{"text": "event pipeline architecture"}
```

**Filtered search:**
```json
{
  "text": "caching strategy",
  "temporality": "durable",
  "confidence_min": 0.7,
  "epistemic_status": "!refuted"
}
```

**Filter-only (no text):**
```json
{
  "keywords": ["kafka"],
  "sort": "created_at",
  "order": "desc"
}
```

**Useful patterns:**
- `resolution: "unresolved"` -- open items
- `missing: ["temporality"]` -- unclassified records
- `max_edges: 0` -- orphan records
- `sort: "staleness", order: "desc"` -- stale records
- `similar_to: "<id>"` -- find related records

### Using Meta Fields

The `meta` field stores structured key-value data that is indexed for exact-match search.

```json
{
  "content": "Sprint planning outcome...",
  "meta": {
    "project": "auth-rewrite",
    "sprint": "23",
    "status": "decided"
  }
}
```

Search by meta:
```json
{"meta": {"project": "auth-rewrite"}}
```

Good uses: project names, ticket IDs, source systems, categories. Bad uses: anything that needs fuzzy matching (use `text` search for that).

### Observe Pipeline

`gramaton_observe` extracts knowledge from conversation context. It's fire-and-forget -- call it often, the quality gates handle noise.

**When to call:**
- After completing a significant task
- When a decision is made or a preference stated
- When the conversation topic changes
- Before the session ends

**Input modes:**
- `facts: ["decided to use ACT-R model", "frequency signal is harmful"]` -- pre-extracted facts (preferred, cheaper)
- `messages: [{"role": "user", "content": "..."}]` -- raw conversation (server extracts facts)

Observed knowledge enters the graph as ephemeral, low-confidence records. Curation promotes good ones and bad ones decay naturally.

## Collection Best Practices

### Creating Collections

**Without schema** -- simplest, good for informal lists:
```json
{
  "name": "Reading List",
  "description": "Papers and articles to read"
}
```

**With schema** -- enforces structure:
```json
{
  "name": "Sprint Backlog",
  "schema": {
    "fields": [
      {"name": "title", "type": "string", "required": true},
      {"name": "status", "type": "enum", "required": true, "values": ["open", "in_progress", "done", "blocked"]},
      {"name": "priority", "type": "enum", "required": false, "values": ["p0", "p1", "p2", "p3"]},
      {"name": "assignee", "type": "string", "required": false}
    ]
  }
}
```

### Schema Field Types

| Type | JSON value | Example |
|------|-----------|---------|
| `string` | `"text"` | title, assignee, notes |
| `number` | `42` or `3.14` | estimate, position |
| `boolean` | `true` / `false` | blocked, reviewed |
| `date` | `"2026-04-07"` or RFC3339 | due_date, started_at |
| `enum` | `"open"` (from values list) | status, priority |
| `enum[]` | `["bug", "security"]` (from values list) | labels, tags |

### Collection Patterns

**PARA (Projects, Areas, Resources, Archive):**
- Projects: time-bound collections with goal and deadline
- Areas: ongoing collections with no end date
- Resources: reference collections
- Archive: retired collections (use `gramaton_collection_delete` to retire)

**Kanban:**
- Three collections: "Todo", "Doing", "Done"
- Move items between them with `gramaton_collection_move`

**Reading list:**
```json
{
  "name": "Reading List",
  "schema": {
    "fields": [
      {"name": "title", "type": "string", "required": true},
      {"name": "url", "type": "string", "required": false},
      {"name": "status", "type": "enum", "required": true, "values": ["unread", "reading", "read"]},
      {"name": "notes", "type": "string", "required": false}
    ]
  }
}
```

### Dedup Handling

When adding an item with a title that already exists in the collection, the server returns:
```json
{
  "duplicate": true,
  "existing_id": "01ABC...",
  "message": "item with title \"Buy milk\" already exists in this collection"
}
```

The agent decides: update the existing item, or add anyway. The server doesn't block the add -- it surfaces the match and lets the caller choose.

## What NOT to Build on Gramaton

Gramaton is infrastructure, not intelligence. It provides storage primitives. Build workflow and intelligence in your agent or application layer.

**Don't build in Gramaton:**
- Workflow automation (reminders, notifications, escalations)
- Personal assistant logic (task detection, scheduling suggestions)
- Routing intelligence (auto-detecting what belongs where)
- Cross-system sync (Jira, Linear, GitHub integration)

**Do build on Gramaton:**
- Storage for your assistant's task tracking
- Knowledge base for your agent's context retrieval
- Structured data store for your tool's state
- Version-controlled config/reference data

The boundary is: Gramaton stores and retrieves. Your agent thinks and decides.

## Agent Prompt Guidance

When writing system prompts or agent instructions for Gramaton integration:

1. **Separate graph from collections in the prompt.** Don't mix "search for context" with "check the task list." They are different operations with different tools.

2. **Be specific about when to search.** "Before answering questions about past decisions" is better than "search when relevant."

3. **Be specific about when to capture.** "After a decision is made" is better than "capture important things."

4. **Don't tell the agent to classify everything.** Let curation handle unclassified records. Only classify when the agent is confident about metadata.

5. **For collections, be explicit about the target.** "Add this to the Sprint Backlog collection" is better than "save this task."

6. **Trust the dedup.** Don't instruct agents to check for duplicates before capturing. The server handles auto-supersession (graph) and dedup detection (collections).

See the [Claude Code integration](../integration/claude-code/CLAUDE.md) and [Kiro specs](../integration/kiro/) for working examples.
