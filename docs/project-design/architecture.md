# Architecture

## Two-Part System

Gramaton is two things that work together:

### Part 1: Gramaton Service

A Go server compiled to a single binary. Owns the data. Handles everything that doesn't need a powerful LLM.

**Responsibilities:**
- Store and index nodes, edges, and properties
- Delegate embedding generation to a configured provider (Ollama, OpenAI-compatible API, or AWS Bedrock)
- Serve queries: search, inspect, explore, traverse
- Deduplication detection via vector similarity
- Relationship detection via vector similarity
- Access tracking and spreading activation
- Deterministic curation (decay, staleness, expiry, stats)
- Maintain a processing queue of records awaiting LLM enrichment

**Does NOT:**
- Call any LLM API (embedding providers are not LLMs — they're smaller, specialized models)
- Bundle or run ML models directly
- Know what "temporality" or "confidence" mean semantically — it stores and indexes them, the agent assigns them

### Part 2: Agent Integration Kit

Prompt patterns and tool definitions that give the user's existing LLM agent the ability and agency to interact with Gramaton.

**Responsibilities:**
- Classify knowledge (temporality, confidence, knowledge type)
- Generate summary pyramids (keywords, short summary, abstract)
- Decompose complex content into constituent records
- Extract entities and identify relationships
- Package the context envelope (project, team, tickets — implicit context)
- Decide when to search and when to capture autonomously
- Run LLM-requiring curation tasks (contradiction detection, concept merging)

**Delivered as:**
- System prompt / `CLAUDE.md` instructions for transparent retrieval and capture
- Subagent prompt templates for async capture and classification
- Skill definitions for explicit commands (`/gramaton-process`, `/gramaton-curate`)
- MCP tool definitions wrapping the CLI
- Documentation for custom agent integration

## System Diagram

```
┌─────────────────────────────────────────────────────────────┐
│  Agent Session (Claude Code, Kiro CLI, custom agents)       │
│                                                             │
│  TRANSPARENT RETRIEVAL (synchronous, inline)                │
│  Agent decides to search → gramaton search → uses results   │
│                                                             │
│  TRANSPARENT CAPTURE (async, via subagent)                  │
│  Agent decides to store → spawns subagent → continues       │
│                                                             │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Subagent (separate context, discarded when done)      │ │
│  │                                                        │ │
│  │  1. Receives content + context envelope from main      │ │
│  │  2. Classifies metadata                                │ │
│  │  3. Extracts entities from content AND context         │ │
│  │  4. Searches Gramaton for related existing nodes       │ │
│  │  5. Calls gramaton capture with full metadata          │ │
│  │  6. Creates edges to existing related nodes            │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  EXPLICIT COMMANDS (when user wants direct control)         │
│  /gramaton-process   → classify pending records             │
│  /gramaton-curate    → run LLM-requiring maintenance        │
│  /gramaton-search    → manual search                        │
└──────────────────────────┬──────────────────────────────────┘
                           │
                    CLI / HTTP / MCP
                           │
┌──────────────────────────▼──────────────────────────────────┐
│  Gramaton Service (single Go binary)                        │
│                                                             │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Protocol Layer                                        │ │
│  │  CLI parser, HTTP handlers, MCP server                 │ │
│  │  Input validation, response serialization              │ │
│  └──────────────────────────┬─────────────────────────────┘ │
│                             │                               │
│  ┌──────────────────────────▼─────────────────────────────┐ │
│  │  Tool Layer                                            │ │
│  │  search, inspect, explore, raw, capture, classify,     │ │
│  │  update, pending, status, diff, log, branch, revert    │ │
│  │                                                        │ │
│  │  Implements retrieval funnel enforcement               │ │
│  │  Computes decay scoring at query time                  │ │
│  │  Triggers spreading activation on access               │ │
│  └──────────────────────────┬─────────────────────────────┘ │
│                             │                               │
│  ┌──────────────────────────▼─────────────────────────────┐ │
│  │  Graph Engine                                          │ │
│  │  Nodes with typed properties                           │ │
│  │  Edges with types, weights, properties                 │ │
│  │  Property indexes (exact, range, substring)            │ │
│  │  Vector indexes (HNSW, flat scan)                      │ │
│  │  filter → rank → traverse as primitive operation       │ │
│  │  Access counter increment (mechanical)                 │ │
│  │  Versioning: commits, branches, diff, merge            │ │
│  └──────────────────────────┬─────────────────────────────┘ │
│                             │                               │
│  ┌──────────────────────────▼─────────────────────────────┐ │
│  │  Storage Layer                                         │ │
│  │  Content-addressed chunks (directory on disk)          │ │
│  │  Commits: flat hash lists (v0.1), prolly trees (if     │ │
│  │    scale demands — swap behind same interface)         │ │
│  │  All access through StorageBackend interface           │ │
│  │  No component leaks assumptions about on-disk format   │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  ┌───────────────────┐  ┌──────────────────────────────────┐│
│  │  Embedding Client │  │  Source Store                    ││
│  │  Ollama (default) │  │  Raw files on filesystem         ││
│  │  OpenAI-compat    │  │  Content-addressed               ││
│  │  AWS Bedrock      │  │  Referenced by source_ref prop   ││
│  └───────────────────┘  └──────────────────────────────────┘│
│                                                             │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Deterministic Curation (scheduled, no LLM)            │ │
│  │  Decay scoring updates, lifecycle transitions,         │ │
│  │  access statistics, manifest rebuild                   │ │
│  └────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## Package Structure

No `internal/`, no `pkg/` — single binary with no external importers, these add pointless directory layers. Flat, domain-named packages at root level. Tools are separate packages (not one `tool/` god-package). Interfaces defined at consumers, not providers (Go idiom: "accept interfaces, return structs").

```
gramaton/
├── main.go                # Composition root — wires everything together
├── cli/                   # Cobra command definitions, thin delegation
│   ├── root.go
│   ├── search.go
│   ├── capture.go
│   ├── serve.go
│   └── ...
├── server/                # HTTP + MCP handlers, routing, auto-start, PID mgmt
├── graph/                 # Property graph engine
│   ├── graph.go           # Engine, constructor, dependency injection
│   ├── node.go            # Node CRUD
│   ├── edge.go            # Edge CRUD, bidirectional indexes
│   ├── traverse.go        # Graph traversal
│   ├── commit.go          # Commits, branches, diff, merge
│   └── query.go           # filter → rank → traverse composition
├── index/                 # Indexing
│   ├── property.go        # Property index (exact, range, substring)
│   ├── hnsw.go            # HNSW vector index
│   └── flat.go            # Flat scan vector index
├── storage/               # Content-addressed storage
│   ├── store.go           # Implementation
│   └── cas.go             # Content hashing, chunk I/O
├── embed/                 # Embedding providers (shared interface — multiple impls)
│   ├── embed.go           # EmbeddingProvider interface + factory
│   ├── ollama/            # Ollama client
│   ├── openai/            # OpenAI-compatible client
│   └── bedrock/           # Bedrock client
├── search/                # Search tool (Tier 1 + scoring + decay)
├── capture/               # Capture tool (store + chunk + embed)
├── inspect/               # Inspect tool (Tier 2 + evidence_health)
├── explore/               # Explore tool (Tier 3 + graph traversal)
├── update/                # Update tool (classify, link, modify)
├── ingest/                # File ingestion (bulk commit, batch embed)
├── versioning/            # Diff, log, branch, revert, export, import
├── curate/                # Deterministic curation (lifecycle, stats, manifest)
├── config/                # Configuration loading + defaults
└── integration/           # Agent integration kit (not Go code — prompts and docs)
    ├── claude-code/       # CLAUDE.md template, subagent prompts
    ├── kiro/              # Skill definitions
    └── docs/              # Custom agent integration guide
```

### Key Design Principles

**Dependency direction flows inward.** The composition root (`main.go`) wires everything. CLI delegates to server. Server delegates to tools. Tools depend on graph, index, storage, embed. Graph depends on storage. Storage depends on nothing.

```
main.go → cli/ → server/ → search/, capture/, ... → graph/, index/, embed/ → storage/
```

No cycles. Every arrow points inward.

**Interfaces at consumers, not providers.** `graph/` defines an unexported `store` interface with just the methods it needs from storage. `search/` defines an unexported `graphQuerier` interface for what it needs from the graph engine. This means:
- Each package imports only what it uses
- Test fakes are local to the test file (no mock packages)
- Packages are independently testable

**Exception: `embed/`** defines a shared `EmbeddingProvider` interface because it has three implementations. This is the correct Go reason to define an interface at the provider.

**Types live with their operations.** `graph.Node`, `graph.Edge` — not `models.Node`. No `models/`, `types/`, or `utils/` packages.

**Composition root (`main.go`):**

```go
func main() {
    cfg := config.Load()
    store := storage.New(cfg.DataDir)
    g := graph.New(store)
    idx := index.New(store)
    emb := embed.New(cfg.Embedding)

    searchTool := search.New(g, idx, emb)
    captureTool := capture.New(g, store, emb)
    inspectTool := inspect.New(g)
    exploreTool := explore.New(g)
    updateTool := update.New(g, idx)
    // ...

    srv := server.New(searchTool, captureTool, inspectTool, ...)
    cli.Run(srv, cfg)
}
```

**Evolution path:** Start here. Split packages when they grow past ~2000 lines with distinct concerns (e.g., `graph/commit/` becomes its own package). Merge packages if two are always imported together. Don't pre-split — let the code tell you.

## Concurrency Model

Gramaton runs as a server process. Multiple agents connect simultaneously.

- **Reads are concurrent.** Multiple agents can search and inspect at the same time.
- **Writes are serialized.** The server handles write ordering internally. Agents don't need to coordinate.
- **Spreading activation on read.** When a node is returned to a consumer, its access_count increments and neighbors get an activation boost. This is a write triggered by a read — handled by the server's write serialization.
- **Branching for curation.** Curation tasks operate on a branch. Branch → run curation → review diff → merge or discard. Prevents bad curation from corrupting main data.

## Server Lifecycle

The server auto-starts in the background when any CLI command needs it. Users never think about server management.

```bash
gramaton search "..."    # server not running? starts automatically, silently
gramaton capture ...     # already running? connects instantly
gramaton status          # shows server state, pid, uptime
gramaton stop            # explicit shutdown (also: auto-stops after configurable idle)
gramaton serve           # power-user: run in foreground with visible logs
```

**Auto-start behavior:**
- Any command that needs the server checks if it's running (PID file at `~/.gramaton/server.pid`)
- If not running, starts the server in the background. Sub-second startup (pure Go, no external deps).
- First command takes ~1 second (server startup + request). Subsequent commands connect instantly.

**Auto-stop behavior:**
- After a configurable idle period with no commands (default: 30 minutes), the server shuts down cleanly
- System sleep/shutdown triggers clean stop
- `gramaton stop` for explicit shutdown

**PID management:**
- Lockfile prevents double-start
- Stale PID detection (process died without cleanup)
- `gramaton status` always shows current state

```yaml
server:
  port: 0                # OS assigns available port on first start, saved to config
  auto_start: true          # start server automatically when needed
  idle_timeout: 30m         # stop after this long with no activity
```

**Precedent:** Docker Desktop, Ollama, and other developer tools auto-start their background processes transparently. The key is fast startup, clean shutdown, predictable resource usage, and discoverability via `status`.

## Distribution

- **Single pure Go binary.** No CGo, no native dependencies. Cross-compiled trivially for macOS (ARM + Intel), Windows, Linux.
- **No Python, no Node, no JVM, no Docker required.** Download binary, run it.
- **Embedding via Ollama (default) or API.** Users configure an embedding provider via `gramaton init`. Ollama is recommended for local, private embeddings. OpenAI-compatible APIs and AWS Bedrock also supported.
- **Config file at `~/.gramaton/config.yaml`.** Sensible defaults for everything. Most users only touch it during `gramaton init`.
