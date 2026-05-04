# Collections Guide

Collections are structured containers with guaranteed exhaustive retrieval. Every item is always returned.

## When to Use Collections

Use for tasks, TODOs, action items, backlogs, checklists -- anything where missing an item is a failure.

**Decision rule:** Will missing one item be a failure? Yes = collection. No = Memory (gramaton_capture).

## Operations

- `gramaton_collection_create`: Create with optional schema, `template` (one of `backlog`, `todo`, `reading-list`, `shopping-list`, `packing-list`, `journal`, `references`), and behaviour fields (`curation`, `supersession`, `contradictions`, `clear_mode`).
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

## Behaviour fields (the three-knob curation model)

Three orthogonal knobs control how curation treats records in a collection. Set them per-collection at create time, or rely on the template defaults.

- `curation` (`standard` | `none`) — LLM analysis intensity. `standard` runs classify, summarize, observation_extract, concept synthesis on items. `none` disables all LLM work; use for shopping/packing-list shapes where classification adds no value. On `curation=none`, `collection_add` is also idempotent (duplicate title returns the existing id).

- `supersession` (`collection` | `store` | `none`) — auto-supersession scope at ≥0.92 cosine. `collection` (default) limits candidates to same-collection records (the cross-collection-contamination fix). `store` is legacy global dedup, default for Memory orphan records. `none` opts out of auto-supersession entirely (use for journal / log shapes where similar entries on different days are signal, not duplicates).

- `contradictions` (`on` | `off`) — whether the system generates `contradicts` edges from records in this collection. `on` runs the LLM-driven contradiction stage. `off` skips it; useful for reference shapes (bookmarks, recipes, places) where two recommendations don't contradict each other.

- `clear_mode` (`resolve` | `unlink`) — how `clear` semantics work for transient lists.

### Multi-collection resolution

A record can belong to multiple collections (one record, multiple `member_of` edges). The effective values are resolved per-knob:

- `supersession` is destructive (sets `valid_until`). **Most-restrictive wins** (`none` > `collection` > `store`).
- `curation` is additive (adds metadata). **Most-permissive wins** (`standard` > `none`).
- `contradictions` is additive (creates edges). **Most-permissive wins** (`on` > `off`).

One-line principle: never irreversibly modify a record without unanimous agreement; always enrich when any collection wants enrichment.

`gramaton_inspect` returns the resolved values as `effective_curation: {curation, supersession, contradictions}` so you can see exactly what curation work will run on a given record.

## Templates

Seven starter templates seed schema + the three behaviour knobs:

| Template | curation | supersession | contradictions | Use case |
|---|---|---|---|---|
| `backlog` | standard | collection | on | Engineering/product backlog with priority + status. Surface design conflicts. |
| `todo` | standard | collection | on | Generic actions list with status + due-by. |
| `reading-list` | standard | collection | off | Articles/books with notes. Two recommendations aren't contradictions. |
| `shopping-list` | none | collection | off | Short-content list ("milk", "eggs"). Same-list dedup; no LLM work. |
| `packing-list` | none | collection | off | Trip checklist. Same shape as shopping-list. |
| `journal` | standard | none | off | Daily entries / observation logs. Append-only (no supersession); no contradiction-checking. |
| `references` | standard | collection | off | Bookmarks, recipes, places, contacts, snippets. Lookup-data shape. |

Pass `template=<name>` to `gramaton_collection_create`; caller-supplied fields override template defaults.

## Cross-Store Linking

Collection items are graph nodes and can be linked to Memory records via `gramaton_link`.

## Related Topics

- `gramaton_guide(topic="capture")` — Memory vs Collections decision rule.
- `gramaton_guide(topic="curation")` — stage-by-knob mapping for the curation pipeline.
- `gramaton_guide(topic="temporal-queries")` — `as_of=T` semantics and the broader temporal-query surface.
