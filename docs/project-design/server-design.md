# Server Design (v0.2)

## Overview

Gramaton v0.2 introduces an on-demand daemon that serves an HTTP API.
The CLI becomes a thin client. All state management, concurrency control,
and persistence flow through the server. This document captures the
architecture decisions and open questions for the server implementation.

## Architecture Model

### On-Demand Daemon

The server follows the Docker/Ollama pattern:

1. First CLI invocation detects no running server
2. CLI starts the server as a background process
3. CLI sends its request to the server over HTTP
4. Server stays running, handles subsequent requests from any client
5. After a configurable idle period, the server shuts down gracefully

In container/remote environments, the server is started explicitly
via `gramaton serve` and runs until stopped.

### Component Roles

```
gramaton CLI (thin client)
  |
  | HTTP (localhost or network)
  |
gramaton server (daemon)
  |
  +-- Graph engine (in-memory, loaded from store on startup)
  +-- Property index
  +-- Vector index
  +-- Embedding provider (Ollama / OpenAI / Bedrock)
  +-- LLM provider (optional, for autonomous curation)
  +-- Persistence (content-addressed store on disk)
```

### Design Principles

- **The HTTP API is the first-class contract.** Every client -- CLI,
  OpenClaw, MCP, mobile, custom agents -- uses the same API.
- **The CLI is the primary user interface.** "Thin client" is an
  implementation detail, not a status demotion. Same commands, same
  output, same experience.
- **Single binary, zero runtime dependencies.** The server needs only
  the gramaton binary and a store directory. No database, no message
  queue, no Redis.
- **Fast startup, clean recovery.** The server must handle SIGKILL
  and cold restarts. Content-addressed storage with atomic writes
  guarantees consistency. Every startup is treated as potential recovery.
- **The store directory is the only state.** Mount it into a new
  container, start gramaton, and all knowledge is there.
- **High-quality, well-supported dependencies only.** No risky or
  niche libraries. Dependencies must be: well-maintained (active
  commits, multiple contributors), industry-standard, and licensed
  under MIT or Apache 2.0 for enterprise compatibility. We don't
  want to solve all problems ourselves, but we don't take on
  dependencies that could become liabilities.

## Decisions Made

### Embedding Providers

Gramaton connects to embedding providers but does not own their
lifecycle. The auto-start convenience for Ollama (v0.1) is removed
from the server; `gramaton init` retains guided setup.

Supported providers for v0.2:
- **Ollama** (local, free, default for `gramaton init`)
- **OpenAI-compatible** (OpenAI, Azure, Mistral, Together, etc.)
- **AWS Bedrock**

Single embedding model per store. Model migration via `gramaton reembed`.
Multi-model support deferred -- the migration path is sufficient.

Ollama's shared singleton model means gramaton cannot guarantee model
residency in memory (LRU eviction). Cloud providers are the upgrade
path for users who need consistent latency.

### Autonomous Curation

The server supports optional LLM-driven curation:

```yaml
llm:
  provider: ollama        # or openai, bedrock
  model: llama3.2         # lightweight model for classification
  api_key_env: ""         # for cloud providers
```

When configured, the server classifies pending records on a schedule.
When not configured, piggyback curation (agent-driven) and manual
classification still work. Three mechanisms, no single point of failure:

| Mechanism         | Runs when                        | Requires               |
|-------------------|----------------------------------|------------------------|
| Server-autonomous | On schedule or pending threshold | LLM provider configured |
| Piggyback         | Agent sees overdue in response   | Agent with LLM access   |
| Manual            | User runs classify               | Human judgment          |

**Privacy note:** cloud LLM providers send record content off-device.
`gramaton init` must warn about this. Ollama keeps everything local.

### Tiered Capability

Every tier is fully functional. Higher tiers add capability, nothing
breaks without them.

1. **No API key, no Ollama** -- keyword/property search, manual and
   piggyback curation. The absolute floor.
2. **Ollama only** -- vector search, local LLM for autonomous curation.
   No cost.
3. **Cloud API key** -- fastest embeddings, most capable classification
   model. Power user path.

