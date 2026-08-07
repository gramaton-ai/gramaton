# Collections Guide

Collections are structured containers with guaranteed exhaustive retrieval. Every item is always returned.

## When to Use Collections

Use for tasks, TODOs, action items, backlogs, checklists -- anything where missing an item is a failure.

**Decision rule:** Will missing one item be a failure? Yes = collection. No = Memory (gramaton_save).

## Routing: which collection?

Each collection carries a `description` saying what it is for. Before filing an item, check the descriptions returned by `gramaton_collection_list` and pick the collection whose description matches the item -- don't route on name alone, and don't create a new collection when an existing description already covers the item. `gramaton_collection_add` (and the batch variant) echoes the target's name and description in its response, so a mis-filed item is visible immediately; use `gramaton_collection_move` to correct it.

## Operations

- `gramaton_collection_create`: Create with optional schema, `template` (one of `backlog`, `todo`, `reading-list`, `shopping-list`, `packing-list`, `journal`, `references`), and behaviour fields (`curation`, `contradictions`, `clear_mode`).
- `gramaton_collection_list`: List all collections.
- `gramaton_collection_items`: List ALL items (exhaustive). Pass `as_of=T` (RFC3339 or `YYYY-MM-DD`) for point-in-time membership at a historical commit; the response carries `semantics: point_in_time`.
- `gramaton_collection_add`: Add item (schema-validated). On collections with `curation=none`, a duplicate title returns the existing id with `deduplicated: true` (idempotent add). On `curation=standard`, a duplicate returns `ErrConflict`.
- `gramaton_collection_add_batch`: Add up to 500 items in one two-phase call. Best-effort per-item failure reporting; intra-batch dedup is first-write-wins.
- `gramaton_collection_update`: Update item fields.
- `gramaton_collection_move`: Move between collections.
- `gramaton_collection_remove`: Remove from collection.
- `gramaton_collection_rename`: Rename a collection.
- `gramaton_collection_delete`: Retire a collection (clear `valid_until` to unretire — items return, no data loss).
- `gramaton_collection_schema`: Read a collection's schema.
- `gramaton_collection_migrate`: Bulk-update items for a schema migration.

## Schemas

Collections can have optional schemas that enforce field types and required fields. Field types: `string`, `number`, `boolean`, `date`, `enum` (closed, values predefined in schema), `enum[]` (multi-select).

### `content_fields`: which fields drive LLM curation + embedding

Schemas may declare an ordered `content_fields` list naming the fields that constitute the canonical text representation of an item. The list drives:

- **LLM-stage curation** — classify, summarize, contradictions, concept synthesis read this text as their input.
- **Vector embedding** — items embed against the joined `content_fields` text (plus the collection's name + description as context). Reembed converges on the same text.

Each name must reference a declared `type=string` field; non-string types are rejected at schema validation. The five `curation=standard` templates ship with explicit declarations:

| Template | content_fields |
|---|---|
| `backlog` | `[title, details]` |
| `todo` | `[title, notes]` |
| `reading-list` | `[title, author, notes]` |
| `journal` | `[title, entry]` |
| `references` | `[title, description, notes]` |

`curation=standard` requires `content_fields` declared (via template or custom schema). `gramaton_collection_create` rejects schemaless `curation=standard` requests with a clear error. `curation=none` collections (`shopping-list`, `packing-list`, plus all schemaless ad-hoc collections by default) don't declare `content_fields`; their items skip the LLM pipeline entirely. New schemaless collections default to `curation=none`; explicitly pass `curation=standard` only when paired with a schema that declares `content_fields`.

When an item's `content_fields`-output text changes via `gramaton_collection_update`, the BM25 index and vector embedding refresh and `processing_status` flips to `captured` so the next curation cycle reclassifies the item. Updates to non-`content_fields` fields (status enums, dates) leave the indexes and pipeline state untouched.

## Behaviour fields (per-collection knobs)

Three orthogonal knobs control how curation and clear semantics treat records in a collection. Set them per-collection at create time, or rely on the template defaults.

- `curation` (`standard` | `none`) — LLM analysis intensity. `standard` runs classify, summarize, observation_extract, concept synthesis on items. `none` disables all LLM work; use for shopping/packing-list shapes where classification adds no value. On `curation=none`, `collection_add` is also idempotent (duplicate title returns the existing id).

- `contradictions` (`on` | `off`) — whether the system generates `contradicts` edges from records in this collection. `on` runs the LLM-driven contradiction stage. `off` skips it; useful for reference shapes (bookmarks, recipes, places) where two recommendations don't contradict each other.

- `clear_mode` (`resolve` | `unlink`) — what "clearing" or resolving an item does to its membership. `resolve` (default) keeps the item as a member of the collection but stamps `valid_until` and (when the schema has an enum `status` field) flips that field to a closed-equivalent value -- the item stays in the historical record so "what did I do last week" still works. `unlink` removes the `member_of` edge so the underlying record stays in the graph but is no longer a collection member; useful when items represent reusable entities (an unread book stays a record you can re-add to the reading-list later). Resolved items remain searchable as historical records; unlinked items keep their identity outside the collection.

### Multi-collection resolution

A record can belong to multiple collections (one record, multiple `member_of` edges). The effective values are resolved per-knob:

- `curation` is additive (adds metadata). **Most-permissive wins** (`standard` > `none`).
- `contradictions` is additive (creates edges). **Most-permissive wins** (`on` > `off`).

One-line principle: always enrich when any collection wants enrichment.

`gramaton_inspect` returns the resolved values as `effective_curation: {curation, contradictions}` so you can see exactly what curation work will run on a given record.

## Templates

Seven starter templates seed schema + the three behavior knobs:

| Template | curation | contradictions | clear_mode | Use case |
|---|---|---|---|---|
| `backlog` | standard | on | resolve | Engineering/product backlog with priority + status. Surface design conflicts. |
| `todo` | standard | on | resolve | Generic actions list with status + due-by. |
| `reading-list` | standard | off | unlink | Articles/books with notes. An unread book stays a record you can re-add later. |
| `shopping-list` | none | off | resolve | Short-content list ("milk", "eggs"). Resolve preserves "when did I last buy eggs" history. |
| `packing-list` | none | off | resolve | Trip checklist. Same shape as shopping-list. |
| `journal` | standard | off | resolve | Daily entries / observation logs. Append-only; no contradiction-checking. |
| `references` | standard | off | resolve | Bookmarks, recipes, places, contacts, snippets. Lookup-data shape. |

Pass `template=<name>` to `gramaton_collection_create`; caller-supplied fields override template defaults.

## Cross-Store Linking

Collection items are graph nodes and can be linked to Memory records via `gramaton_link`.

## Related Topics

- `gramaton_guide(topic="save")` — Memory vs Collections decision rule.
- `gramaton_guide(topic="curation")` — stage-by-knob mapping for the curation pipeline.
- `gramaton_guide(topic="temporal-queries")` — `as_of=T` semantics and the broader temporal-query surface.
