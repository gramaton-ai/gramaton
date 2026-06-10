# Architecture

This document describes Gramaton's internal architecture for developers working on the codebase. If you're looking for how to *use* Gramaton as an integrator, see the [Integrator Guide](integrator-guide.md) instead; if you're looking for how to change the code, read this together with [CONTRIBUTING.md](../CONTRIBUTING.md).

## System overview

Gramaton is a single Go binary that runs as an on-demand daemon. The CLI auto-starts the server on first use. The server manages the graph, indexes, embeddings, and background curation. All state lives on the local filesystem.

```
   Agent
     │
     │  MCP  /  HTTP  /  CLI
     ▼
┌────────────────────────────┐
│  Server transports         │   bindings_*.go, mcp.go, handler_intake.go,
│  + curation runner         │   curation/runner.go
└─────────────┬──────────────┘
              │
┌─────────────┴──────────────┐
│   api (canonical ops)      │   one file per operation; locking discipline
└─────────────┬──────────────┘
              │
┌─────────────┴──────────────┐
│   core.Engine              │   composition root; owns RWMutex
└─────────────┬──────────────┘
              │
  ┌───────────┼───────────────────┐
  ▼           ▼                   ▼
┌───────┐   ┌─────────────────┐   ┌──────────┐
│ graph │   │ indexes         │   │ storage  │
│       │   │ BM25 / Property │   │ prolly   │
│       │   │ HNSW / Flat     │   │ tree     │
│       │   │ Collections /   │   │          │
│       │   │ Secondary       │   │          │
└───────┘   └─────────────────┘   └──────────┘

                 embed.Provider      llm.Provider
                 (Ollama / BERT /    (Anthropic /
                  OpenAI / Bedrock)   OpenAI / Bedrock)
```

Requests flow downward; dependencies flow inward. Nothing in `core/`, `graph/`, `index/`, or `storage/` imports `api/` or `server/`.

## The four layers

### 1. Transports (`server/`, `cli/`)

The outermost layer. Three transports, one surface underneath.

- **HTTP**: `server/bindings_*.go` register Cobra-style routes via `http.ServeMux`. Each route deserializes a request body, calls an `api.API` method, and serializes the response. No business logic — just wire format translation and an `api.APIError` → HTTP status mapping. The whole mux is wrapped by `securityHeaders`, which sets security headers, saves the response status for the request log, and installs a panic-recover defer at the transport boundary — an unrecovered panic inside any api/ method is logged and turned into a structured `{code:"internal", retryable:false}` 500 envelope rather than a closed connection. `http.ErrAbortHandler` is re-panicked so net/http's intentional-abort semantics survive.
- **MCP (Streamable HTTP + stdio)**: `server/mcp.go` wires the MCP SDK's server to a `/mcp` route on the same HTTP listener (loopback-only — non-loopback callers get rejected before reaching the handler), and `cli/mcp_cmd.go` exposes an equivalent stdio entry point. `server.registerMCPTools` calls nine cluster registrars (`bindings_records.go`, `bindings_search.go`, `bindings_sessions.go`, `bindings_collections.go`, `bindings_admin.go`, `bindings_history.go`, `bindings_maintenance.go`, `mcp_intake.go`, `mcp_guide.go`), each of which registers MCP tools that call `api.API` methods directly — not through HTTP.
- **CLI**: `cli/*.go` holds one Cobra command per operation. A CLI command opens a local HTTP client against the server and calls the HTTP route (`cli/httpclient.go`). For MCP-native clients that run Gramaton as a stdio subprocess, `cli/mcp_cmd.go` + `cli/mcp_proxy_*.go` register the same MCP tool set, proxying each call to the HTTP server via the same local client.

The important invariant: all three transports consume the same request/response types declared once in `api/`. There is no per-transport struct. A tool description written as `api.CaptureDescription` appears verbatim in MCP (direct registration, CLI proxy) and in any docs generated from the package.

### 2. The canonical operations surface (`api/`)

`api/` is the single source of truth for what an operation *is*. One file per operation (`api/save.go`, `api/search.go`, `api/sessions.go`, `api/collections.go`, `api/branches.go`, ...). Each follows the same shape:

```go
type XxxRequest  struct { /* json + jsonschema tags */ }
type XxxResponse struct { /* json tags */ }
const XxxDescription = "..."   // MCP tool description
func (a *API) Xxx(ctx context.Context, req XxxRequest) (XxxResponse, *APIError)
```

