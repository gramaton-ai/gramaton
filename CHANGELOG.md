# Changelog

All notable changes to Gramaton are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **On-demand server daemon** -- Gramaton now runs as an HTTP server
  that auto-starts on first CLI invocation and shuts down after idle
  timeout (default 30 min). Graph stays in memory for fast access.
  `gramaton serve` for explicit control (`--fg`, `--stop`).
- **Auto-supersession** -- when a new record is captured with high
  embedding similarity (>= dedup threshold) to an existing record,
  the server automatically sets `valid_until` on the old record and
  creates a `supersedes` edge. No agent involvement required.
- **MCP branch tool** -- all 5 branch operations (list, create,
  checkout, merge, discard) now work via MCP. Previously a stub.
- **Deterministic curation** -- background goroutine runs every 5
  minutes (configurable). Lifecycle transitions expire stale ephemeral/
  temporal records. Orphan linking creates `related_to` edges for
  records with zero connections. Duplicate consolidation auto-supersedes
  near-duplicate pairs. Concept candidate flagging identifies keywords
  above emergence threshold. Store manifest computes aggregate stats.
- **Autonomous LLM curation** -- when an LLM provider is configured
  (Anthropic API), the server classifies pending records, generates
  missing summaries, and promotes concept candidates. Rate-limited
  (default 20 LLM calls per cycle, batch size 10). Each mutation is
  atomic (no branches for routine classification).
- **LLM provider interface** -- `llm.Provider` with `Complete` and
  `ModelID`. Anthropic Messages API client as first implementation.
  Config supports env var name or direct API key.
- **Curation endpoints** -- `GET /v1/curation` (status, concept
  candidates, manifest) and `POST /v1/curation/trigger` (manual
  cycle). `gramaton_curation` MCP tool with status/trigger actions.
- **Enhanced curation status** -- response envelope now includes
  concept_candidates, stale_count, orphan_count, last_curated,
  and autonomous flag alongside pending_count and overdue.
- **Store manifest** -- computed each curation cycle: total records,
  edges, pending, orphans, stale, records by type, temporal range.
- `gramaton curation` CLI command with `--trigger` flag.
- **MCP diff and log tools** -- both now fully implemented. Previously
  stubs that returned error messages.
- CLI commands `stats` and `duplicates` for the new endpoints.
- **REST API** -- 24 HTTP endpoints for all operations (records CRUD,
  search, explore, branches, diff, log, revert, reembed, ingest,
  status). Standard response envelope with curation status and meta.
- **MCP integration** -- 13 MCP tools via Streamable HTTP at `/mcp`.
  Agents call `gramaton_search`, `gramaton_capture`, etc. as typed
  tools with no shell involvement. Uses official MCP Go SDK
  (Apache-2.0). Stdio bridge via `gramaton mcp` for legacy clients.
- **CLI thin client** -- all CLI commands now delegate to the server.
  Same commands, same output format, backward-compatible with v0.1
  agent prompts. Auto-starts the daemon transparently.
- **Core engine extraction** -- graph engine, indexes, and providers
  extracted into `core/` package with `sync.RWMutex` for concurrent
  read/write safety. Embedding calls happen outside the lock.
- **Server lifecycle** -- port 42982, PID file discovery, flock-based
  race protection, health check verification, graceful shutdown.
- `gramaton tempdir` command -- prints the OS-appropriate temp
  directory path for agent file input
- `--file`/`-f` flag on `capture`, `classify`, `update` -- reads
  JSON from a file instead of stdin
- Auto-cleanup of temp files after successful read, stale file sweep
  (1 hour) on each write command invocation
- v0.2 server design document with all architecture decisions resolved

- **Advanced search features** -- 15 new query capabilities:
  - Filter-only queries (search without text, returns all matching records)
  - Sort by created_at, last_accessed, access_count, confidence,
    importance, content_length, edge_count, staleness (asc/desc)
  - Importance range filter (importance_min/max)
  - Negation on enum filters (prefix with `!`, e.g. `!ephemeral`)
  - Missing field detection (`missing: ["temporality"]` finds unclassified)
  - Keyword exact match (tag-based lookup, all specified must be present)
  - Access-based queries (last_accessed_before/after, access_count_min/max)
  - Validity window queries (valid_before/after, expires_before/after)
  - Full-text substring search (`match` param, case-insensitive, distinct
    from vector similarity)
  - Record-to-record similarity (`similar_to` with record ID, uses stored
    embedding)
  - Random/sample mode (partial Fisher-Yates, optionally filtered)
  - Faceted counts (per-field breakdowns alongside results)
  - Orphan/connectedness queries (min_edges/max_edges filter, edge_count sort)
  - Staleness detection (computed score from temporality + age + access)
- **Aggregates endpoint** -- `GET /v1/stats` and `gramaton_stats` MCP tool.
  Returns counts by temporality, knowledge_type, epistemic_status, and
  confidence distribution across the store.
- **Near-duplicate detection** -- `POST /v1/duplicates` and
  `gramaton_duplicates` MCP tool. Pairwise embedding similarity scan,
  configurable threshold and max pairs.
- **Keyword index** -- PropertyIndex now indexes individual tokens from
  StringList properties for exact tag lookup via `LookupKeyword`.
- **NodesWithKey** -- PropertyIndex method returns all node IDs that have
  a given property, enabling missing-field queries.
