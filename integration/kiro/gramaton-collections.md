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
| Passive capture (observe) | Explicit add only |

## Creating a Collection

Call `gramaton_collection_create`:
- `name`: unique within the store, max 128 chars
- `description`: optional
- `schema`: optional, defines required/optional fields with types

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

Fields are validated against the schema. Missing required fields are rejected.

If an item with the same title already exists, the response includes duplicate info -- decide whether to update or add.

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