### Branching

Per-request branch selection via HTTP header:

```
X-Gramaton-Branch: curation-2026-04-04
```

Default is `main`. The CLI stores the "checked out" branch in a local
config file and sends it automatically. No global checkout state on the
server -- safe for multi-client access.

### Access Recording

Access recording (access_count, last_accessed) and spreading activation
are internal server bookkeeping. They are not part of the API contract.
The server updates them when serving records, like a web server writing
access logs. Clients do not need to be aware of or trigger these.

### File Ingestion

The API supports both local and remote ingestion from day 1:

- **Local path mode:** server reads files from a path on its filesystem.
  Used by CLI on the same machine.
- **Remote upload mode:** client sends file content in the request body.
  Supports single file, bulk (multiple files in one request), and
  large files via multipart/form-data.

## HTTP API (v1)

### Response Envelope

Every response is wrapped:

```json
{
  "data": { ... },
  "curation": {"pending_count": 5, "overdue": true},
  "meta": {"duration_ms": 12, "version": "0.2.0"}
}
```

Errors use standard HTTP status codes:

```json
{
  "error": {
    "code": "not_found",
    "message": "record not found",
    "retryable": false
  }
}
```

### Endpoints

#### Records

| Method | Path                         | Description        | Maps to CLI        |
|--------|------------------------------|--------------------|--------------------|
| POST   | /v1/records                  | Create a record    | `capture`          |
| GET    | /v1/records/{id}             | Get a record       | `inspect <id>`     |
| PATCH  | /v1/records/{id}             | Update properties  | `update`           |
| DELETE | /v1/records/{id}             | Soft delete        | `delete <id>`      |
| POST   | /v1/records/{id}/edges       | Create an edge     | `update` (link_to) |
| POST   | /v1/records/{id}/classify    | Classify a record  | `classify`         |
| GET    | /v1/records/{id}/history     | Per-record log     | `log --record`     |

#### Search and Traversal

| Method | Path         | Description       | Maps to CLI             |
|--------|--------------|-------------------|-------------------------|
| POST   | /v1/search   | Query the store   | `search [query] [flags]`|
| POST   | /v1/explore  | Graph traversal   | `explore <id> [flags]`  |

#### Pending Records

| Method | Path         | Description              | Maps to CLI   |
|--------|--------------|--------------------------|---------------|
| GET    | /v1/pending  | List unclassified records| `pending`     |

#### Branches

| Method | Path                             | Description     | Maps to CLI            |
|--------|----------------------------------|-----------------|------------------------|
| GET    | /v1/branches                     | List branches   | `branch list`          |
| POST   | /v1/branches                     | Create a branch | `branch create <name>` |
| POST   | /v1/branches/{name}/checkout     | Checkout        | `branch checkout`      |
| POST   | /v1/branches/{name}/merge        | Merge           | `branch merge`         |
| DELETE | /v1/branches/{name}              | Discard         | `branch discard`       |

#### History

| Method | Path      | Description       | Maps to CLI             |
|--------|-----------|--------------------|------------------------|
| GET    | /v1/log   | Commit log         | `log`                  |
| GET    | /v1/diff  | Compare commits    | `diff [flags]`         |

#### Operations

| Method | Path          | Description             | Maps to CLI     |
|--------|---------------|-------------------------|-----------------|
| POST   | /v1/revert    | Restore a prior commit  | `revert <hash>` |
| POST   | /v1/reembed   | Regenerate embeddings   | `reembed`       |
| POST   | /v1/ingest    | Ingest files            | `ingest <path>` |
| POST   | /v1/duplicates | Find near-duplicates  | `duplicates`    |

#### System

| Method | Path        | Description         | Maps to CLI    |
|--------|-------------|---------------------|----------------|
| GET    | /v1/status  | Health + stats      | `status`       |
| GET    | /v1/stats   | Aggregate stats     | `stats`        |

#### MCP

| Method | Path   | Description           | Maps to CLI |
|--------|--------|-----------------------|-------------|
| *      | /mcp   | MCP protocol endpoint | N/A         |

