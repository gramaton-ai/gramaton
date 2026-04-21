# Changelog

All notable changes to Gramaton are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Bulk collection_add (P1-78)** -- new MCP tool
  `gramaton_collection_add_batch` and HTTP route
  `POST /v1/collections/{id}/items/batch` add up to 500 items in a
  single call. Batches schema validation + embedding + save into one
  engine lock cycle; ~20-50x faster than repeated single adds for
  N=100. Best-effort semantics: per-item validation and dedup
  failures reported in the Failed array, items passing pre-checks
  commit atomically. Intra-batch dedup uses first-write-wins.
  `CollectionAddBatchDescription` constant shared across HTTP,
  MCP, and CLI proxy transports.
- **LLM contributor skills** -- `.claude/skills/` ships agent skills
  encoding project conventions for LLM coding assistants:
  `new-operation`, `migrate-to-api`, `pre-merge-check`,
  `gramaton-review`, `gramaton-security-review`, `store-health`,
  `curation-sweep`, `benchmark-extract`. Claude Code auto-discovers
  them; other tools reading agent-skill markdown can adapt. Skills cite CONTRIBUTING.md
  as the source of truth rather than duplicating conventions. Added
  `CLAUDE.md` at the repo root as a short skill index and governance
  reference. `.gitignore` narrowed from a blanket `.claude/` rule to
  a default-deny whitelist: only `.claude/skills/` is shared; user-
  local settings and session state stay out of the repo.
- **HNSW vector index** -- pure Go Hierarchical Navigable Small World
  implementation behind the existing VectorIndex interface. O(log N)
  approximate nearest neighbor search replaces O(N) brute-force at
  scale. Dynamic switching: FlatIndex (exact) below `hnsw_threshold`
  (default 5000), HNSW above. Candidate-filtered queries fall back to
  flat scan on small candidate sets. Configurable M, efConstruction,
  efSearch. 1.00 recall@10 at 1000 vectors with default parameters.
- **Persisted BM25 index** -- BM25 term frequencies serialized as a
  content-addressed chunk alongside each commit (`bm25_root` field).
  On startup, the index loads from disk instead of re-tokenizing all
  content. Saves 10-30 seconds at 100K+ nodes. Backward compatible:
  old commits without `bm25_root` fall back to full rebuild.
- **Dirty tracking** -- graph tracks which nodes/edges are modified
  since the last save. `Save()` only marshals dirty items (O(K)
  instead of O(N) where K is typically 1-5 per mutation).
- **Incremental prolly tree** -- `ProllyTree.Update()` applies
  mutations by walking from root to affected leaves, modifying only
  touched chunks. O(K * depth) where depth is 3-4 for trees up to
  millions of entries. Falls back to full `Build()` for first save
  or branch switch.
- **Debounced access saves** -- `serviceInspect` and `serviceSearch`
  no longer trigger a full save to persist access_count. Access
  metadata is flushed by a background goroutine every 30 seconds
  and on shutdown. Eliminates 80-90% of unnecessary full saves.
- **--version flag** -- `gramaton --version` and `gramaton version`
  subcommand with commit hash, build date, and store format version.
  Version set via ldflags at build time. Replaces all hardcoded
  version strings.
- **Store format versioning** -- `FORMAT` file in the data directory
  tracks the on-disk store format version. Checked on engine load:
  stores from newer binaries are rejected with a clear upgrade
  message. Backward compatible: missing FORMAT file gets current
  version written automatically.
- **Collections** -- structured containers with schema enforcement and
  guaranteed exhaustive retrieval. Collections complement the knowledge
  graph for data that needs complete visibility (tasks, backlogs,
  checklists). Features: named collections with optional schema (6 field
  types: string, number, boolean, date, enum, enum[]), schema evolution
  with explicit migration, multi-collection membership, retirement
  (reversible), title-based dedup detection, field-based sorting.
  11 MCP tools (`gramaton_collection_*`) and 12 HTTP endpoints.
- **Integrator guide** -- documentation for agent builders covering
  graph vs. collection usage, capture/search best practices, collection
  patterns (PARA, kanban), schema definitions, and prompt guidance.
- **Structured metadata** -- records accept a `meta` map at capture
  and update time. Values (string, number, bool, string array) are
  stored as typed `meta.*` properties, validated at write time, and
  indexed in BM25 for keyword search. Search accepts a `meta` filter
  for exact-match pre-filtering on any meta field.
