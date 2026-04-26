# Collections Guide

Collections are structured containers with guaranteed exhaustive retrieval. Every item is always returned.

## When to Use Collections

Use for tasks, TODOs, action items, backlogs, checklists -- anything where missing an item is a failure.

**Decision rule:** Will missing one item be a failure? Yes = collection. No = Memory (gramaton_capture).

## Operations

- `gramaton_collection_create`: Create with optional schema, `template` (one of `backlog`, `todo`, `reading-list`, `shopping-list`, `packing-list`), and behaviour fields (`curation`, `clear_mode`, `supersession`).
- `gramaton_collection_list`: List all collections.
- `gramaton_collection_items`: List ALL items (exhaustive). Pass `as_of=T` (RFC3339 or `YYYY-MM-DD`) for point-in-time membership at a historical commit; the response carries `semantics: point_in_time`.
- `gramaton_collection_add`: Add item (schema-validated). On collections with `curation: minimal`, a duplicate title returns the existing id with `deduplicated: true` (idempotent add). On any other curation profile, a duplicate returns `ErrConflict`.
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

## Behaviour fields

- `curation` (`standard` | `minimal`) — minimal disables LLM curation work on items in the collection. Use for shopping-list / packing-list shapes where classification adds no value. `collection_add` is also idempotent on minimal collections (duplicate title → returns existing id).
- `clear_mode` — defines how `clear` semantics work for transient lists (e.g. shopping list reset).
- `supersession` — how new items relate to older same-title items.

## Templates

Five starter templates seed schema + behaviour fields:

- `backlog` — engineering / product backlog with priority + status.
- `todo` — generic actions list with status + due-by.
- `reading-list` — articles / books with status + tags.
- `shopping-list` — minimal-curation list with bought/unbought items.
- `packing-list` — minimal-curation checklist with packed/unpacked items.

Pass `template=<name>` to `gramaton_collection_create`; you can still override or extend the resulting schema afterward.

## Cross-Store Linking

Collection items are graph nodes and can be linked to Memory records via `gramaton_link`.

## Related Topics

- `gramaton_guide(topic="capture")` — Memory vs Collections decision rule.
- `gramaton_guide(topic="temporal-queries")` — `as_of=T` semantics and the broader temporal-query surface.