### CLI-Only Commands (No API Equivalent)

- `gramaton init` -- interactive guided setup
- `gramaton tempdir` -- local temp directory path
- `gramaton serve` -- start the server

## Usage Topologies

### Topology A: Local Co-Resident

Agent and gramaton on the same machine. Claude Code, Kiro CLI,
local OpenClaw.

```
[Agent] --localhost--> [Gramaton Server] --> [Store on disk]
```

Server auto-starts on first CLI call. Idle timeout shuts it down.

### Topology B1: Co-Located in Container

Agent and gramaton in the same container. Remote agent environments.

```
[Container: Agent + Gramaton Server] --> [Store on persistent volume]
```

Server started via `gramaton serve` in the container entrypoint.
Store on a mounted persistent volume survives container restarts.

### Topology B2: Remote Server

Gramaton runs separately from the agent. Agent connects over the
network.

```
[Agent (anywhere)] --network--> [Gramaton Server] --> [Store]
```

Requires authentication and TLS. Server binds to 0.0.0.0 instead
of loopback.

### Topology C: Pause/Resume

Agent session pauses (container suspends). Later it resumes, possibly
on different hardware. Server treats every startup as potential recovery.
Content-addressed storage with atomic writes guarantees consistency.

## Decided: Concurrency Model

Graph-level `sync.RWMutex`. One lock for the entire graph and indexes.

**Read operations** (search, inspect, explore, pending, log, diff,
status) acquire `RLock`. Multiple readers run concurrently and never
block each other.

**Write operations** (capture, update, classify, delete, merge, revert)
acquire `Lock`. One writer at a time. Blocks readers during the write.

**Persistence:** synchronous commit on every write. Prolly trees make
this cheap (O(changes), not O(N)). No write-ahead log, no batched
commits. No crash window where data could be lost.

**Branch model:** one active graph in memory. Branch operations
(checkout, merge) are exclusive operations that reload the graph
under the write lock. All clients see the same branch.
Per-request branch selection deferred to v0.3.

**Commit history:** linear. The RWMutex serializes all writes, so
commits form a clean chain with no merge needed.

**Rationale:** the workload is 2-5 concurrent clients, read-heavy,
with writes lasting milliseconds. Graph-level locking is ~20 lines
of code, correct by construction, and will never be a bottleneck for
a personal knowledge store. If it ever becomes one, the upgrade path
is branch-level locking.

## Decided: Server Lifecycle and Discovery

### Port and Bind Address

Default port 42982 (unassigned by IANA, no known conflicts).
Default bind address `127.0.0.1` (loopback only). Both configurable:

```yaml
server:
  port: 42982
  bind: "127.0.0.1"
  idle_timeout: 30m
```

Fixed default port rather than OS-assigned because:
- Remote/container users need a predictable port for firewall rules
- Documentation and examples are clearer with a known port
- Discovery is simpler

### Server Info File

The server writes `~/.gramaton/server.json` on startup:

```json
{
  "pid": 12345,
  "port": 42982,
  "bind": "127.0.0.1",
  "started_at": "2026-04-04T12:00:00Z",
  "store_dir": "/Users/b/.gramaton/data",
  "version": "0.2.0"
}
```

The `store_dir` field prevents connecting to the wrong server when
multiple stores exist (via `--config-dir`).

### CLI-to-Server Discovery

1. Read `~/.gramaton/server.json` (or `<config-dir>/server.json`)
2. If found and PID is alive and store_dir matches:
   a. Hit `GET /v1/status` to verify it's a gramaton process
      (guards against PID reuse by a different process)
   b. If health check passes → connect
   c. If health check fails → treat as stale, delete file, start server
3. If found but PID is dead → delete stale file, start server
4. If not found → start server

### `gramaton serve`

```
gramaton serve            # background (default)
gramaton serve --fg       # foreground, for containers/debugging
gramaton serve --stop     # send shutdown to running server
```

### Auto-Start

When the CLI needs a server and none is running:

