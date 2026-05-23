# Custom Agent Integration

How to integrate Gramaton with any agent framework.

## Access Methods

Gramaton offers three ways for agents to interact with the knowledge
store. Choose based on your framework's capabilities:

### 1. MCP (Recommended)

The fastest path. Gramaton exposes the full MCP tool surface
(currently 38 tools across 9 clusters) via Streamable HTTP. Agents
call typed tools with structured parameters -- no shell, no escaping,
no permission prompts.

**Setup:**
- Ensure the gramaton daemon is running (`gramaton serve`)
- Configure your MCP client to connect to `http://localhost:42982/mcp`
- For stdio-based MCP clients, use `gramaton mcp` as the command

**Available tools (grouped by cluster):**

*Records:*
| Tool | Description |
|------|-------------|
| `gramaton_save` | User-initiated save to Memory |
| `gramaton_inspect` | Get full record details (and one-hop edges) |
| `gramaton_update` | Update record metadata |
| `gramaton_classify` | Classify a pending record |
| `gramaton_resolve` | Mark a record as resolved (completed/abandoned/obsolete) |

*Search and ops:*
| Tool | Description |
|------|-------------|
| `gramaton_search` | Search the knowledge store (Memory + Sessions) |
| `gramaton_explore` | Graph traversal from a node |
| `gramaton_duplicates` | Find near-duplicate records |
| `gramaton_pending` | List unclassified records |
| `gramaton_stats` | Aggregate statistics |
| `gramaton_status` | Health and store metadata |

*Sessions (autonomous save):*
| Tool | Description |
|------|-------------|
| `gramaton_session_start` | Bind a working-directory session |
| `gramaton_session_get` | Look up a session by id |
| `gramaton_session_prepare` | Phase 1: receive extraction instructions |
| `gramaton_session_save` | Phase 2: submit extracted segments |

*Intake:*
| Tool | Description |
|------|-------------|
| `gramaton_intake` | Submit pre-extracted facts for deferred curation |

*Collections:*
| Tool | Description |
|------|-------------|
| `gramaton_collection_create` | Create a collection (optional schema, template, behaviour fields) |
| `gramaton_collection_list` | List all collections |
| `gramaton_collection_items` | List ALL items (exhaustive); supports `as_of=T` for point-in-time |
| `gramaton_collection_add` | Add an item (idempotent on `curation: minimal`) |
| `gramaton_collection_add_batch` | Add up to 500 items in one call |
| `gramaton_collection_update` | Update item fields |
| `gramaton_collection_move` | Move between collections |
| `gramaton_collection_remove` | Remove from collection |
| `gramaton_collection_rename` | Rename collection |
| `gramaton_collection_delete` | Retire/unretire |
| `gramaton_collection_schema` | Read schema |
| `gramaton_collection_migrate` | Bulk-update items for schema migration |

*Linking:*
| Tool | Description |
|------|-------------|
| `gramaton_link` | Create a typed edge between records |
| `gramaton_unlink` | Remove an edge |

*History (temporal queries):*
| Tool | Description |
|------|-------------|
| `gramaton_log` | Commit history (filters: actions, exclude_curation, mutations) |
| `gramaton_diff` | What changed between two dates / commits |
| `gramaton_history` | Per-record change history |

*Maintenance:*
| Tool | Description |
|------|-------------|
| `gramaton_curation` | Curation status, trigger, dry-run, or batch |
| `gramaton_reembed` | Regenerate stale embeddings |

*Admin:*
| Tool | Description |
|------|-------------|
| `gramaton_branch` | Branch management (list/create/checkout/merge/discard) |
| `gramaton_backup` | Create a backup or check status |

*Guide:*
| Tool | Description |
|------|-------------|
| `gramaton_guide` | Live reference for any topic (save, search, sessions, collections, metadata, curation, temporal-queries) |

### 2. REST API

Direct HTTP calls to the Gramaton server. Best for frameworks that
can make HTTP requests but don't support MCP.

**Base URL:** `http://localhost:42982`

