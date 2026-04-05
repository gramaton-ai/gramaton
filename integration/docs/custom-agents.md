# Custom Agent Integration

How to integrate Gramaton with any agent framework.

## Access Methods

Gramaton offers three ways for agents to interact with the knowledge
store. Choose based on your framework's capabilities:

### 1. MCP (Recommended)

The fastest path. Gramaton exposes 15 MCP tools via Streamable HTTP.
Agents call typed tools with structured parameters -- no shell, no
escaping, no permission prompts.

**Setup:**
- Ensure the gramaton daemon is running (`gramaton serve`)
- Configure your MCP client to connect to `http://localhost:42982/mcp`
- For stdio-based MCP clients, use `gramaton mcp` as the command

**Available tools:**
| Tool | Description |
|------|-------------|
| `gramaton_search` | Search the knowledge store |
| `gramaton_capture` | Store a knowledge record |
| `gramaton_inspect` | Get full record details |
| `gramaton_update` | Update record properties |
| `gramaton_link` | Create an edge between records |
| `gramaton_classify` | Classify a pending record |
| `gramaton_explore` | Graph traversal |
| `gramaton_pending` | List unclassified records |
| `gramaton_status` | Health and stats |
| `gramaton_branch` | Branch management |
| `gramaton_diff` | What changed since a date |
| `gramaton_log` | Commit and record history |
| `gramaton_reembed` | Regenerate stale embeddings |
| `gramaton_stats` | Aggregate statistics |
| `gramaton_duplicates` | Find near-duplicate records |

### 2. REST API

Direct HTTP calls to the Gramaton server. Best for frameworks that
can make HTTP requests but don't support MCP.

**Base URL:** `http://localhost:42982`

**Key endpoints:**
```
POST   /v1/records              Create a record
GET    /v1/records/{id}         Get a record
PATCH  /v1/records/{id}         Update properties
DELETE /v1/records/{id}         Soft delete
POST   /v1/records/{id}/edges   Create an edge
POST   /v1/records/{id}/classify  Classify a record
POST   /v1/search               Search the store
POST   /v1/explore              Graph traversal
GET    /v1/pending              List unclassified records
GET    /v1/branches             List branches
POST   /v1/branches             Create a branch
GET    /v1/log                  Commit history
GET    /v1/diff                 Compare commits
GET    /v1/status               Health and stats
GET    /v1/stats                Aggregate statistics
POST   /v1/duplicates           Find near-duplicates
```

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
gramaton capture -f <json-file>
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

### When to Capture

- User makes a decision or states a preference
- A significant fact, insight, or design rationale emerges
- An architecture or design choice is made with reasoning
- Research findings or domain knowledge are discussed
- A constraint, requirement, or tradeoff is identified

### When NOT to Capture

- Trivial exchanges, greetings, small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Your own generated responses or analysis

### Capture Workflow

The standard capture workflow is three operations:

1. **Capture** the knowledge with classification metadata
2. **Search** for related existing records
3. **Link** the new record to related ones

This creates a connected knowledge graph, not just a list of facts.

### Interpreting Metadata

Results include `metadata_summary` -- a one-line LLM-readable trust
assessment. Example: "Current. Durable, high-confidence (0.90),
well-established. Last accessed 3 days ago."

Use this for quick assessment. For critical decisions, check the
raw fields (confidence, temporality, epistemic_status) directly.

### Curation

Responses include a `curation` field:
```json
{"pending_count": 14, "overdue": true}
```

When `overdue` is true, spawn background curation to classify
pending records. This keeps the store healthy without user
intervention.

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