1. Start `gramaton serve` as a detached background process
   - Unix: `Setsid: true` to create new session
   - Windows: `CREATE_NEW_PROCESS_GROUP`
2. Poll `GET /v1/status` with backoff (up to 10 seconds)
3. Send the original request once the server is ready

Race condition protection: server acquires `flock` on
`~/.gramaton/server.lock` before binding. Second instance detects
the port is taken, checks the health endpoint, and connects.

### Idle Timeout

Default: 30 minutes, configurable. Server tracks the time of the
last client HTTP request. Background goroutine checks every 60
seconds. Internal operations (autonomous curation, stale sweep)
do not reset the timer.

### Graceful Shutdown

Triggered by SIGTERM, SIGINT, idle timeout, or `POST /v1/shutdown`
(loopback only):

1. Stop accepting new connections
2. Wait for in-flight requests (30 second deadline)
3. Let in-progress autonomous curation finish its current record
4. Remove `server.json`
5. Release lock file
6. Exit 0

Uses Go's `http.Server.Shutdown(ctx)` for steps 1-2.

### Edge Cases

| Scenario | Behavior |
|----------|----------|
| Server crashes | Atomic writes protect store. CLI detects dead PID, cleans up, restarts. |
| Two stores, two servers | Each config dir has its own server.json. Different ports. |
| Port conflict (non-gramaton) | Server fails to bind, exits with error. CLI reports it. |
| Container SIGKILL | Same as crash. Next startup recovers cleanly. |
| Slow graph load | CLI polls /v1/status with backoff, times out after 10s. |

## Decided: Authentication

### Loopback (Topologies A, B1, C)

No authentication required. The OS enforces that only local processes
can reach 127.0.0.1. Same trust model as Ollama and Docker daemon.

### Network-Accessible (Topology B2)

When `bind: "0.0.0.0"`, the server generates a 256-bit cryptographically
random bearer token on first start:

```
~/.gramaton/auth_token    # 0o600 permissions, owner-read-only
```

Every request must include:
```
Authorization: Bearer <token>
```

The CLI reads the token from the local file automatically. Remote
clients configure the token manually (copy once).

Token regeneration: `gramaton serve --stop && rm ~/.gramaton/auth_token
&& gramaton serve` generates a new token.

If the server binds to a non-loopback address and no token file exists,
it generates one and logs a warning with the file location.

### TLS

Auth without TLS sends the token in plaintext. For network access:

- **Reverse proxy (recommended):** nginx/caddy/Tailscale handles TLS.
  Gramaton serves plain HTTP.
- **Built-in TLS:** Server accepts cert and key paths in config.

```yaml
server:
  port: 42982
  bind: "0.0.0.0"
  tls_cert: ""        # path to cert file (optional)
  tls_key: ""         # path to key file (optional)
```

### Scope

- `/v1/shutdown` is always restricted to loopback. If auth is
  enabled (non-loopback bind), shutdown also requires a valid token
  as defense in depth.
- Tokens are accepted only via the `Authorization` header, never
  via URL query string (query strings leak to logs and history).
- mTLS and OAuth are out of scope. Personal tool, single user.

## Security Considerations

### Critical

**SSRF via ingest local path mode.** The `POST /v1/ingest` endpoint
accepts a local file path. A remote attacker with a valid auth token
could read arbitrary server files via `{"path": "/etc/shadow"}`.

Mitigations (layered):
1. Local path mode is restricted to loopback requests only. Network
   clients must use upload mode (file content in the request body).
   The server checks the source address -- if non-loopback and the
   request uses `path` instead of file content, reject with 403.
2. Even from loopback, paths must be under explicitly configured
   allowed directories (`ingest_allowed_paths` in config). An agent
   tricked via prompt injection into ingesting `/etc/shadow` or
   `~/.ssh/id_ed25519` is blocked by the allowlist. Default: current
   working directory only.

**Token timing attacks.** Bearer token comparison must use
`crypto/subtle.ConstantTimeCompare` to prevent timing-based token
extraction.

**Request body DoS.** Without size limits, a client can send a
multi-GB request body and exhaust server memory.

