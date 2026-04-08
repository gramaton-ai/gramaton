# Architecture

This document describes Gramaton's internal architecture for developers working on the codebase.

## System Overview

Gramaton is a single Go binary that runs as an on-demand daemon. The CLI auto-starts the server on first use. The server manages the graph, indexes, and embeddings. All state lives on the local filesystem.

```
                  ┌──────────────┐
                  │  CLI / MCP   │   protocol layer
                  └──────┬───────┘
                         │ HTTP
                  ┌──────┴───────┐
                  │    Server    │   request handling, locking, curation
                  └──────┬───────┘
                         │
                  ┌──────┴───────┐
                  │    Engine    │   graph operations, embedding, search
                  └──────┬───────┘
                    ┌────┼────┐
               ┌────┴┐ ┌┴───┐ ┌┴────┐
               │Graph│ │Idx │ │Store│  data layer
               └─────┘ └────┘ └─────┘
```

## Package Map

### Protocol Layer

| Package | Purpose |
|---------|---------|
| `cli/` | Cobra commands. Each file is one command. Talks to server via HTTP. |
| `cli/mcp_proxy.go` | MCP tool registration. Each tool proxies to an HTTP endpoint. |
| `server/` | HTTP handlers, MCP handler, service methods, curation runner. |

### Engine Layer

| Package | Purpose |
|---------|---------|
| `core/` | `Engine` -- the central coordinator. Holds the graph, indexes, and embedder. Manages locking. |
| `search/` | `Tool` -- search, scoring, dedup, graph traversal. Pure computation, no I/O. |
| `curation/` | Deterministic and autonomous curation. Runs on a timer inside the server. |
| `embed/` | `Provider` interface + factory. Implementations in `embed/ollama/`, `embed/openai/`, `embed/bedrock/`. |
| `llm/` | `Provider` interface + factory. Implementations in `llm/anthropic/`, `llm/openai/`, `llm/bedrock/`. |

### Data Layer

| Package | Purpose |
|---------|---------|
| `graph/` | Property graph: nodes, edges, properties. Pure in-memory data structure. |
| `index/` | `PropertyIndex` (exact + range lookups), `FlatIndex` (brute-force vector search), `BM25Index` (keyword search). |
| `storage/` | Prolly tree -- content-addressed, append-only persistence. Serialization, chunking, commit history. |
| `store/` | Named store management. Resolves store paths, validates names. |

### Support

| Package | Purpose |
|---------|---------|
| `config/` | Config types, defaults, YAML loading. |
| `logging/` | Rotating file logger with size budgets. |
| `backup/` | Tar archive backup/restore, export/import. |
| `internal/awscfg/` | Shared AWS credential loading for Bedrock providers. |
| `testutil/` | Test helpers, builders, fake providers. |

## Dependency Direction

Dependencies flow inward. Outer layers depend on inner layers, never the reverse.

```
cli/ server/          → core/ search/ curation/
core/ search/         → graph/ index/ embed/ llm/
graph/ index/         → (standard library only)
storage/              → graph/ (for serialization)
embed/*/ llm/*/       → config/ (for provider config)
```

The `core.Engine` is the composition root. It wires together the graph, indexes, embedder, and LLM provider. The server wraps the engine and adds HTTP/MCP handling, locking discipline, and curation scheduling.

## Data Flow

### Capture

```
Client → POST /v1/records → parseJSON → serviceCapture
  1. Pre-embed content (outside lock, if embedder configured)
  2. Lock
  3. Create node with properties
  4. Add to vector index, property index, BM25 index
  5. Check for duplicates (cosine + Jaccard guard)
  6. If duplicate: create supersedes edge, set valid_until on old record
  7. If content is long: chunk into child nodes with part_of edges
  8. Commit to storage
  9. Unlock
```

### Search

```
Client → POST /v1/search → parseJSON → serviceSearch
  1. Pre-embed query text (outside lock)
  2. RLock
  3. Filter candidates by metadata (property index)
  4. Score candidates:
     a. Vector similarity (flat index, cosine)
     b. BM25 keyword score
     c. RRF fusion of vector + BM25 ranks
     d. Freshness (time decay by temporality)
     e. ACT-R activation (access count + recency)
     f. Confidence (from record metadata)
     g. Composite = weighted sum of similarity, freshness, activation, confidence
  5. Sort by composite score, take top N
  6. Record access (activation bump)
  7. RUnlock
  8. Return results with metadata summaries
```

### Curation Cycle

Runs every 5 minutes (configurable).

**Deterministic (always):**
- Expire stale ephemeral/temporal records (set valid_until)
- Link orphan nodes to similar neighbors
- Detect and flag duplicates
- Rebuild store manifest (type counts, keyword distribution)

**Autonomous (when LLM configured):**
- Classify pending records (temporality, confidence, knowledge_type, etc.)
- Generate missing summaries
- Detect contradictions between similar records
- Synthesize concept nodes from recurring keywords

## Concurrency Model

The engine uses a single `sync.RWMutex`. The locking discipline is:

- **Service methods** acquire and release locks internally.
- **HTTP handlers and MCP tools** do not hold locks -- they call service methods.
- **Embedding calls** happen outside the lock (pre-embed pattern).

The pre-embed pattern avoids holding the lock during network I/O:
1. RLock to read content
2. Unlock
3. Embed content (network call, may be slow)
4. Lock to write results

Curation holds its own mutex for status tracking, independent of the engine lock.

## Storage Model

Gramaton uses a prolly tree (probabilistic B-tree) for persistence. Key properties:

- **Content-addressed.** Every chunk is identified by its hash. Identical subtrees share storage.
- **Append-only.** Mutations create new root nodes. Old roots are retained as commit history.
- **Deterministic splits.** Chunk boundaries are determined by hashing, so independent mutations to the same tree produce structurally identical results (enabling clean merges).

The storage layer is below the graph layer. The graph is fully materialized in memory on startup and flushed to the prolly tree on commit. This keeps search fast (no disk I/O during queries) at the cost of memory proportional to store size.

## Adding a New Provider

### Embedding Provider

1. Create `embed/<name>/<name>.go` implementing `embed.Provider`:
   ```go
   type Provider interface {
       Embed(ctx context.Context, texts []string) ([][]float32, error)
       ModelID() string
   }
   ```
2. Add the case to `embed.New()` in `embed/embed.go`.
3. Add tests in `embed/<name>/<name>_test.go`.

### LLM Provider

1. Create `llm/<name>/<name>.go` implementing `llm.Provider`:
   ```go
   type Provider interface {
       Complete(ctx context.Context, prompt string) (string, error)
       ModelID() string
   }
   ```
2. Add the case to `llm.New()` in `llm/llm.go`.
3. Add tests in `llm/<name>/<name>_test.go`.

Both interfaces are intentionally minimal. The provider handles auth, retries, and serialization internally. The engine and server never import provider packages directly -- they use the interface.