**Key endpoints:**
```
# Records
POST   /v1/records                          Create a record
GET    /v1/records/{id}                     Get a record
PATCH  /v1/records/{id}                     Update properties
DELETE /v1/records/{id}                     Soft delete
POST   /v1/records/{id}/edges               Create an edge
DELETE /v1/edges/{edge_id}                  Remove an edge
POST   /v1/records/{id}/classify            Classify a record
POST   /v1/records/{id}/resolve             Mark as resolved
GET    /v1/records/{id}/history             Per-record history

# Search and ops
POST   /v1/search                           Search the store
POST   /v1/explore                          Graph traversal
POST   /v1/duplicates                       Find near-duplicates
GET    /v1/pending                          List unclassified records
GET    /v1/stats                            Aggregate statistics
GET    /v1/status                           Health and store metadata
GET    /v1/health                           Lock-free liveness probe
GET    /v1/stats/llm                        LLM usage & cost stats

# Sessions
POST   /v1/sessions                         Start a session
GET    /v1/sessions/{id}                    Look up a session
POST   /v1/sessions/{id}/prepare            Phase 1 of save flow
POST   /v1/sessions/{id}/save             Phase 2 of save flow
POST   /v1/sessions/{id}/archive            Archive a session

# Intake (replaces the retired /v1/observe)
POST   /v1/intake                           Submit pre-extracted facts
POST   /v1/ingest                           Bulk file ingestion (loopback only)

# History (temporal queries)
GET    /v1/log                              Commit history
GET    /v1/diff                             Compare commits / dates

# Branches
GET    /v1/branches                         List branches
POST   /v1/branches                         Create a branch
POST   /v1/branches/{name}/checkout         Switch branch
POST   /v1/branches/{name}/merge            Merge into current
DELETE /v1/branches/{name}                  Discard a branch
POST   /v1/revert                           Revert to a prior commit (loopback only)

# Curation and maintenance
GET    /v1/curation                         Curation status and candidates
POST   /v1/curation/trigger                 Trigger a curation cycle
POST   /v1/curation/batch                   Batch-classify pending
POST   /v1/curation/drain                   Drain queued curation work
POST   /v1/reembed                          Regenerate stale embeddings (loopback only)

# Collections
POST   /v1/collections                                  Create a collection
GET    /v1/collections                                  List all collections
GET    /v1/collections/{id}/items                       List items (exhaustive; ?as_of=T for historical)
POST   /v1/collections/{id}/items                       Add an item
POST   /v1/collections/{id}/items/batch                 Add up to 500 items
PATCH  /v1/collections/{id}/items/{item_id}             Update item fields
POST   /v1/collections/{id}/items/{item_id}/move        Move item
DELETE /v1/collections/{id}/items/{item_id}             Remove item
PATCH  /v1/collections/{id}                             Rename collection
DELETE /v1/collections/{id}                             Retire/unretire
GET    /v1/collections/{id}/schema                      Read schema
PUT    /v1/collections/{id}/schema                      Update schema
POST   /v1/collections/{id}/migrate                     Bulk migrate items

# Backup / export / import (loopback only)
GET    /v1/backup                           List existing backups
POST   /v1/backup                           Create a backup
POST   /v1/restore                          Restore from a backup
POST   /v1/export                           Export the store to a tarball
POST   /v1/import                           Import a tarball
POST   /v1/shutdown                         Request server shutdown
```

Routes marked "loopback only" return 403 to non-loopback callers.

All responses use a standard envelope:
```json
{
  "data": { ... },
  "curation": {"pending_count": 5, "overdue": true},
  "meta": {"version": "0.2.0"}
}
```

### 3. CLI

Shell commands for frameworks with shell access. The CLI auto-starts
the server daemon on first use.

```bash
gramaton search "<query>" [flags]
gramaton inspect <record-id>
gramaton explore <record-id> [flags]
gramaton save -f <json-file>
gramaton classify -f <json-file>
gramaton update -f <json-file>
gramaton pending
gramaton status
```

The CLI returns JSON to stdout. Write commands accept JSON via
`--file` flag (preferred) or stdin.

## Agent Behavior Guidelines

Regardless of which access method you use, your agent's system
prompt should include these behavioral instructions:

### When to Search

- Before answering questions about past decisions or context
- When the user references prior sessions
- When you need context beyond the current conversation
- When uncertain whether the user expressed a preference before