Mitigation: `http.MaxBytesReader` on all endpoints, enforcing the
existing `MaxJSONSize` config limit. File uploads (ingest) get a
separate, larger configurable limit (default 50MB per file, 200MB
per bulk request).

**HTTP server timeouts.** Without read/write/idle timeouts, a
Slowloris attack can exhaust server goroutines by opening many
connections and sending data slowly. Go's default is no timeout.

Mitigation: configure `http.Server` timeouts:
```go
server := &http.Server{
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 30 * time.Second,
    IdleTimeout:  120 * time.Second,
}
```
Long-running operations (reembed, bulk ingest) need streaming
responses or a longer write timeout with per-handler override.

### High

**Auth token and API key leakage.** Bearer tokens, embedding provider
API keys, and LLM provider API keys must never appear in server logs,
error messages, or API responses. The server must redact `Authorization`
headers and credential values from all output.

**Token in URL query string.** Some clients might send `?token=xxx`
instead of the `Authorization` header. URL query strings appear in
access logs, proxy logs, and browser history. The server must only
accept tokens via the `Authorization` header and explicitly reject
query string auth with a clear error.

**Error verbosity.** API error responses must not contain stack
traces, internal file paths, or system information. Internal errors
return a generic message with an error code; diagnostic details go
to server-side logs only. Extend the path-stripping discipline from
the v0.1 `--file` security fix to all HTTP error responses.

**File and directory permissions.** All sensitive files must be 0o600,
config directory must be 0o700:
- `~/.gramaton/` directory -- 0o700 (owner-only listing and traversal)
- `server.json` -- 0o600 (if writable by attacker, redirects CLI
  to a malicious server)
- `auth_token` -- 0o600 (full store access if leaked)
- `gramaton.yaml` -- 0o600 (contains API keys for cloud providers)
- `gramaton init` should verify and fix directory permissions

### Medium

**Auto-start binary resolution.** The CLI starts `gramaton serve` as
a subprocess. Must use `os.Executable()` (resolved absolute path),
not `os.Args[0]` (could be relative or manipulated via PATH).

**Environment sanitization on auto-start.** The child server process
inherits the parent's full environment, including sensitive env vars
(cloud credentials, API keys for other services). The server should
only read env vars explicitly referenced in its config (via
`api_key_env` fields), not rely on ambient environment. Document
that the server inherits the spawning process's environment.

**LLM classification prompt injection.** With autonomous curation,
the server feeds record content to an LLM for classification. A
malicious record could attempt to manipulate classification output.

Mitigations:
- Feed content as a clearly delimited data block, not inline in the
  instruction
- Validate LLM output against the same enums and ranges used
  everywhere else (`validateEnum`, `validateFloat64Range`)
- A poisoned classification still has to pass all field validation

**X-Gramaton-Branch header validation.** Branch names from the HTTP
header must pass the same `validBranchName` sanitization as CLI
branch creation. Reject path traversal (`../../`), excessively long
strings, and special characters. Invalid branch headers return 400.

**server.json tampering.** If an attacker can overwrite `server.json`,
they could point the CLI to a malicious server. Mitigations:
- 0o600 file permissions
- CLI verifies PID is alive AND hits `GET /v1/status` to confirm the
  response identifies as Gramaton with a matching `store_dir`
- PID-alive check alone is insufficient due to PID reuse

**Ingest upload size.** Bulk uploads and large files need configurable
size limits to prevent resource exhaustion. Defaults: 50MB per file,
200MB per bulk request.

**Ingest filename sanitization.** Uploaded filenames are stored in
`source_ref`. Filenames must be sanitized via `filepath.Base()` to
strip directory components and prevent path traversal patterns from
entering stored metadata.

**Backup archive validation.** During restore, the archive validation
must check for HEAD at the root level (`data/HEAD` or `HEAD`), not
match any nested file named HEAD. Symlinks, hardlinks, and other
non-regular file types must be explicitly rejected during extraction
to prevent zip-slip variants.

