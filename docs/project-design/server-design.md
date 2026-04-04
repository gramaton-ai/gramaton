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

#### System

| Method | Path        | Description    | Maps to CLI  |
|--------|-------------|----------------|--------------|
| GET    | /v1/status  | Health + stats | `status`     |

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

## Open Questions

### 1. Concurrency Model

How do simultaneous reads and writes interleave?

Options:
- **Graph-level RWLock:** One writer at a time, multiple concurrent
  readers. Simple, potentially contentious under high write load.
- **Branch-level locking:** Writes to different branches don't block
  each other. Reads never block. More granular, more complex.
- **Node-level locking:** Maximum concurrency, maximum complexity.
  Probably overkill for a personal knowledge store.

Considerations:
- Most workloads are read-heavy (search, inspect, explore)
- Writes are relatively infrequent (capture, update, classify)
- The store is personal (not multi-tenant), so contention is low
- Correctness matters more than throughput

### 2. Server Lifecycle

- **Port selection:** Fixed default port? Random with discovery?
  How to avoid conflicts with other gramaton instances?
- **PID management:** PID file location and format. How to detect
  stale PID files from crashed processes.
- **Auto-start mechanism:** How does the CLI start the server as a
  background process? `os/exec` with detach? Platform-specific
  (launchd, systemd)?
- **Idle timeout:** What counts as "idle"? No requests for N minutes?
  Configurable? Default value?
- **Graceful shutdown:** Finish in-flight requests, flush writes,
  then exit. Signal handling (SIGTERM, SIGINT).

### 3. CLI-to-Server Discovery

How does the CLI find the running server?

Options:
- **Port in config file:** Server writes its port to a known location.
  CLI reads it.
- **Lock file with port:** PID file includes port number.
- **Fixed default port:** Convention-based (e.g., 19876). Simple, but
  risks conflicts.
- **Environment variable:** `GRAMATON_PORT` or `GRAMATON_URL`.

### 4. Authentication

Needed for Topology B2 (network-accessible server). Not needed for
loopback-only access.

Options:
- **Bearer token:** Server generates a token on startup, writes it
  to a file. CLI reads and sends it. Remote clients configured manually.
- **API key:** User-configured, stored in gramaton config.
- **mTLS:** Certificate-based. Strong but complex setup.

For v0.2, bearer token generated on startup is likely sufficient.
API key for remote access. mTLS deferred.

### 5. Migration from v0.1

- Existing stores: v0.2 server reads the same store format. No
  migration needed for the data.
- Existing CLAUDE.md instructions: must be updated to use the new
  `--file` flag (already done) and eventually the HTTP API.
- Agent prompts: subagent-capture.md and subagent-curate.md already
  updated for `--file`. Server mode is transparent to agents using
  the CLI.
- The CLI command surface stays identical. Existing scripts and
  integrations work without changes.

### 6. MCP Integration

- Same server, separate endpoint (`/mcp`).
- MCP tools map to API endpoints (search, capture, inspect, etc.).
- MCP tool discovery: server advertises available tools via the
  MCP protocol.
- Question: does MCP support require a separate library/dependency?

### 7. Implementation Phasing

What is the minimum viable server? Suggested phases:

- **Phase 1:** Server with core CRUD (records, search, status).
  CLI thin client for those commands. Everything else stays direct.
- **Phase 2:** Remaining endpoints (branches, diff, log, ingest).
  Full CLI migration to thin client.
- **Phase 3:** MCP endpoint, additional embedding providers,
  autonomous curation.
- **Phase 4:** Auth, TLS, network access (Topology B2).
