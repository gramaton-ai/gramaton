# Custom Agent Integration

How to integrate Gramaton with any agent framework — code calling a
model API directly (Anthropic, Bedrock, or any provider) in Python,
Go, TypeScript, or anything else, with no harness wizard to lean on.

Integration has three parts:

1. **Connect** your agent to Gramaton (MCP, REST, or CLI — below).
2. **Steer** the model with the canonical agent guidance
   ([System Prompt](#system-prompt)).
3. **Wire the session lifecycle** into your agent loop — the part
   the harness hooks normally do for you
   ([Session Lifecycle Wiring](#session-lifecycle-wiring)).

## System Prompt

Merge [`integration/custom-agents/system-prompt.md`](../custom-agents/system-prompt.md)
into your agent's system prompt. It is the same canonical guidance
`gramaton init` installs for Claude Code, Codex, and Cursor —
rendered harness-neutral from the templates in
`internal/setup/templates/guidance/` and pinned by a drift test, so
it never lags the tool surface. It carries the retrieval triggers,
save rules, metadata interpretation, and the session flow; don't
hand-write your own version of that material.

## Session Lifecycle Wiring

On the supported harnesses, `gramaton init` installs lifecycle hooks
that bind sessions automatically. A custom agent has no hooks — your
agent loop owns this:

1. **At conversation open**, call `gramaton_session_start` (or
   `POST /v1/sessions`) with a stable `client_session_id` — your
   framework's conversation/thread id. Persist the returned Gramaton
   `session_id` alongside the conversation state.
2. **Pass that `session_id`** to `gramaton_session_prepare` /
   `gramaton_session_save` at boundaries: a task completes, the user
   pivots topics, ~10 substantive turns pass, or you are about to
   compact/truncate context. Always prepare before save.
3. **Before compaction**, optionally archive the raw transcript via
   `POST /v1/sessions/{id}/archive` so post-compaction extraction
   still has source material.

Note that `gramaton session current` (the CLI lookup the system
prompt mentions) reads per-working-directory state written by the
harness hooks. Without hooks it has nothing to find — use the
`session_id` your loop persisted instead. The per-cwd binding is
also last-writer-wins: two hooked agents sharing one working
directory contend for it, which is another reason a custom agent
should carry its own `session_id` explicitly.

## Access Methods

Gramaton offers three ways for agents to interact with the knowledge
store. Choose based on your framework's capabilities:

### 1. MCP (Recommended)

The fastest path. Gramaton exposes the full MCP tool surface
(currently 38 tools across 10 clusters) via Streamable HTTP. Agents
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
POST   /v1/sessions/{id}/save               Phase 2 of save flow
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

## Custom-Agent-Specific Notes

The behavioral guidance — retrieval triggers, save rules, session
extraction, metadata interpretation — lives in the
[system-prompt artifact](#system-prompt); it is deliberately not
duplicated here. The notes below cover only what differs for custom
builds.

### Intake (Pre-Extracted Facts)

For pre-extracted facts (no LLM available in your loop, or an
external pipeline feeding Gramaton), use `gramaton_intake` — it
bypasses the session flow and queues facts directly for curation:

```
gramaton_intake(facts=["Decided to use JWT", "API v2 replaces v1"])
```

Intake and session extraction are both fire-and-forget safety nets:
do not announce them, do not call them every turn. Explicit
`gramaton_save` remains the primary, high-quality, user-initiated
save path.

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