**Per-field input validation.** All write endpoints (capture, update,
classify) must enforce the same bounds as search:
- Content length: MaxContentLength (default 1MB) on capture
- Keywords: max 100 count, max 256 chars per keyword
- String fields: summary_short (500), summary_abstract (5000),
  source_ref (2048), context fields (2048)
- Reembed batch: max 500
- Log limit: max 500
- Export top: max 10000

**Pre-embed-outside-lock discipline.** All handlers that call external
embedding providers must follow the gather-embed-apply pattern:
1. Read lock to gather data
2. Release lock, perform embedding I/O
3. Write lock to apply results

This applies to: capture, search, ingest, and reembed handlers.
Holding the write lock during external I/O blocks all readers and
writers, causing effective DoS.

**Response security headers.** Every HTTP response must include:
- `Content-Type: application/json` (prevents content-type sniffing)
- `X-Content-Type-Options: nosniff` (prevents browsers from guessing)
- `Cache-Control: no-store` (responses contain sensitive knowledge)

Applied via middleware to all endpoints.

**CORS policy.** Default: no CORS headers (deny all cross-origin
requests). Browsers block cross-origin requests without explicit
CORS headers. If a web UI is added later, CORS can be configured
via an allowlist. No wildcard CORS ever.

### Low

**Shutdown endpoint defense in depth.** `/v1/shutdown` is loopback-only.
As a belt-and-suspenders measure, if auth is enabled (non-loopback bind),
the shutdown endpoint also requires a valid bearer token. Protects
against bugs in the loopback check (IPv6 edge cases, proxy
`X-Forwarded-For` spoofing).

**Networked filesystem warning.** `flock` does not work reliably on
NFS or other networked filesystems. The server lock file assumes a
local filesystem. Running a store on NFS is unsupported and should
produce a warning on startup if detected.

### Known Risks (Cannot Fully Solve)

**Store content as prompt injection vector.** Records returned by
search are consumed by AI agents. A poisoned record could attempt
to hijack the agent's behavior. This is inherent to any knowledge
store for AI agents.

Mitigations (defense in depth, not prevention):
- Confidence, epistemic_status, and source metadata provide trust
  signals that agents can use to assess reliability
- `source_ref` and `testimony_hops` provide provenance tracking
- Ultimately the consuming agent decides what to trust
- This risk should be documented in the agent integration guide

**LLM provider privacy.** When using cloud LLM providers (OpenAI,
Anthropic, AWS) for autonomous curation, record content is sent to
third-party APIs. Users with sensitive data must be aware of this.
`gramaton init` should explicitly warn when a cloud LLM provider is
configured, and the config file should document which data leaves
the machine. Local providers (Ollama) keep all data on-device.

## Decided: MCP Integration

### Why MCP is High Priority

MCP is not just a protocol integration -- it solves the core usability
problem. Today, every capture, classify, and link operation goes through
the shell, triggering heredoc safety heuristics and requiring user
approval. MCP eliminates this entirely: agents call typed tools with
structured parameters. No shell, no escaping, no permission prompts.

### Transport Strategy

**Primary: Streamable HTTP** on the same server, same port.

The daemon serves MCP at `/mcp` alongside the REST API. No additional
process. No stdio reliability issues (stdout contamination, orphaned
processes, silent hangs). The server is already running -- MCP is just
another protocol on the same HTTP listener.

**Secondary: stdio bridge** for clients that only support stdio.

`gramaton mcp` is a thin stateless process that auto-starts the daemon
and translates stdio JSON-RPC to HTTP API calls. If it crashes, the
daemon keeps running. No state loss. The stdio reliability problems
(process lifecycle coupling, reconnection races) are mitigated because
the bridge is stateless.

```
MCP-native agents (Claude Code, Cursor):
  Agent <--Streamable HTTP--> gramaton server :42982/mcp

Stdio-only agents:
  Agent <--stdio--> gramaton mcp <--HTTP--> gramaton server :42982

REST API agents (OpenClaw, custom):
  Agent <--HTTP--> gramaton server :42982/v1/...
```

### MCP Tools (16)

All tools map to REST API endpoints. The MCP layer is thin protocol
translation, not business logic.

