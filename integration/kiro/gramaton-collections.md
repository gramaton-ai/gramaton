# gramaton-collections

Manage structured collections in Gramaton for tasks, lists, and backlogs.

## When to Use

- When the user needs to track items exhaustively (tasks, reading lists, checklists)
- When missing an item would be a failure (unlike knowledge search, which tolerates misses)
- When items need structured fields with validation (status, priority, due dates)
- NOT for fuzzy knowledge retrieval -- use `gramaton_search` for that

## Collections vs. Knowledge Graph

| | Knowledge Graph | Collections |
|---|---|---|
| Use `gramaton_search` | Use `gramaton_collection_items` |
| Ranked results (best N) | ALL items, guaranteed |
| Fuzzy, semantic | Structured, enforced |
| Passive save via sessions | Explicit add only |

## Creating a Collection

Call `gramaton_collection_create`:
- `name`: unique within the store, max 128 chars
- `description`: optional
- `template`: optional shortcut -- one of `backlog`, `todo`,
  `reading-list`, `shopping-list`, `packing-list`, `journal`,
  `references`. Seeds schema and behavior fields for the chosen
  shape.
- `schema`: optional, defines required/optional fields with types

### Behavior knobs (set at create time)

Four orthogonal flags on the collection node control how items
flow through the rest of the system:

- `curation`: `none` (default for ad-hoc collections; items skip
  the LLM pipeline) or `standard` (items get classified,
  summarized, observation-extracted, concept-synthesized;
  requires `content_fields` declared in the schema).
- `supersession`: `off`, `on` (auto-supersede near-duplicates
  within this collection at ≥0.92 cosine), or `store`
  (auto-supersede against the whole store).
- `contradictions`: `off` or `on` (gate this collection's items
  into the LLM contradiction-detection pipeline).
- `clear_mode`: `resolve` (default; `gramaton_resolve` flips item
  status to a closed value) or `unlink` (resolve removes the item
  from the collection instead of stamping closure metadata; useful
  for transient lists).

The standard templates pick sensible defaults: `backlog`, `todo`,
`reading-list`, `journal`, and `references` set `curation:
standard`; `shopping-list` and `packing-list` set `curation:
none`. Override per-create as needed.

### Schema field types

| Type | Values | Example |
|------|--------|---------|
| `string` | any text | title, notes |
| `number` | numeric | estimate, position |
| `boolean` | true/false | blocked |
| `date` | YYYY-MM-DD or RFC3339 | due_date |
| `enum` | one of predefined values | status: [open, done] |
| `enum[]` | multiple from predefined values | labels: [bug, security] |

### Example: Sprint backlog

```json
{
  "name": "Sprint 23",
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

## Adding Items

Call `gramaton_collection_add` with `collection_id` and `fields`.

For more than ~10 items at once, use `gramaton_collection_add_batch`
(up to 500 items per call). Sequential `_add` calls are
disproportionately slow.

Fields are validated against the schema. Missing required fields are rejected.

### Duplicate Titles

Behavior depends on the collection's `curation` mode:

- `none` (default for ad-hoc / shopping-list / packing-list):
  duplicate returns the existing item id with `deduplicated: true`
  -- idempotent, no error.
- `standard` (templates like `backlog`, `todo`, `journal`):
  duplicate returns `ErrConflict` with the existing item's id;
  decide whether to update via `_update`, add under a different
  title, or skip.

## Listing Items

Call `gramaton_collection_items` with `collection_id`.

Returns ALL items. No ranking, no cutoff. Sort by any field with `sort` and `order`.

## Updating Items

Call `gramaton_collection_update` with `collection_id`, `item_id`, and `fields`.

Only specified fields change. Existing fields are preserved. Validated against schema.

## Moving Items

Call `gramaton_collection_move` with `collection_id`, `item_id`, and `target_collection_id`.

Item is removed from source and added to target. Target schema is validated.

## Retiring Collections

Call `gramaton_collection_delete`. This retires the collection (reversible). Items and edges are preserved. Call again to unretire.

## Common Patterns

### Kanban board
Three collections: "Todo", "Doing", "Done". Move items between them.

### Reading list
One collection with status field: `enum: [unread, reading, read]`.

### PARA
- Projects: time-bound collections
- Areas: ongoing collections (no end date)
- Resources: reference collections
- Archive: retired collections