### When to Save

- User makes a decision or states a preference
- A significant fact, insight, or design rationale emerges
- An architecture or design choice is made with reasoning
- Research findings or domain knowledge are discussed
- A constraint, requirement, or tradeoff is identified

### When NOT to Save

- Trivial exchanges, greetings, small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Your own generated responses or analysis

### Save Workflow

The standard save workflow is three operations:

1. **Save** the knowledge with classification metadata
2. **Search** for related existing records
3. **Link** the new record to related ones

This creates a connected knowledge graph, not just a list of facts.

### When to Extract from Conversation (Sessions)

At natural breakpoints (end of task, topic change, session wind-down,
before context compaction), call the two-phase session flow:

```
gramaton_session_prepare(session_id="<id>")
gramaton_session_save(session_id="<id>", segments=[...])
```

`prepare` returns extraction instructions plus already-saved
segments (for dedup). `commit` submits extracted segments. Each
segment becomes a Session record (BM25-indexed); when
`promote_to_memory: true` (default) it also becomes a Memory record
(vector-embedded, full lifecycle, auto-supersession).

Set `promote_to_memory: false` for exploration, open questions, or
dead ends — they stay searchable in Sessions without polluting
Memory's vector space.

Do not call `commit` without calling `prepare` first; the server
rejects orphan commits.

For pre-extracted facts (no LLM available, or external pipeline),
use `gramaton_intake` instead — it bypasses the session flow and
queues facts directly for curation:

```
gramaton_intake(facts=["Decided to use JWT", "API v2 replaces v1"])
```

Both paths are fire-and-forget: do not announce, do not call every
turn. They are safety nets for knowledge the agent didn't explicitly
save. Explicit `gramaton_save` remains the primary,
high-quality, user-initiated save path.

### Interpreting Metadata

Results include `metadata_summary` -- a one-line LLM-readable trust
assessment. Example: "Current. Durable, high-confidence (0.90),
well-established. Last accessed 3 days ago."

Use this for quick assessment. For critical decisions, check the
raw fields (confidence, temporality, epistemic_status) directly.

### Curation

Responses include a `curation` field:
```json
{"pending_count": 14, "overdue": true, "autonomous": false}
```

When `autonomous: true`, the server handles classification, summary
generation, contradiction detection, and store maintenance
automatically. Do not duplicate its work.

When `overdue: true` AND `autonomous: false`, spawn background
curation to classify pending records. This keeps the store healthy
without user intervention.

Preview curation changes before applying:
```
gramaton_curation(action="dry_run")
```

## Output Format

All operations return structured JSON. Search results include:

```json
{
  "id": "01H5K9E2GJ...",
  "keywords": ["kafka", "rabbitmq"],
  "summary_short": "Chose Kafka for event pipeline",
  "metadata_summary": "Current. Durable, confidence 0.90, well-established",
  "confidence": 0.9,
  "temporality": "durable",
  "effective_score": 0.78,
  "created_at": "2026-03-15T10:30:00Z",
  "access_count": 5,
  "edge_count": 3,
  "staleness": 0.12,
  "content_length": 256
}
```

Search responses also include faceted counts:
```json
{
  "results": [...],
  "facets": {
    "temporality": {"durable": 8, "temporal": 3},
    "knowledge_type": {"episodic": 6, "semantic": 5},
    "epistemic_status": {"well_established": 9, "probable": 2}
  }
}
```

## Search Patterns

Text is optional -- omit it for filter-only queries. Useful patterns:

| Pattern | Query |
|---------|-------|
| Newest records | `sort="created_at", top=10` |
| Unclassified | `missing=["temporality"]` |
| By tag | `keywords=["auth"]` |
| Most stale | `sort="staleness", order="desc"` |
| Orphans (no edges) | `max_edges=0` |
| Literal text match | `match="RWMutex"` |
| Similar to a record | `similar_to="<id>"` |
| Random sample | `random=true, top=3` |
| Exclude refuted | `epistemic_status="!refuted"` |
| Most accessed | `sort="access_count", order="desc"` |
| Expiring soon | `expires_before="2026-04-15"` |
| Never accessed | `access_count_max=0` |