| Tool | Maps to | Description |
|------|---------|-------------|
| `gramaton_search` | `POST /v1/search` | Search the knowledge store (15+ filter/sort options) |
| `gramaton_capture` | `POST /v1/records` | Store a record |
| `gramaton_inspect` | `GET /v1/records/{id}` | Get full record details |
| `gramaton_update` | `PATCH /v1/records/{id}` | Update record properties |
| `gramaton_link` | `POST /v1/records/{id}/edges` | Create an edge between records |
| `gramaton_classify` | `POST /v1/records/{id}/classify` | Classify a pending record |
| `gramaton_explore` | `POST /v1/explore` | Graph traversal |
| `gramaton_pending` | `GET /v1/pending` | List unclassified records |
| `gramaton_status` | `GET /v1/status` | Health and stats |
| `gramaton_stats` | `GET /v1/stats` | Aggregate statistics (counts by type, confidence dist) |
| `gramaton_duplicates` | `POST /v1/duplicates` | Find near-duplicate records |
| `gramaton_branch` | `/v1/branches/*` | Create, checkout, merge, discard branches |
| `gramaton_diff` | `GET /v1/diff` | What changed since a date/topic |
| `gramaton_log` | `GET /v1/log` | Commit history, per-record history |
| `gramaton_reembed` | `POST /v1/reembed` | Regenerate stale embeddings |
| `gramaton_curation` | `GET /v1/curation` | View curation status or trigger a cycle |

Not exposed as MCP tools (CLI/API only):
- `revert` -- destructive, requires explicit user intent
- `ingest` -- complex (file uploads, bulk), better via CLI or API

### Agent Workflow Patterns

The dominant capture workflow is three MCP calls:

```
gramaton_capture(content="...", temporality="durable", ...)
  → returns {id: "01ABC..."}
gramaton_search(text="related topics")
  → returns related records
gramaton_link(id="01ABC...", target_id="01DEF...", edge_type="related_to")
```

Common read pattern is two calls:

```
gramaton_search(text="auth decisions")
  → scan results
gramaton_inspect(id="01ABC...")
  → full content for the relevant record
```

Curation is a tight loop of classify + search + link per record.

### Impact on Agent Integration

| Before (v0.1) | After (v0.2) |
|---------------|--------------|
| CLAUDE.md teaches how to call gramaton (heredocs, file flags, command syntax) | MCP handles mechanics; CLAUDE.md teaches when and why (behavioral guidance) |
| Shell permission prompts on every capture | MCP tools auto-allowed or approved once |
| Agent-specific instructions (Claude Code, Kiro, OpenClaw) | Universal MCP tools work across any MCP-compatible harness |
| Agents collapse Write + Bash into heredocs, triggering safety heuristics | No shell involvement at all |

### Go MCP Library

Use the official MCP Go SDK: `modelcontextprotocol/go-sdk`

- License: Apache-2.0 (enterprise-compatible)
- Maintained by Google engineers under the MCP GitHub org
- 4.3K stars, actively developed (last commit April 2026)
- Preferred over `mark3labs/mcp-go` (MIT, 8.5K stars) due to
  institutional backing and lower bus-factor risk

## Decided: Migration from v0.1

No breaking changes. Smooth upgrade path:

- **Store format:** v0.2 server reads the existing v0.1 store
  unchanged. Content-addressed storage, prolly trees, commit format
  -- all the same. No migration tool needed.
- **CLI surface:** identical commands, identical flags, identical
  output. Existing scripts and agent integrations work without
  changes. The only difference is that commands now delegate to the
  daemon instead of doing direct file I/O.
- **Agent prompts:** CLAUDE.md and subagent instructions continue to
  work via CLI. MCP is an additional (better) path, not a
  replacement. Agents can migrate to MCP tools at their own pace.
- **Configuration:** `gramaton.yaml` gains new server, LLM, and auth
  sections. Existing fields are unchanged. New fields have sensible
  defaults.
- **First run after upgrade:** first CLI command auto-starts the
  daemon. User sees the same output, slightly faster on subsequent
  calls. No ceremony, no "please run gramaton migrate."

