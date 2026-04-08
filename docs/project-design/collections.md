# Collections

Structured containers for exhaustive knowledge management.

**Status:** Design draft. Under active iteration.

## Problem

Gramaton's knowledge graph excels at fuzzy, semantic retrieval -- finding the best matches for a query, ranked by relevance. But some information needs **exhaustive recall**: tasks, schedules, reading lists, project backlogs, checklists. Missing one item is a failure, not a relevance tradeoff.

Users and agents naturally try to store structured items (TODOs, action items, shopping lists) in the knowledge graph. This fails silently:

1. Items get lost in the graph -- no query guarantees completeness
2. Agents capture inconsistently -- some items get the right keywords, some don't
3. There's no lifecycle management -- no way to reliably track what's open vs. done
4. The user assumes the system is tracking their items. It isn't.

The worst case: an agent stores a TODO on behalf of the user, reports success, and the item is never reliably retrievable again. The user trusts the system. The system fails silently.

## Two Modes of Knowledge

The insight is that there are two fundamentally different information needs, and they require different retrieval guarantees:

| | Knowledge Graph | Collections |
|---|---|---|
| **Retrieval** | Ranked relevance (best N) | Exhaustive (all items) |
| **Tolerance for missing items** | Fine -- low-relevance misses are acceptable | Zero -- every item must be visible |
| **Structure** | Loose, flexible metadata | Schema-enforced, required fields |
| **Capture mode** | Passive (observe) or explicit | Explicit only |
| **Validation** | Lenient, curation cleans up later | Strict, rejects invalid items |
| **Natural representation** | Searchable graph | Ordered list / document |
| **Examples** | Decisions, context, research, preferences | Tasks, schedules, reading lists, checklists |

These are not competing approaches. They are complementary halves of a complete knowledge management system. Gramaton currently only has the first half.

## What Is a Collection

A collection is a named container with:

- **Identity** -- a name, description, and optional schema
- **Schema** -- what fields items must/may have (flexible, user-defined)
- **Membership** -- explicit list of items. Not inferred, not searched -- enumerated
- **Ordering** -- explicit position, or sorted by a field
- **Exhaustive retrieval** -- "list all items in this collection" is guaranteed complete
- **Lifecycle** -- items move between collections (e.g., backlog -> active -> done -> archive)

A collection is NOT:
- A search query saved as a filter
- A tag or keyword grouping
- A graph traversal result

The difference matters. Search results are approximate. Collection membership is definitive.

## Collections in the Graph

A collection is a first-class node in the knowledge graph with `knowledge_type: collection` (or a dedicated type). Its items are linked via `member_of` edges. This means:

- Knowledge records in the graph can **link to** collection items (and vice versa)
- A TODO "implement ACT-R scoring" in a sprint collection links to the design rationale node explaining why
- Collection nodes appear in graph traversal -- `gramaton explore` on a collection shows its items and their connections
- The graph provides semantic context around structured items

The collection node is the bridge between structured and semantic knowledge. The collection enforces structure on its members. The graph provides meaning around them.

```
  [Sprint 23]                     Knowledge Graph
  ├── task: implement ACT-R ──links to──► [ACT-R design rationale]
  ├── task: add Bedrock provider           [Anderson & Schooler 1991]
  └── task: security review ──links to──► [threat model analysis]
```

## Schema Enforcement

Collections can define a schema -- what fields items must have. The server enforces this at capture time.

```yaml
# Example schema: a project backlog
name: sprint-backlog
schema:
  required:
    - title        # string
    - status       # enum: open, in_progress, done, blocked
  optional:
    - priority     # enum: p0, p1, p2, p3
    - assignee     # string
    - due_date     # date
    - estimate     # string
```

If a capture targets this collection and is missing `status`, the server rejects it. Not a warning -- a failure. The agent must provide the field or route the item elsewhere.

Schemas are optional. A collection without a schema is just an ordered list with guaranteed membership tracking. A reading list doesn't need priority fields. A grocery list doesn't need status tracking.