- Search results now include created_at, access_count, importance,
  content_length, edge_count, and staleness fields.

### Changed

- **CLI tenet 10 refactor** -- CLI package now imports only config,
  core, embed, and server (was 9 internal packages). Deleted duplicate
  engine.go (300 lines), dead curation.go, dead atomic.go. Ingest
  command rewritten to use server API instead of direct graph
  manipulation. Ollama setup extracted to embed/setup.go. Dead branch
  helpers removed.
- Search pre-embeds query text outside the lock to avoid blocking
  the server during Ollama model load
- Capture pre-embeds content outside the lock for the same reason
- Server does not auto-start Ollama -- connects to whatever
  embedding provider is configured

### Security

- Restrict `--file` to gramaton temp directory only, reject arbitrary
  paths
- Resolve symlinks before path validation to prevent symlink-based
  escapes
- Read via file descriptor to avoid TOCTOU between validation and
  read
- Verify input is a regular file (not device, pipe, or symlink)
- Strip absolute paths from error messages to prevent info disclosure
- Sweep removes symlinks unconditionally from temp directory
- HTTP server timeouts (read 10s, write 120s, idle 120s)
- Security headers on all REST responses (Content-Type, nosniff,
  no-store)
- MCP endpoint excluded from REST security headers (own content
  negotiation)
- Search input bounds: top capped at 1000, keywords at 100, missing
  fields at 50, match string at 1024 bytes, explore depth at 10,
  edge types at 50, duplicate pairs at 1000
- Float64 range validation on all confidence/importance parameters (0-1)
- Integer sign validation on access_count and edge count parameters
- Duplicate threshold validated 0-1 with safe default
- Error messages no longer echo raw user input (date, sort, order)
- Random mode uses partial Fisher-Yates (O(k) not O(n)) to prevent
  CPU exhaustion on large candidate sets
- Vector search bounded to 3x top results instead of full candidate set
- Log limit capped at 500, commit traversal depth at 5000
- Diff topic parameter length capped at 1024
- Branch name validation no longer echoes invalid characters
- Chunking no longer embeds under the write lock. Pre-embed and
  pre-chunk outside the lock, apply under lock (same fix applied
  to capture, MCP capture, and ingest). Eliminates server deadlock
  when content exceeds chunk threshold and Ollama is slow to respond.
- Ingest pre-embeds all files outside the lock in one batch instead
  of embedding each file under the write lock sequentially

## [0.1.0] - 2026-04-04

First working release. CLI-driven knowledge store for AI agents with
property graph storage, vector search, and versioned persistence.

### Added

- **Graph engine** -- property graph with 8 typed properties (String,
  Float64, Int64, Bool, Timestamp, Vector, StringList, Bytes), ULID node
  and edge IDs, safe property accessors
- **Content-addressed storage** -- SHA-256 hashing, atomic writes via
  temp+rename, hex-validated paths, 0o700 directory permissions
- **Prolly trees** -- probabilistic B-trees with FNV-1a rolling hash for
  content-defined chunk boundaries, configurable chunk size and split
  probability, O(changes) commit diffs with subtree skipping
- **Versioned commits** -- v0 (flat hash lists) and v1 (prolly tree roots)
  format, backward-compatible loading, per-record history via
  NodeHashInCommit (O(log N))
- **Property index** -- exact match (serialized value map), range queries
  (sorted slice + binary search), substring search (case-insensitive
  Contains)
- **Vector index** -- flat index with cosine similarity, candidate set
  filtering, keyed by node ID
- **Search** -- 6-factor scoring model (similarity, recency, freshness,
  frequency, activation, confidence), metadata filtering (temporality,
  knowledge type, epistemic status, confidence range, date range),
  configurable result count
- **Spreading activation** -- access recording increments access_count and
  updates last_accessed, one-hop neighbor boosting with configurable base
  amount and attenuation factor
- **Document chunking** -- configurable threshold, chunk size, and overlap,
  word-boundary-aware splitting, parent-child edge linking
- **Dedup detection** -- vector similarity threshold check before capture
- **Embedding** -- Ollama provider with HTTP client (60s timeout),
  auto-start via lifecycle management, auto-model-pull, guided init with
  platform-specific install instructions
- **Branching** -- create, list, checkout, merge (fast-forward), discard,
  branch name sanitization
- **CLI commands** -- capture, search, inspect, explore, update, delete,
  classify, pending, ingest, reembed, branch, diff, log, revert, status,
  init
- **Diff** -- commit-level diff with prolly tree subtree skipping, --since
  and --topic flags, short hash resolution
- **Per-record log** -- track property changes across commits for a
  specific record, showing from/to diffs
- **Agent integration** -- Claude Code CLAUDE.md template, capture and
  curation subagent prompts, Kiro skill definitions, custom agent
  integration guide
- **Piggyback curation** -- curation status (pending_count, overdue)
  appended to search/inspect/explore responses, triggers agent-driven
  classification of pending records
- **Security hardening** -- input validation with LimitReader, BOM
  stripping, UTF-8 validation, null byte rejection, allocation guards
  (10M element max), symlink protection on ingest, 0o600 file permissions
- **Configuration** -- YAML config with all design doc defaults, prolly
  tree tuning parameters, activation settings, storage paths

[Unreleased]: https://github.com/brandonlattin/gramaton/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/brandonlattin/gramaton/releases/tag/v0.1.0