- **Faceted suggestions** -- when search results score below a
  configurable threshold (default 0.75), the response includes
  `suggestions.available_filters` showing meta field values found
  across results. Agents can use these to refine with explicit
  meta filters. Config: `search.suggestion_threshold`.
- **ACT-R scoring model** -- replaced 6-signal scoring (similarity,
  recency, freshness, frequency, activation, confidence) with 4-signal
  model (similarity, freshness, ACT-R activation, confidence). The
  ACT-R base-level activation `B = ln(n/0.5) - 0.5*ln(L)` unifies
  frequency and recency into a single theoretically-grounded signal
  based on Anderson & Schooler 1991. Spreading activation from
  neighbors is additive (A = B + S), matching ACT-R's full equation.
  Frequency signal removed (eval showed it was actively harmful).
  Default weights: similarity=0.55, activation=0.20, confidence=0.15,
  freshness=0.10.
- **Default embedding model changed** -- switched from `nomic-embed-text`
  (137M params, 768d) to `mxbai-embed-large` (335M params, 1024d).
  Eval showed +14% NDCG@5 and +12% MAP. Run `gramaton reembed` after
  upgrading to re-embed existing records.
- **Bedrock provider support** -- added Amazon Bedrock as a provider for
  both embeddings and LLM. Embedding supports Titan Embed V2 and Cohere
  Embed model families. LLM uses the Converse API (works with Claude,
  Titan, Llama, Mistral, and any Bedrock model). Auth via AWS named
  profiles, environment variables, or the default credential chain.
  Config: `embedding.provider: bedrock` / `llm.provider: bedrock`.
- **OpenAI-compatible provider** -- added OpenAI-compatible provider for
  both embeddings (`/v1/embeddings`) and LLM (`/v1/chat/completions`).
  Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together, Fireworks,
  and any compatible API. API key is optional (local servers often don't
  need one). Config: `embedding.provider: openai` / `llm.provider: openai`.

### Removed