### Schema Evolution

Schemas change. A backlog that starts with `title` and `status` eventually needs `priority`. Adding a required field to a schema with existing items is a two-step operation:

1. **Update the schema.** `collection_schema update` adds the new field. The collection enters a **migration state**. New items must include the field. Existing items are flagged as pre-migration.

2. **Migrate existing items.** The agent or user backfills the field on existing items, either individually or in bulk:
   - `collection_migrate backlog --field priority --value p2` (set all to p2)
   - `collection_migrate backlog --field priority --value null` (explicitly null -- "I don't care")
   - Item-by-item via `collection_update`

The migration completes when all items comply. The server tracks progress ("34 of 47 items migrated").

Principles:
- **No silent defaults.** The server never fabricates a value. Even "null" is a deliberate choice made by the user or agent.
- **No silent backfill.** Existing items are not retroactively modified without an explicit migration step.
- **Every field value has provenance.** It was set at capture, set during migration, or set by an explicit update. The commit history records which.
- **Collections remain usable during migration.** Items list, reads, and new captures all work. Pre-migration items are annotated so nothing is ambiguous.

Removing a field or making a required field optional is always safe -- no migration needed.

## Capture Routing

Routing is always explicit. The agent or user specifies a target collection at capture time:

```json
{
  "collection": "sprint-backlog",
  "title": "Add Bedrock provider",
  "status": "open",
  "priority": "p1"
}
```

The server validates against the collection's schema. If valid, the item is added. If not, the capture fails with a clear error describing what's missing.

Captures without a `collection` field go to the knowledge graph, as they do today. The server never infers, suggests, or auto-routes. Routing is the caller's decision.

Passive capture (observe) always feeds the knowledge graph. Never collections. The two paths are cleanly separated:
- **Graph**: autonomous, passive, low-friction
- **Collections**: intentional, validated, explicit

## Lifecycle

Items move between collections. This is how lifecycle management works:

```
[Backlog] ──move──► [Active] ──move──► [Done] ──move──► [Archive]
```

Moving an item updates its `member_of` edge. The item retains all its properties, links, and history. The graph records the transition (commit history shows when it moved and from where).

## Retrieval

Collection retrieval is fundamentally different from graph search:

```
# Exhaustive -- returns ALL items, guaranteed
gramaton collection list sprint-backlog

# Filtered -- still exhaustive within the filter
gramaton collection list sprint-backlog --status open

# Ordered
gramaton collection list sprint-backlog --sort priority --order asc
```

No relevance scoring. No vector search. No "top N." Every item in the collection is returned. The user sees a complete picture.

This is the core trust guarantee: if you put something in a collection, you will always get it back when you list that collection. The system cannot silently lose it.

## Gramaton's Role

Gramaton is infrastructure, not intelligence. Collections provide storage primitives -- not workflow, not project management, not personal assistance.

What Gramaton does:
- Store collections with schema enforcement
- Guarantee exhaustive retrieval
- Enforce validation on capture and update
- Track membership, ordering, and history
- Expose CRUD tools for agents and external systems to consume

What Gramaton does NOT do:
- Detect that something "looks like a task"
- Suggest routing from graph to collection
- Manage workflows or automations
- Send reminders or notifications
- Make decisions about what belongs where

Personal assistants, project management agents, and workflow tools integrate WITH Gramaton by calling its collection tools. Gramaton provides the reliable storage layer. The intelligence lives elsewhere.

## MCP Tools

```
gramaton_collection_list    -- list all collections
gramaton_collection_create  -- create a collection with optional schema
gramaton_collection_items   -- list all items in a collection (exhaustive)
gramaton_collection_add     -- add an item to a collection (schema-validated)
gramaton_collection_update  -- update an item's fields
gramaton_collection_move    -- move an item between collections
gramaton_collection_remove  -- remove an item from a collection
gramaton_collection_schema  -- read or update a collection's schema
gramaton_collection_migrate -- bulk-update items for schema migration
gramaton_collection_rename  -- rename a collection
gramaton_collection_delete  -- retire/delete a collection
```

