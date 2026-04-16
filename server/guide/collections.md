# Collections Guide

Collections are structured containers with guaranteed exhaustive retrieval. Every item is always returned.

## When to Use Collections

Use for tasks, TODOs, action items, backlogs, checklists -- anything where missing an item is a failure.

**Decision rule:** Will missing one item be a failure? Yes = collection. No = Memory (gramaton_capture).

## Operations

- `gramaton_collection_create`: Create with optional schema.
- `gramaton_collection_items`: List ALL items (exhaustive).
- `gramaton_collection_add`: Add item (schema-validated).
- `gramaton_collection_update`: Update item fields.
- `gramaton_collection_move`: Move between collections.
- `gramaton_collection_remove`: Remove from collection.
- `gramaton_collection_list`: List all collections.

## Schemas

Collections can have optional schemas that enforce field types and required fields. Field types: string, number, boolean, date, enum, enum[].

## Cross-Store Linking

Collection items are graph nodes and can be linked to Memory records via `gramaton_link`.

## Related Topics

- See `gramaton_guide(topic="capture")` for when to use Memory vs Collections.