Locking discipline lives here. Methods call `a.engine.Lock()` / `Unlock()` (or `RLock()` / `RUnlock()`) and do nothing slow inside the lock:

- Validate inputs.
- Pre-embed content **outside** the lock (`a.preEmbedContent(...)` in `api/save.go` is the canonical example — embeddings take tens of milliseconds, so the lock stays short).
- Acquire lock, mutate or read, release.
- Serialize results.

`api.API` itself is constructed via `api.New(Dependencies{...})`. Dependencies are `Engine`, `Runner`, `UsageTracker`, `Log`, `ConfigDir`, `StoreName`. The API also owns transient state that outlives a single request — most notably the prepared-sessions map (`preparedSessions`, sweeper goroutine).

See CONTRIBUTING.md's "Adding a new operation" recipe for the full five-step procedure when adding a new operation to this layer.

### 3. The composition root (`core.Engine`)

`core/engine.go` owns the wiring. An `Engine` holds:

- The loaded graph (`graph.Graph`).
- The index set (`indexSet`: BM25, vector HNSW/Flat, property, secondary, collections, commit-timestamp).
- A `searcherSubsystem` that wraps `search.Tool` — pure computation on top of the graph and indexes.
- Provider references (`embed.Provider`, `llm.Provider`).
- The storage layer (`storage.Store`).
- A single `sync.RWMutex` that gates all mutation.
- A dirty flag for access metadata (access counts are flushed periodically rather than on every read).

`LoadEngine` / `LoadEngineWithOptions` are the public constructors. Options support dependency injection for tests (`WithEmbedder`, `WithLLM`, `WithVectorIndex`).

The Engine never knows about the transports or the api/ layer. It exposes primitives (`Graph()`, `VecIdx()`, `PropIdx()`, `CheckDedup()`, `IndexNode()`, `Save()`, `Lock()`/`Unlock()`) that `api/` methods compose into operations.

### 4. Data layer (`graph/`, `index/`, `storage/`)

- **`graph/`**: in-memory property graph. Nodes with typed key-value properties, edges with type + weight + optional properties. Pure data structure — no I/O. `graph.Properties` is a `map[string]Property`; property types are a sum (`String`, `Float64`, `Int64`, `Bool`, `Timestamp`, `Vector`, `StringList`, `Bytes`).
- **`index/`**: BM25 (`bbolt_bm25.go`), vector indexes (`hnsw.go`, `flat_mmap.go`, switched dynamically by candidate-set size), property exact/range lookups (`bbolt_property.go`), secondary indexes (`bbolt_secondary.go`), and collections metadata (`bbolt_collections.go`). The commit-timestamp index (`graph/tsindex.go`) lives alongside `graph/bbolt_edges.go` since commits are graph-level concepts but it shares the same bbolt database as the index/ types. All persisted via bbolt buckets or a mmap'd flat file for vectors.
- **`storage/`**: prolly tree — a probabilistic B-tree with content-addressed chunks. Mutations create new root hashes; old roots stay reachable as commit history. `storage/cas.go` is the content-addressed store; `storage/prolly.go` is the tree itself; `storage/gc.go` garbage-collects unreferenced chunks.

The graph is fully materialized in memory on startup and flushed to the prolly tree on save. Queries never hit disk once the server is warm. Saves are incremental: the graph tracks dirty nodes/edges and only marshals what changed (O(K) instead of O(N)). The BM25 index is persisted alongside each commit and loaded from disk at startup, skipping re-tokenization.

## Package map

### Protocol / outer layer

| Package | Purpose |
|---------|---------|
| `server/` | HTTP server, MCP registrar, binding tables per operation cluster, curation runner lifecycle, intake handler |
| `cli/` | Cobra commands (one file per command), HTTP client for hitting the server, MCP stdio entry point, MCP proxy cluster files |

### Operations

| Package | Purpose |
|---------|---------|
| `api/` | Canonical request/response types, descriptions, and methods. Locking discipline. The surface every transport consumes. |

### Engine + computation