## Decided: Implementation Plan

### Goal

v0.2 must be a complete, functional tool that proves Gramaton's value
against flat files and RAG. This is not a minimum viable product --
it is the full realization of the server architecture with every
feature needed for real-world comparison.

**What Gramaton must prove:**
- Epistemic metadata (confidence, temporality, status) produces
  better retrieval than raw vector similarity
- Knowledge graph relationships (edges, traversal, activation) surface
  context that chunked documents miss
- Curation pipeline means knowledge improves over time, not just
  accumulates
- Versioning provides provenance that flat files and RAG lack
- MCP integration makes the agent experience frictionless

Everything ships. The question is build order, not what to defer.

### Build Order

Sequenced by dependency -- each layer builds on the previous.

**Layer 1: Server core**

The daemon, HTTP listener, graph-level RWMutex, server lifecycle
(auto-start, idle timeout, graceful shutdown, PID/lock management).
This is the foundation everything else runs on.

- `gramaton serve` (foreground and background)
- Server info file and CLI discovery
- Health endpoint (`GET /v1/status`)
- Graph loaded into memory on startup, indexes rebuilt

**Layer 2: REST API -- all endpoints**

Every endpoint from the API spec, wired to the in-memory graph
engine. The existing CLI handler logic (runCapture, runSearch, etc.)
is refactored into HTTP handlers.

- Records: create, get, update, delete, classify, history, edges
- Search and explore
- Pending
- Branches: create, list, checkout, merge, discard
- History: log, diff
- Operations: revert, reembed, ingest (local + upload)

**Layer 3: CLI thin client**

Rewrite every CLI command to delegate to the server via HTTP.
Auto-start the daemon on first call. Same command surface, same
output format.

- All existing commands work identically
- `--file` flag still works (server reads from temp dir)
- Server discovery via server.json

**Layer 4: MCP**

Streamable HTTP endpoint on the daemon. Stdio bridge command.
13 MCP tools with typed JSON schemas. This is the usability
breakthrough -- agents interact without shell friction.

- `modelcontextprotocol/go-sdk` integration
- `/mcp` endpoint on the daemon
- `gramaton mcp` stdio bridge
- Tool definitions with input schemas
- Updated agent integration docs

**Layer 5: Additional embedding providers**

OpenAI-compatible and AWS Bedrock embedding providers. Provider
selection in config. Model migration via reembed.

- `embed/openai/` package (covers OpenAI, Azure, Mistral, Together)
- `embed/bedrock/` package
- `gramaton init` guided provider selection

**Layer 6: Autonomous curation**

Optional LLM provider config. Server-side classification on schedule.
Complement to piggyback and manual curation.

- LLM provider interface and implementations
- Classification prompt with injection resistance
- Configurable schedule (interval or pending threshold)
- LLM output validation against existing field constraints

**Layer 7: Auth and network access**

Bearer token generation, constant-time comparison, TLS support.
Enables Topology B2 (remote server).

- Auto-generated auth token for non-loopback bind
- `crypto/subtle` token comparison
- Built-in TLS (cert/key config)
- SSRF protection on ingest local path mode
- Request body size limits (`http.MaxBytesReader`)

### Testing Strategy

Each layer gets integration tests before moving to the next.
The v0.1 test suite continues to pass throughout -- the server
must be a transparent improvement, not a rewrite that breaks
things.

End-to-end validation after Layer 4: an agent session using MCP
tools to capture, search, classify, link, branch, diff, and
explore. This is the proof point.

### What This Enables

After all layers ship, Gramaton supports:

| Client | Access method |
|--------|---------------|
| Claude Code | MCP tools (Streamable HTTP) |
| Kiro | MCP tools (Streamable HTTP) |
| OpenClaw | REST API or MCP via mcporter |
| Custom agents | REST API |
| Power users | CLI (thin client to daemon) |
| Remote containers | REST API or MCP, auth + TLS |
| Curation | Autonomous (server), piggyback (agent), or manual (CLI) |