Items in collections are also graph nodes. They can be inspected, linked, and explored like any other node. The collection tools are a structured lens on top of the graph, not a separate system.

## Relationship to PARA

PARA (Projects, Areas, Resources, Archive) is one organizational system that maps naturally to collections:

| PARA Category | Collection Pattern |
|---|---|
| **Projects** | Active, time-bound collections with goal and deadline |
| **Areas** | Ongoing collections with no end date (standards to maintain) |
| **Resources** | Reference collections (reading lists, tool inventories) |
| **Archive** | Inactive collections, retained for history |

But collections are not PARA-specific. A kanban board is three collections (Todo, Doing, Done) with move transitions. A reading list is one collection with a "read" status field. A sprint backlog is a collection with priority and estimate fields.

The primitive (structured containers with schema and exhaustive retrieval) supports many organizational patterns without hardcoding any of them.

## Agent Trust

The system must enforce correctness. Agent conventions are suggestions; enforcement is the product.

Principles:
- **Schema violations are errors.** If a collection requires `status` and the agent doesn't provide it, the capture fails. The agent gets feedback and can retry.
- **Membership is explicit.** Items are in a collection because they were added to it, not because they match a query. No inference, no drift.
- **Deduplication is server-side.** If an item with the same identity already exists in the collection, it's an update, not a duplicate. The server decides, not the agent.
- **Routing is always explicit.** The caller specifies the target collection. The server never infers or suggests.

This means agents can interact with collections without perfect behavior. The server catches structural mistakes. The worst case is a rejected capture with a clear error -- not a silent loss.

## Resolved Questions

1. **Collection identity** -- both name (human-readable, unique within a store, enforced) and ULID (internal). Names allow spaces, mixed case, punctuation. Case-insensitive lookup. Max 128 chars.

2. **Item identity / dedup** -- server does best-effort detection (title match, content similarity within the collection). If potential duplicate found, response surfaces the match and the agent decides: update existing or add new. Ambiguity is the agent's problem.

3. **Schema format** -- YAML for human authoring, JSON via MCP tools. Stored on the collection node in the graph (schema-in-the-data). Schemas travel with the store on export/import. No separate registry or config directory.

4. **Collection templates** -- start blank. No shipped templates. Templates are opinionation we don't need yet.

5. **Cross-collection queries** -- deferred. Single-collection queries first.

6. **Ordering** -- hybrid. Field-based sort is the default (conflict-free, agent-friendly). Collections that need manual ordering opt in by adding a `position` field to their schema. Position conflicts resolved by server (fractional insert or append).

7. **Collection size limits** -- no hard limit enforced by the system.

8. **Permissions** -- no permissions system. All collections are user-managed. Gramaton does not create or manage collections on anyone's behalf.

9. **LLM involvement** -- none. Collections are LLM-free by design. Pure CRUD with validation. The LLM's role is in the agent that calls collection tools, not in Gramaton.

10. **Schema field types** -- six types: `string`, `number`, `boolean`, `date`, `enum` (closed, values predefined in schema), `enum[]` (multi-select, values predefined). Flat fields only, no nesting, no arrays beyond enum[]. Start strict, expand later if needed.

11. **Collection retirement** -- retirement sets `valid_until` on the collection node. `member_of` edges are kept, not removed. `collection_items` skips retired collections by default. `collection_items --include-retired` shows them. Unretire by clearing `valid_until` -- all items come back. No data loss. Consistent with tenet 8.

12. **Multi-collection membership** -- allowed. An item can belong to multiple collections via multiple `member_of` edges. Removing from one collection doesn't affect other memberships. Schema enforcement applies per collection.

## Open Questions

1. **Versioning** -- collections live in the same versioned store. Diffing a collection over time should work naturally with existing commits, but needs verification during implementation.