| Package | Purpose |
|---------|---------|
| `core/` | `Engine` — composition root; holds graph, indexes, providers, RWMutex. Constructors and functional options. |
| `search/` | `Tool` — pure computation: hybrid vector + BM25 with RRF fusion, scoring (`score.go`), reranking, dedup, query decomposition. No I/O. |
| `curation/` | Deterministic and autonomous curation. `Runner` (timer-driven, default 1-minute cadence) inside the server process. Lifecycle transitions, orphan linking, dedup, concept candidate detection + enrichment (deterministic); classification, summary generation, contradiction detection, qualitative manifest (autonomous, LLM-gated). Per-task wall-clock timeout (`curation.task_timeout`, default 90s) prevents one hung call from starving a cycle. Startup self-heal hook (`runStartupSelfHeal`) runs a one-shot content-quality pass when the server starts. Per-collection eligibility is gated by orthogonal behavior knobs on the collection node (`curation`, `supersession`, `contradictions`, `clear_mode`); `EffectiveCurationFor(record)` resolves the effective level by walking `member_of` edges. New ad-hoc collections default to `curation=none`; the standard templates (`backlog`, `todo`, `reading-list`, `journal`, `references`) opt in to `curation=standard` and declare a `content_fields` list naming the fields the LLM treats as the item's content. |
| `dedup/` | Near-duplicate detection via vector similarity + Jaccard guard. |
| `chunking/` | Long-content splitting before embedding. |

### Providers

| Package | Purpose |
|---------|---------|
| `embed/` | `Provider` interface + factory. Implementations: `embed/bert/` (pure-Go default), `embed/ollama/`, `embed/openai/`, `embed/bedrock/`. |
| `llm/` | `Provider` interface + factory. Implementations: `llm/anthropic/`, `llm/openai/`, `llm/bedrock/`. Provides `Complete`, `CompleteWithModel`, and `CompleteStructured` (provider-gated via `SupportsStructuredOutput()`); curation uses the structured path for classification when available and falls back to plain `Complete` otherwise. Usage tracking and rate limiting live at this layer (`metered.go`, `ratelimit.go`, `pricing.go`, `usage.go`). |

### Data

| Package | Purpose |
|---------|---------|
| `graph/` | In-memory property graph: nodes, edges, properties. Pure data structure. |
| `index/` | Persisted indexes: BM25, vector (HNSW / Flat / mmap), property, secondary, collections. |
| `storage/` | Prolly tree on a content-addressed store. Commit history, garbage collection. |

### Support