- **docs/project-design sunset pass.** Removed nine design docs
  under `docs/project-design/` that described superseded pipelines
  or were duplicates of top-level `docs/` content:
  `server-design.md` (pre-T-02 "v0.2" daemon spec, superseded by
  the `api/` unified surface); `observe-pipeline.md` (the
  `gramaton_observe` flow, soft-deprecated and replaced by the
  session prepare/commit two-tier model); `capture-and-processing.md`
  and `curation.md` (early subagent-classification-at-capture model
  with `/gramaton-process` and `/gramaton-curate` skills that were
  never built -- current reality is server-side autonomous curation
  plus agent piggyback fallback); `validation.md` (theoretical
  test methodology superseded by the LongMemEval benchmark work in
  `gramaton-inspection/benchmark-methodology.md`);
  `open-questions.md` (all items marked RESOLVED);
  `agent-integration.md` (taught the observe/subagent integration
  pattern, now outdated -- live integration guidance lives in the
  agent's `CLAUDE.md` and the top-level `docs/integrator-guide.md`);
  `tenets.md` and `architecture.md` (duplicates of the top-level
  `docs/tenets.md` and `docs/architecture.md`). `project-design/README.md`
  rewritten as a leaner index over the remaining 9 docs (data-model,
  retrieval, embedding, collections, foundations, case-studies,
  data-integrity, design-decisions, glossary). Git history preserves
  the removed files for recall. Phase-2 rewrites of the top-level
  `docs/architecture.md`, `integrator-guide.md`, and root `README.md`
  (which still reference the removed `gramaton_observe` tool) are
  tracked in `gramaton-inspection/documentation-plan.md`.
- **T-02 cascade cleanup stage C3 (collections).** Deleted
  `server/service_collections.go` (928 lines) and
  `server/service_collections_test.go` (967 lines), plus the
  duplicate `server/collections.go` (289 lines) whose schema
  types and field-type constants already live in
  `api/collection_schema.go`. All 12 server-level collection
  methods were unreachable via bindings after T-02 routed
  `bindings_collections.go` through `s.api.CollectionXxx`.
  Coverage ported to `api/collections_test.go`: 27 tests over
  Create/List/Items/Add/Remove/Update/Move/Rename/Delete/
  SchemaUpdate/Migrate, including schema validation, projection
  + filter edge cases, multi-membership, dedup, and schema
  evolution. Two tests pinned T-02 behavior changes: duplicate
  name and duplicate title now surface as `conflict` rather
  than `duplicate`-code or success-with-`duplicate=true` maps.
  The HTTP-boundary test (`TestCollectionItemsHTTPProjectionAndFilter`)
  stayed in `bindings_collections_test.go`; the
  `TestCollectionPerformance` test was preserved too.
- **T-02 cascade cleanup stage C2 (records).** Removed seven dead
  server-level record services from `server/service_records.go`
  (`serviceInspect`, `serviceUpdate`, `serviceClassify`,
  `serviceResolve`, `serviceLink`, `serviceDeleteEdge`,
  `serviceDeleteRecord`), their supporting types
  (`updateRequest`, `classifyRequest`, `resolveRequest`,
  `edgeRequest`) and validators (`validateUpdateRequest`,
  `validateClassifyRequest`) in `handler_records.go`, and the
  duplicate `inspectMetadataSummary` helper (live copy lives in
  `api/internal.go`). Coverage ported to new
  `api/records_test.go`: 10 tests over api.Inspect, api.Update,
  api.Classify, api.Resolve, api.Link, api.Unlink, and
  api.DeleteRecord. Kept `serviceCapture` + its helpers
  (`preEmbedContent`, `applyPreEmbedded`, `validateCaptureRequest`,
  `setOptionalProps`) since `handler_intake.go`'s `serviceIntake`
  still calls through to them; five capture tests retained in
  `service_records_test.go`. Also dropped four consts in
  `server/validation.go` that became unused after `serviceSearch`
  went in stage C1: `maxSearchTop`, `maxMissingFields`,
  `maxMatchLength`, `maxSearchHops`.
- **T-02 cascade cleanup stage C1 (search + test rewires).** Deleted
  `server/service_search.go` (the last method `serviceSearch` was
  unreachable via bindings since T-02 routed `bindings_search.go`
  through `s.api.Search`). Rewired the one test reference in
  `server/handler_sessions_test.go` to `srv.api.Search`. Also
  rewired `bindings_collections_test.go` helpers (`makeCollection`,
  dedup test seed) from `srv.serviceCollection{Create,Add}` to
  `srv.api.Collection{Create,Add}`. Deleted the unused `searchRequest`
  type in `server/handler_search.go`; kept `parseDateArg` (used by
  remaining server-level services).
- **T-02 cascade cleanup stage B (sessions).** Deleted
  `server/service_sessions.go` (1255 lines) and
  `server/service_sessions_test.go` (1659 lines): the server-level
  session subsystem was fully dead after T-02 routed
  `bindings_sessions.go` through `s.api.SessionXxx`. Tests ported to
  `api/sessions_test.go` (47 tests, package `api`) using a new
  `setupTestAPI` helper. Also removed the `preparedSessions` and
  `preparedSweepCancel` fields from `server.Server` (now owned end-
  to-end by `api.API`), and the three internal api helpers
  (`sessionAddTopic`, `sessionAddSegment`, `sessionUpdateSegmentCapture`)
  which had no callers after the helper-path tests were retired.
  `server/handler_sessions_test.go` helper `createSessionWithSegments`
  rewired to `srv.api.SessionStart/Prepare/Commit`. Test coverage for
  the session subsystem is now preserved at the api layer, where the
  logic actually lives. Full server + api suites pass with `-race`.
  Dropped: seven tests that exercised internal AddTopic/AddSegment/
  UpdateSegmentCapture helpers (not user-facing); ReadArchive tests
  (the function was never wired to HTTP/MCP and was flagged as a
  latent footgun in the T-02 security review).
- **T-02 cascade cleanup stage A (isolated orphans).** Deleted the
  three T-02 service orphans that were safely isolated (no test
  references): `serviceExplore` (`server/service_search.go`) and
  its `exploreRequest` type (`server/handler_search.go`);
  `servicePending` (entire `server/service_ops.go` file, 46 lines);
  `serviceCollectionSchemaRead` (`server/service_collections.go`).
  Also removed the consts in `server/validation.go` that became
  unused after the function deletions: `maxExploreDepth`,
  `maxEdgeTypes`, `maxExploreNodes`, plus the already-orphaned
  `maxDuplicatePairs`, `maxLogTraversal`, `maxTopicLength`,
  `maxFactLen`, `maxReembedBatch`. Build and full server test
  suite pass unchanged. Stage B (port
  `server/service_sessions_test.go` to `api/sessions_test.go`
  before deleting `server/service_sessions.go`) and stage C
  (records/search/collections equivalents) tracked as separate
  tasks.
- **T-02 dead code cleanup (server handlers + mcp wrappers).** Deleted
  941 lines across 8 files that were orphaned by the T-02 api/
  migration: `server/mcp_records.go`, `server/mcp_collections.go`
  (both entirely unreachable; `registerMCPTools` in `server/mcp.go`
  routes exclusively through the `bindings_*.go` path), plus
  `server/handler_sessions.go` and `server/handler_collections.go`
  (all contained only pre-T02 HTTP handlers with no live callers).
  Surgical deletes from `server/handler_records.go` (8 dead
  `handle*Record` / `handle*Edge` methods; kept the captureRequest/
  updateRequest/classifyRequest/resolveRequest/edgeRequest types and
  the live helpers `preEmbedContent`, `applyPreEmbedded`,
  `validateCaptureRequest`, `validateUpdateRequest`,
  `validateClassifyRequest`, `setOptionalProps`,
  `inspectMetadataSummary` used by `service_records.go`),
  `server/handler_search.go` (`handleSearch`, `handleExplore`; kept
  `parseDateArg` used by `service_search.go`),
  `server/handler_ops.go` (`handlePending`; kept `handleRevert`,
  `handleIngest`, ingest helpers), and `server/handlers.go`
  (`handleStatus`; kept `handleHealth`, `handleShutdown`,
  `handleDebugGoroutines`, `handleLLMStats`, `isLoopback`,
  `loopbackOnly`). No behavior change -- routing was already going
  through `bindings_*.go` paths. Follow-up candidates: the cascade
  of newly-exposed orphans (`serviceExplore`, `servicePending`,
  `serviceCollectionSchemaRead`, `startPreparedSweeper`, unused
  constants in `server/validation.go`) will need their own pass.

### Changed

- **Pure-Go BERT is the official default embedding provider.** The
  `config.Defaults()` return value (`embedding.provider: "bert"`,
  `model: "bge-small-en-v1.5"`) already pointed at BERT, and
  `embed/setup.go` already tried BERT first with Ollama as fallback.
  This change aligns the user-facing story with that reality: the
  README Quick Start no longer instructs users to install Ollama
  (Gramaton downloads the BERT model itself on first run, ~130MB,
  no external runtime); the Features bullet lists BERT as default
  and Ollama as an alternative local option; the Configuration
  sample drops the explicit Ollama block and notes that an
  `embedding:` section is only needed to override the BERT default.
  `cli/init.go`'s `printNoOllama` fallback message was rewritten as
  `printEmbeddingSetupFailed`: when setup fails it now names the
  BERT-download path as the most likely cause (network), and lists
  Ollama, OpenAI, and Bedrock as alternatives rather than
  recommending Ollama. Known limitation: `embed/bert/matmul_amd64.go`
  still falls back to pure Go -- the AVX2 kernel TODO from
  `matmul_amd64.go:3` is real and tracked in the Gramaton development
  collection. Apple Silicon has the arm64 NEON kernel and is
  unaffected.
- **`README.md` rewritten for the post-T-02 surface.** Added a "Why
  Gramaton" section motivating the project against built-in agent
  memory and generic vector stores (without naming them) and listing
  the concrete questions the system is designed to answer. Added a
  new "Three Ways to Store Knowledge" section covering the Memory /
  Sessions / Collections split with the decision rule ("will missing
  one item be a failure?"). Rewrote the MCP tool table as five
  grouped clusters (Records, Search & discovery, Sessions,
  Collections, History & admin) matching the 38 tools actually
  registered by `server/mcp.go`. Dropped the stale `gramaton_observe`
  entry (the tool is no longer registered). Corrected the embedding
  provider list to include pure-Go BERT (`embed/bert/`). Corrected
  the feature bullet on concept emergence to reflect that candidate
  *promotion* is LLM-gated while candidate *detection* and existing-
  concept enrichment are always-on. Fixed the architecture diagram
  to show the `api/` canonical operations layer between transports
  and the engine. Added the named-store feature bullet. Added a
  CLI-reference entry for `gramaton mcp` (stdio proxy) and a
  Documentation-table entry for `CONTRIBUTING.md` and
  `docs/benchmarks.md`. Every feature claim verified against current
  code before landing. Phase 2 of the documentation consolidation
  tracked in `gramaton-inspection/documentation-plan.md`.
- **`gramaton_search` tool description rewritten with trigger-led framing.**
  The prior description was mechanical (`"Search Memory and Sessions.
  Returns results ranked by composite score..."`) and said nothing about
  when agents should call it. Observed failure mode (2026-04-20): agents
  write architecture answers, design docs, and competitive claims from
  general knowledge without first checking the store for project-specific
  prior thinking -- losing decisions that were already made. New
  description leads with "call BEFORE producing content that references
  project state" and lists concrete triggers: architecture questions,
  design/methodology writing, user references to prior sessions,
  reasoning where project-specific prior art may exist. Same pattern as
  the recent `session_prepare` rewrite. Shipped through the existing
  `api.SearchDescription` constant (no refactor needed; constant was
  already in place).
- **Session tool descriptions migrated to `api.SessionXxxDescription`
  constants.** `api/sessions.go` now defines `SessionStartDescription`,
  `SessionGetDescription`, `SessionPrepareDescription`, and
  `SessionCommitDescription` alongside the Request/Response types. Both
  MCP transports (`server/bindings_sessions.go` and
  `cli/mcp_proxy_sessions.go`) reference the constants instead of
  duplicating literal strings. Aligns with the existing convention used
  by capture + collections; structurally prevents the two MCP surfaces
  from drifting on help text. Also deleted `server/mcp_sessions.go`
  (dead code since the T-02 api/ migration -- `registerMCPTools` in
  `server/mcp.go` routes via `registerSessionsMCPTools` in bindings, not
  the older `registerMCPSessionTools`).
- **Leaner `session_prepare` extraction prompt.** The prompt returned
  by `gramaton_session_prepare` dropped from 202 lines (~9360 bytes)
  to 51 lines (~2525 bytes). Detailed content (field-role framework,
  classification heuristics per axis, question-type mapping, two-tier
  semantics) has been delegated to `gramaton_guide(topic="capture")`,
  `(topic="metadata")`, and `(topic="sessions")` -- the topics already
  carry the same material as the authoritative reference. The prompt
  retains the must-haves: the submission tool name, the full segment
  field list, the four core principles (synthesize-not-summarize,
  capture-don't-suppress, prospective-findability, skip-only-low-
  value), and explicit guide pointers. Per-call cognitive load on the
  LLM drops by ~73%; extraction quality shouldn't change because the
  reference material is now one guide call away. Updated in both
  embedded prompt files (`api/prompts/extraction.md`,
  `server/prompts/extraction.md`). Test
  `TestPrepareReturnsExtractionPromptWithSections` updated to assert
  the new contract (field names + principles + guide pointers),
  replacing the old per-section checklist.
- **`benchmark-extract` skill sub-agent contract refined.** Updated
  the sub-agent template in `.claude/skills/benchmark-extract/SKILL.md`
  to reflect the actual flow used during the 2026-04-20 pilot: read
  transcript from a staging file path (not inline in the prompt), call
  `session_start` first to get the ULID, then `prepare` + `commit` with
  the ULID (not the client_session_id). These details were learned
  empirically and the skill now matches what works end-to-end.
- **`gramaton_session_prepare` tool description rewritten for higher
  autonomous-trigger rate.** The prior description led with compaction
  and buried the actual triggers as "also call at natural breakpoints,"
  producing a regression vs the old `gramaton_observe` self-trigger
  cadence. New description leads with "EAGERLY throughout a conversation,
  not just at the end," names the rule-articulation trigger explicitly,
  adds a ~10-substantive-turns floor even without an explicit trigger,
  and flags bundling-at-session-end as an anti-pattern. Synced across
  the three description sites (`server/mcp_sessions.go`,
  `server/bindings_sessions.go`, `cli/mcp_proxy_sessions.go`).
  Corresponding "When to Call" guidance added to
  `server/guide/sessions.md` and `server/guide/capture.md`, and the
  shipped `integration/claude-code/CLAUDE.md` template was updated
  with the same trigger list + scheduled cadence + anti-pattern
  callout. Behavior change: LLM agents using the MCP surface should
  call prepare/commit more frequently during real-dev conversations.
- **Engine god-object split (P2-01)** -- `core/engine.go` reorganised
  into named subsystems and sibling pipeline packages across two PRs.
  PR1 extracted `providers`, `searcher`, and `indexSet` subsystems
  (engine shrank 1207 -> 952 LOC); `indexSet.applyToNode` consolidates
  cross-index updates so future indexes are picked up automatically
  rather than via edits to every node-creation path. PR2 extracted
  `chunking` and `dedup` to sibling packages (engine shrank 952 -> 564
  LOC); `core.Engine.PreChunk`, `ApplyChunks`, `CheckDedup`, and
  `IsContextLengthError` now delegate. `core.PreChunkResult` is a
  type alias for `chunking.Result` so existing callers compile
  unchanged. No public API signatures changed.
- **Server service layer extraction** -- extracted 14 service methods from
  HTTP handlers into `service_records.go`, `service_search.go`,
  `service_ops.go`. Both HTTP handlers and MCP tools now delegate to the
  same service layer, eliminating ~800 lines of duplicated business logic.
  Split `mcp.go` (1359 lines) into 5 focused files (`mcp.go` 66 lines,
  `mcp_search.go`, `mcp_records.go`, `mcp_ops.go`, `mcp_admin.go`).

### Fixed

- **MCP capture missing resolution on supersede** -- auto-supersession via
  MCP now sets `resolution` and `resolved_at` on the old record, matching
  HTTP handler behavior.
- **MCP inspect missing edge_id** -- inspect results via MCP now include
  `edge_id` in related entries, matching HTTP handler behavior.
- **MCP reembed blocking under lock** -- reembed via MCP now uses 3-phase
  approach (read lock to identify stale nodes, embed outside lock, write
  lock to apply). Previously held write lock during all embedding I/O.
- **MCP observe skipping validation** -- observe via MCP now validates
  message counts (max 100), content lengths (50KB), fact lengths (10KB),
  and message roles, matching HTTP handler behavior.
- **Ingest per-file content length** -- ingest handler now validates
  per-file content length before pre-embedding.
- **CLI url.PathEscape** -- added `url.PathEscape` to 6 CLI commands
  (inspect, classify, delete, update, resolve) to prevent path injection
  if record ID formats ever change.
- **Dead code removal** -- removed 4 unused validation functions from
  `cli/input.go`.

### Changed (continued)

- **Structural refactoring** -- `Engine.IndexNode` centralizes PropIdx +
  BM25 + VecIdx sync after node creation, replacing 10 copy-paste sites.
  `Engine.SetContentProp` refreshes BM25 on content changes. Shared
  `graph.IsStructuralEdge/IsStructuralChild/SemanticEdgeCount` replace
  5 duplicate `isChunkNode` and 2 duplicate edge-count functions.
  `search.computeSimilarities` extracted from 250-line monolith. Fixes
  missing BM25 indexing in ingest and import handlers, and stale BM25
  when curation updates content properties.
- **Scoring weight rebalance** -- Similarity weight increased from 0.35
  to 0.50, confidence reduced from 0.20 to 0.15, recency/freshness
  reduced from 0.15 to 0.10 each, frequency halved from 0.10 to 0.05,
  activation doubled from 0.05 to 0.10. Validated against 672 records
  across 6 content types (personal, academic, news, conversations,
  technical, reference) using NDCG@5 and MAP metrics. Fixes issue where
  high-confidence frequently-accessed records dominated search results
  regardless of topical relevance.

### Added

- **BM25 hybrid search with RRF fusion** -- BM25 keyword index runs
  alongside vector similarity. Results fused with Reciprocal Rank
  Fusion (RRF). Solves multi-concept query degradation where queries
  like "consciousness and memory" previously returned only single-
  concept results. Config: `search.bm25_k1`, `search.bm25_b`,
  `search.rrf_k`.
- **Jaccard dedup guard** -- auto-supersession at cosine >= 0.92 now
  requires Jaccard word-overlap >= 0.3 on content text. Prevents
  false positives on long documents with similar structure but
  different content (e.g., SEP philosophy articles).
- **Cross-section semantic linking** -- deterministic curation finds
  similar sections across different parent documents and creates
  `related_to` edges. Sibling exclusion prevents linking sections
  from the same article. Config: `curation.section_link_min`,
  `curation.max_section_links_per_run`.
- **RAPTOR-inspired concept node creation** -- autonomous curation
  converts keyword-based concept candidates into searchable concept
  nodes with LLM-generated summaries, embeddings, and `instance_of`
  edges from member records. Concept nodes act as retrieval hubs for
  collapsed-tree search. Config: `llm_curation.max_concepts_per_run`.
- **Section summary generation** -- autonomous curation detects section
  nodes with truncated summaries and generates proper LLM summaries.
- **Query decomposition fallback** -- `DecomposeQuery` splits multi-
  concept queries into sub-queries via LLM. `MergeResults` combines
  with RRF. `ShouldDecompose` triggers on low-confidence initial results.
- **Parallel LLM calls in curation** -- `classifyPending` and
  `generateSummaries` use a 4-worker pool for concurrent LLM calls.
  Concept node embedding moved outside the write lock.
- **Edge deletion endpoint** -- `DELETE /v1/edges/{edge_id}` for
  removing edges. Edge IDs now included in inspect response.
- **MCP proxy tools** -- `gramaton_unlink` (edge deletion) and
  `gramaton_history` (per-record change history). `gramaton_delete`
  intentionally excluded from MCP (destructive operations require
  CLI or HTTP API).
- **valid_until clearing** -- `PATCH /v1/records/{id}` with
  `valid_until: "clear"` removes valid_until, resolution, and
  resolved_at properties (reversing false supersession).
- **Multi-concept eval queries** -- 18 queries (8 personal, 10 SEP)
  targeting cross-concept retrieval for hybrid search validation.
- **Retrieval quality evaluation framework** (`eval/` package) --
  NDCG@k, Precision@k, MAP, Jaccard, confidence calibration metrics.
  Loads datasets from `~/.gramaton/eval/` (generated by gramaton-bench).
  Supports per-dataset and combined evaluation, weight configuration
  matrix testing. Mock and LLM-gated capture quality evaluation.
- **Backup status endpoint** -- `GET /v1/backup` lists existing backups
  without creating a new one.

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
- **Observe pipeline** -- `gramaton_observe` MCP tool and
  `POST /v1/observe` endpoint. Dual-mode: send raw conversation
  messages (server extracts via LLM) or pre-extracted facts (no LLM
  needed). Fire-and-forget with async processing. Three-layer feedback
  loop detection (dedup, recency, retrieval tracking). Quality gates
  filter noise before storage. Survivors stored as deferred captures
  (ephemeral, low confidence) for curation to promote or decay.
- **Retrieval tracker** -- server automatically tracks which records
  were served to agents via search, inspect, and explore. Used by
  observe quality gates to prevent feedback loops (re-extracting
  knowledge that was just retrieved). Thread-safe, time-bounded,
  zero API changes.
- **Garbage collection** -- deterministic curation phase that hard-
  deletes records meeting ALL criteria: unclassified 30+ days, zero
  access, zero edges, ephemeral, low confidence. Off by default,
  dry-run by default when enabled. Implements "never delete knowledge,
  forget noise" principle.
- **Curation dry-run** -- `gramaton_curation(action="dry_run")` runs
  the full autonomous LLM pipeline but returns planned changes instead
  of applying them. Preview what curation would do.
- **Contradiction detection** -- autonomous curation detects semantic
  contradictions between records with 0.5-0.85 cosine similarity.
  LLM evaluates pairs and creates `contradicts` or `supersedes` edges.
  Configurable thresholds and batch limits.
- **Store manifest qualitative summary** -- LLM-generated 2-3 sentence
  assessment of store strengths and gaps. Included in manifest alongside
  auto-generated stats. Top 20 keywords tracked for domain analysis.
- **Concept node enrichment** -- deterministic curation computes
  `evidence_count` and `last_evidence_at` on concept nodes from
  inbound edges each cycle.
- **Expiration visibility** -- metadata_summary now shows "expires in
  3 days" or "expired 5 days ago" instead of just "Current/Historical".
- **asserted_as_of field** -- new timestamp property for when the source
  made a claim (distinct from created_at when captured). Supported on
  capture, update, classify, search results, MCP tools, and import.
- **Record resolution lifecycle** -- `gramaton_resolve` MCP tool,
  `POST /v1/records/{id}/resolve` endpoint, and `gramaton resolve` CLI
  command. General-purpose lifecycle transitions: completed, superseded,
  abandoned, obsolete. Sets `resolution`, `resolved_at`, and auto-sets
  `valid_until` to deprioritize in search. Search supports `resolution`
  filter (including `unresolved` for records with no resolution set).
  Auto-supersession now sets `resolution: superseded` on old records.
  Resolution appears in metadata summaries and search facets.
- **Engine functional options** -- `LoadEngineWithOptions` supports
  dependency injection via `WithEmbedder` and `WithLLM` at construction.
  Dependencies immutable after init (no runtime setters).
- **Curation endpoints** -- `GET /v1/curation` (status, concept
  candidates, manifest) and `POST /v1/curation/trigger` (manual
  cycle, dry-run). `gramaton_curation` MCP tool with status/trigger/
  dry_run actions.
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
- **MCP integration** -- 19 MCP tools via Streamable HTTP at `/mcp`.
  Agents call `gramaton_search`, `gramaton_capture`, etc. as typed
  tools with no shell involvement. Uses official MCP Go SDK
  (Apache-2.0). Stdio transport via `gramaton mcp` for clients like
  Claude Code.
- **MCP proxy architecture** -- `gramaton mcp` is now a stateless HTTP
  proxy. Tool calls forward to the HTTP server instead of loading a
  separate engine in-process. Eliminates state divergence when multiple
  Claude Code sessions run simultaneously. Server auto-starts if not
  running. MCP processes can die and restart with zero consequences.
- **Backup status endpoint** -- `GET /v1/backup` lists existing backups
  without creating a new one. Used by `gramaton_backup(action="status")`.
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

- **Backup/restore** -- `gramaton backup` creates compressed tar.gz
  archives with ISO8601 timestamps in filenames. Includes all store
  data (chunks, HEAD, refs) and sanitized config (API keys stripped).
  `gramaton restore` with interactive confirmation. Auto-backup via
  curation runner with configurable schedule and retention (default:
  2 backups, daily). POST /v1/backup and gramaton_backup MCP tool.
- **Export** -- query-driven export in JSON Lines, CSV, and Markdown
  formats. Reuses search filter infrastructure. POST /v1/export
  endpoint and `gramaton export` CLI command with --format and
  --output flags.
- **Import** -- JSON Lines import with property allowlist, new ULID
  assignment, edge remapping within import batch. CSV import with
  column mapping and aliases. Obsidian vault import with YAML
  frontmatter parsing and [[wikilink]] to edge conversion. Security:
  content sanitization, safe property allowlist, max 10K records per
  import, no edges to pre-existing records.
- **Search flag helpers** -- extracted addSearchFlags/buildSearchBody
  from search_cmd.go for reuse by export command.
- **Structured logging** -- JSON-formatted logs via `log/slog` with
  levels (debug/info/warn/error). File-based at `~/.gramaton/gramaton.log`
  with automatic rotation (50MB per file), gzip compression of old
  files, and configurable disk budget (default 512MB). Foreground
  mode also writes to stderr. Debug level logs every HTTP request
  with method, path, duration, and remote address. Curation logs
  include component tags for filtering.
- **Log rotation** -- built-in rotating writer with gzip compression.
  No external dependency (lumberjack). Enforces total disk budget by
  deleting oldest compressed files first.
- **Update endpoint extended** -- PATCH /v1/records/{id} and
  gramaton_update MCP tool now support valid_until, keywords, and
  summary_short in addition to existing metadata fields.
- **Named stores** -- multiple isolated knowledge graphs under
  `~/.gramaton/stores/<name>/`. Each store has its own data directory,
  server process, and optional config override (inherits global config
  if absent). Select via `--store <name>` flag or `GRAMATON_STORE`
  env var. Management commands: `gramaton store list|create|delete|rename`.
  Renaming supports the unnamed default store via the "default" alias.
  Backup filenames include the store name for identification. Store
  names validated: 1-64 alphanumeric/hyphen/underscore characters.

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
- Reembed pre-embeds outside the lock using the same gather-embed-apply
  pattern as capture. Previously held the write lock during all external
  embedding calls, blocking the entire server for the batch duration.
- Content length validated against MaxContentLength on capture endpoint
  (previously only enforced on import)
- Keyword count (100) and per-keyword length (256 chars) validated on
  capture, update, and classify endpoints (previously only on search)
- String field length limits enforced: summary_short (500), summary_abstract
  (5000), source_ref (2048), all context fields (2048)
- Reembed batch size capped at 500 (previously unbounded)
- Log endpoint limit capped at 500 (previously unbounded)
- Diff topic parameter validated against maxTopicLength before use
- Export top parameter capped at 10000 (previously unbounded when
  explicitly set)
- Ingest filenames sanitized via filepath.Base to strip directory
  components from stored source_ref
- Backup archive validation tightened: HEAD file check requires root-level
  entry (data/HEAD or HEAD), not any nested file named HEAD
- Backup restore rejects symlinks, hardlinks, and other non-regular file
  types in tar archives (previously silently ignored)
- Config directory created with 0o700 permissions (was 0o755)
- Store name validated against allowlist regex before path resolution
  (prevents path traversal via --store flag or GRAMATON_STORE env var)

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

[Unreleased]: https://github.com/gramaton-ai/gramaton/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/gramaton-ai/gramaton/releases/tag/v0.1.0