| Package | Purpose |
|---------|---------|
| `config/` | Config types, `Defaults()`, YAML load/save, named-store fallback resolution. |
| `logging/` | Rotating file logger with size budgets. |
| `backup/` | Tar archive backup/restore, export/import. The walker takes a bbolt-native coherent snapshot of `jobs.db` via `View+WriteTo` so the file is always consistent in the tarball. |
| `jobs/` | Persistent async-job tracking. Bbolt-backed store (separate `jobs.db` from `indexes.db`) for `gramaton_save_batch` and future async ops. State machine with explicit transition whitelist; per-Get schema migration via `FormatVersion`; tenant-scoped `FindByClientToken`; TTL-based GC sweep goroutine started by `core.Engine`. |
| `hooks/` | Go implementation of agent lifecycle hooks (session-start, stop, pre-compact, post-compact; Kiro agent-spawn, user-prompt-submit, stop). Exposed as `gramaton hook <event>` subcommands; one-line proxy scripts at `~/.gramaton/hooks/**/*.{sh,cmd}` forward stdin to them. Proxy variants per harness: `.sh` everywhere for Claude Code (bundles Git Bash on Windows); `.cmd` on Windows for Kiro (native, no bash); both variants on every host for Codex (its hooks.json entries carry `command` + `commandWindows` and pick per-OS at runtime). Codex shares Claude Code's stdin contract, so the same `gramaton hook` handlers serve both. |
| `internal/awscfg/` | Shared AWS credential chain loader for Bedrock providers. |
| `internal/setup/` | Interactive setup wizard (`gramaton init`) used to register MCP entries and install per-client agent guidance. Supported harnesses are declared in a registry (`harness.go`); wizard steps iterate it instead of switching on client names, so adding a harness is one registry entry plus an addendum template. |
| `internal/setup/templates/` | Single-source agent-guidance prose: shared `guidance/base.md` plus per-harness `guidance/addendum_*.md`, rendered by `templates.Render` with `{{client_name}}` / `{{mcp_reconnect_hint}}` interpolation. `templates.GuidanceVersion` is stamped into the installed fence marker (`v=X.Y.Z`) for drift detection (#80); bump discipline in CONTRIBUTING.md. `integration/<dir>/` snapshots (including the harness-neutral `custom-agents/system-prompt.md` for bespoke-agent builders) are drift-tested against the canonical renderers via `TestIntegrationSnapshotsMatchCanonical` (refresh with `go test ./internal/setup -update-integration`). |
| `internal/mmap/` | Cross-platform read-only file mmap: `syscall.Mmap` on Unix, `CreateFileMapping` + `MapViewOfFile` on Windows via `golang.org/x/sys/windows`. Consumed by `embed/bert/safetensors.go` (model weights) and `index/flat_mmap.go` (vector index). In-house to preserve Gramaton's frugal-deps posture — `edsrzf/mmap-go` carries 500+ LOC of features we don't use. |
| `internal/version/` | Build-time version injection. |

## Dependency direction

Dependencies flow inward. Outer layers depend on inner layers, never the reverse.

```
server/ cli/        → api/ core/ search/ curation/
api/                → core/ curation/ llm/ graph/
core/ search/       → graph/ index/ storage/ embed/ llm/ config/
curation/           → core/ graph/ llm/ config/
graph/ index/       → standard library (bbolt for persistence)
storage/            → graph/ (for serialization)
embed/*/  llm/*/    → config/ internal/awscfg/
```

No package in `core/` or below imports `api/` or `server/`. No `embed/xxx` or `llm/xxx` provider imports another provider — each is self-contained behind its interface.

## Lifecycle

**Startup** (`LoadEngine` → `server.New` → `server.Start`):

1. Load `config.yaml` with optional named-store fallback (`config.LoadWithFallback`).
2. Open the prolly tree at `data_dir/store/`.
3. Instantiate the embedding and LLM providers from config; nil providers are legal (embedding/LLM are both optional).
4. Construct the partial `core.Engine` (cfg, store, prov, opts) and call `Engine.OpenFiles` for the file-init half: bbolt + indexes + graph rebuild + vector index + jobs store + sweeper. `OpenFiles` is also the entry point for re-opening after a Restore swap.
5. Start the curation `Runner` goroutine and the prepared-sessions sweeper.
6. Bind the HTTP listener; mount the MCP Streamable HTTP handler at `/mcp` (loopback-only).

The `OpenFiles` body, in order: format-version check, bbolt open, index set construction, graph rebuild from HEAD, EngineOption replay (for test-injected vec/embedder/llm), default vector index open if not injected, partial-rebuild of primary indexes, searcher subsystem build, jobs store open + in-flight job recovery, sweeper goroutine spawn, search-snapshot store. Mirrored by `CloseFiles` for the destructive operations (Restore) that must release file handles before the on-disk swap.

**Request**: transport handler → `api` method → engine primitives → response → serialized back out. Lock is held only inside the api method, only for the mutation window.

**Idle**: the server tracks last-request time. After the configured `idle_timeout` (default 4 hours) with no activity, it shuts down gracefully. CLI auto-starts a new server on the next call.

**Shutdown**: `server.Stop` cancels the curation runner, stops the prepared-sessions sweeper, flushes access metadata, closes indexes, closes bbolt, closes the prolly store.

## Data flow examples

### Save

```
Client → POST /v1/records → api.Save
  1. Validate request (content length, enums, meta shape)
  2. Pre-embed content_short outside the lock (via a.engine.Embedder())
  3. engine.Lock()
  4. Create node with properties (content_full, processing_status, context_*, meta.*, timestamps)
  5. IndexNode for BM25 + any meta text
  6. Attach pre-computed embedding via applyPreEmbedded; pick best vector for the search index
  7. CheckDedup: if cosine ≥ threshold and action = reject → rollback node, return ErrConflict
                 if action = supersede → mark older record historical, create supersedes edge
  8. Save incremental prolly update
  9. engine.Unlock()
  10. Serialize CaptureResponse (id, warnings, superseded[])
```

### Save batch (sync)

```
Client → POST /v1/save/batch (Wait=true) → api.SaveBatch
  1. Phase 0: validate envelope (item count cap, byte budget, ClientToken UUID, edge count cap)
  2. ClientToken idempotency: FindByClientToken(token, tenant) — same body returns prior JobID,
     mismatching body rejected with conflict, failed/cancelled prior gets a fresh job linked via SupersedesJobID
  3. Create Job (kind=capture_batch, status=running, TenantID from ctx)
  4. Phase 1: per-item validation off-lock (failures land in failed[])
  5. Phase 2: batch embed off-lock with per-item fallback
  6. Phase 3 (single Save): engine.Lock(); commit valid items + intra-batch edges; engine.Save; engine.Unlock()
     - Save failure: scoped rollback PropIdx + VecIdx + BM25 + SecIdx + Graph; mark failed/save_failed
  7. JobStore.Update with Result + Errors; mark completed
```

### Save batch (async, multi-chunk)

```
Client → POST /v1/save/batch (Wait=false) → api.SaveBatch
  1. Common prelude (validate, idempotency, create Job with status=pending)
  2. Spawn goroutine via runCaptureBatchAsync; return JobID + status=pending immediately

In the goroutine (runCaptureBatchAsyncChunked):
  a. Re-read Job status; advance pending→running atomically (cancel that won the race exits cleanly)
  b. Phase 0/1 + Phase 2 once for ALL items
  c. For each chunk of MaxSyncBatchSize:
     - shouldStopChunked (ctx.Done() OR Job.Status=cancelled) → finalizeCancelledWithProgress
     - engine.Lock(); commit chunk's items in one Save ("capture_batch chunk N/M"); engine.Unlock()
     - chunk Save failure: scoped rollback this chunk only; finalizeChunkSaveFailure with reason "chunk_N_save_failed"
     - AdvanceStatus(running, mutator) persists ProcessedCount + ClientRefToID for status readers
  d. Edge fixup: shouldStopChunked → engine.Lock(); resolve every edge against the now-complete ClientRefToID map;
     engine.Save("capture_batch edge fixup"); engine.Unlock()
     - fixup Save failure: rollback in-memory edges; finalizeEdgeFixupFailure with reason "edge_fixup_failed"
       and Result.EdgesFailed listing every edge with code "fixup_failed" for caller-driven gramaton_link replay
  e. Mark completed; populate Job.Result with the full CaptureBatchResponse (items + edges + stats)

Companion endpoints:
  GET  /v1/save/batch/{job_id}/status  → live snapshot (read-only, cheap to poll)
  POST /v1/save/batch/{job_id}/cancel  → flip Job.Status=cancelled atomically; signal runner ctx
  GET  /v1/save/batch/{job_id}/result  → block (poll backoff) until terminal or timeout
  GET  /v1/jobs                            → enumerate jobs (tenant-filtered, paginated)
```

### Search (fresh query)

```
Client → POST /v1/search → api.Search (no cursor in request)
  1. Pre-embed query text outside the lock (if embedding configured)
  2. engine.RLock()
  3. Filter candidates by metadata (property index, resolution, lifecycle, epistemic status, temporality, ...)
  4. Hybrid rank: vector similarity + BM25 keyword, fused via RRF
  5. Composite score per candidate: similarity, knowledge freshness (temporality-keyed exponent), access recency, confidence. Importance acts as a floor.
  6. Materialize the top-`candidate_cap` (default 500, hard ceiling 1000) IDs + scores into a SearchSnapshot keyed by a fresh ULID query_id; pin in core.SnapshotStore with `snapshot_ttl` (default 20m)
  7. Slice page 1 from the snapshot at the requested page_size (default 20, max 100); fetch record content + neighbor traversal for that page only
  8. Record access (bump access_count / last_accessed / activation_boost on neighbors; flushed later by access-dirty timer)
  9. engine.RUnlock()
 10. Assemble result rows with metadata summaries + store origin (memory / sessions); response carries `page`, `page_size`, `total`, `next_cursor`, `query_id`, and a `pages` table of `{page, cursor}` for every page in the snapshot
```

### Search (paginated — cursor in request)

```
Client → POST /v1/search → api.Search (cursor encoded as `query_id:start:page_size`)
  1. Decode cursor; look up SearchSnapshot by query_id in core.SnapshotStore
     - Miss / expired: return {error: "snapshot_expired"}; caller re-runs the original query
  2. engine.RLock()
  3. Slice the encoded page from the snapshot's frozen ID list — match set is the same one the original query produced
  4. Fetch record content per ID (fresh; mutations since the snapshot are visible)
  5. engine.RUnlock()
  6. Assemble result rows; response echoes the same `query_id`, the next/previous cursors from the same `pages` table, and `ignored_params` listing any text/match/filter args that were dropped (the cursor's snapshot wins over fresh filter args)
```

For exhaustive retrieval beyond `candidate_cap`, callers route to `gramaton_export` instead — same filter set, no candidate cap, streaming three-phase (RLock → collect IDs; per-record RLock → fetch + write) so a long export doesn't hold a single read lock across all I/O.

### Session commit

```
Client → POST /v1/sessions/{id}/save → api.SessionSave
  1. Verify session has a prior prepare (tracked in a.preparedSessions)
  2. For each segment:
     a. Pre-embed summary_short outside the lock
     b. engine.Lock()
     c. Create a Session segment node (BM25-indexed)
     d. If promote_to_memory (default true): create a linked Memory record with the same content,
        check dedup, run auto-supersession against prior Memory records
     e. Link the Session segment to the Memory record via extracted_as edge
     f. engine.Unlock()
  3. Single final Save for the batch
  4. Return per-segment IDs and any supersession records
```

## Concurrency model

The engine uses one `sync.RWMutex`. Rules:

- **`api/` methods** acquire and release the lock internally. They are the only layer that calls `engine.Lock()` / `Unlock()`.
- **Transport handlers** never hold the lock directly — they call api methods.
- **Network I/O** (embedding calls, LLM calls) happens outside the lock. The canonical pattern: RLock to read what you need, Unlock, call provider, Lock to write results.
- **Curation** runs in its own goroutine driven by a timer. Curation phases call the same api methods or engine primitives the transports do, so they participate in the same lock.
- **Access metadata** (access_count, last_accessed, activation_boost) is written under the lock but not flushed to disk on every read. A background flusher saves the accumulated batch periodically (see `accessDirty` in `core/engine.go`). This trades exact durability of "last accessed at" for O(1) reads on hot queries.

## Storage model

Gramaton uses a prolly tree (probabilistic B-tree) for persistence:

- **Content-addressed.** Every chunk is identified by its hash. Identical subtrees share storage.
- **Append-only.** Mutations create new root hashes. Old roots stay reachable as commit history. `gramaton log`, `gramaton diff`, `gramaton revert` work because the old state is still there.
- **Deterministic splits.** Chunk boundaries are determined by hashing, so independent mutations to the same tree produce structurally identical results (enabling clean branch merges).

The graph is materialized in memory on startup and flushed incrementally on `Save`. Dirty tracking (per graph object) keeps saves O(K) where K is the number of modified nodes/edges. The BM25 index is persisted as a content-addressed chunk referenced by each commit (`bm25_root`), so startup skips re-tokenization at cost of a small per-commit storage overhead.

Garbage collection of unreferenced chunks is available via `storage/gc.go` but disabled by default — the "never delete" tenet means commit history is cheap to keep.

## Adding a new operation

See CONTRIBUTING.md's operation recipe. The short version:

1. Add a file at `api/<op>.go` with `XxxRequest`, `XxxResponse`, `XxxDescription`, `func (a *API) Xxx(...)`. Use jsonschema tags on request fields.
2. Register the HTTP route in the right `server/bindings_<cluster>.go`.
3. Register the MCP tool in the same bindings file (direct) and in the matching `cli/mcp_proxy_<cluster>.go` (proxy). Both use `api.XxxDescription`.
4. Add a CLI command under `cli/<op>.go` if end-users should call it directly.
5. Add tests in `api/<op>_test.go` (and binding tests if the HTTP/MCP surface has non-trivial transport concerns).

The `.claude/skills/new-operation/` skill encodes this as an invocable procedure for LLM assistants.

## Adding a new provider

### Embedding

1. Create `embed/<name>/<name>.go` implementing `embed.Provider`:
   ```go
   type Provider interface {
       Embed(ctx context.Context, texts []string) ([][]float32, error)
       ModelID() string
       ContextWindow() int
   }
   ```
2. Add the case to `embed.New()` in `embed/embed.go`.
3. Add tests in `embed/<name>/<name>_test.go` following the shape of `embed/bert/bert_test.go` or `embed/ollama/ollama_test.go`.

### LLM

1. Create `llm/<name>/<name>.go` implementing `llm.Provider` (see `llm/llm.go` for the current interface; it includes completion plus usage reporting).
2. Add the case to `llm.New()` in `llm/llm.go`.
3. Add pricing data to `llm/pricing.go` if cost tracking is needed.
4. Add tests mirroring existing provider tests.

Both interfaces are intentionally minimal. The provider handles auth, retries, and serialization internally. `core.Engine` and `api.API` never import provider packages directly — they go through the interface.
