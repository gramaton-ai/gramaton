# Glossary

Quick reference for terminology used across the project.

## Terminology Convention

Two terms describe the same thing for different audiences:

- **Knowledge graph** — used in tool descriptions, API docs, and developer docs. Primes LLMs and developers to think in terms of nodes, edges, and traversal.
- **Knowledge store** — used in README, user-facing docs, error messages, and CLI output. Approachable for non-technical users who just want to store and retrieve knowledge.

Never mix both in the same surface. Pick one per context.

## System

| Term | Definition |
|------|-----------|
| **Gramaton** | The complete system — service + agent integration kit. |
| **Gramaton Service** | The Go server binary. Stores knowledge, delegates embedding to an external provider (Ollama/API/Bedrock), serves queries. No LLM dependency. Pure Go, no native dependencies. |
| **Agent Integration Kit** | Prompt patterns, system prompt templates, subagent prompts, and skill definitions that give agents the ability to use Gramaton transparently. |
| **Context envelope** | Five domain-neutral structured fields the agent packages alongside content at capture time: what is this about, who/what is involved, what prompted this, what should this be findable by, what else relates. Contains implicit knowledge that isn't in the content itself. What makes records findable by context, not just by content. |
| **Retrieval funnel** | The enforced query pattern: cheap/broad tools first, expensive/deep tools require IDs from narrower tools. Enforced by API shape, not prompt instructions. |
| **Store manifest** | Lightweight summary of what the knowledge store contains (domains, projects, counts, temporal range, strengths/gaps). Injected into agent system prompt so the agent knows what the store covers. |

## Data Model

| Term | Definition |
|------|-----------|
| **Node** | The fundamental unit. A bag of typed key-value properties with a unique ID. |
| **Edge** | A first-class relationship between two nodes. Has ID, type (string), weight (0.0–1.0), and optional properties. Bidirectionally indexed. |
| **Property** | A typed key-value pair on a node or edge. 8 types: String, Float64, Int64, Bool, Timestamp, Vector, StringList, Bytes. Always flat — no nesting. |
| **Knowledge record** | A node populated with Gramaton's metadata schema — content, embeddings, epistemic metadata, lifecycle, provenance. |
| **Concept node** | A source-independent node representing a concept (e.g., "Kafka", "OAuth2"). Acts as a hub — many knowledge records link to it. `knowledge_type: conceptual`. |
| **Chunk node** | A fragment of a long document, linked to its parent via `part_of` edge. Holds chunk text + embedding. Minimal metadata. |

## Summary Pyramid

| Term | Definition |
|------|-----------|
| **Summary pyramid** | Multiple representations of the same content at different token costs. A retrieval optimization, not a decomposition strategy. |
| **content_keywords** | StringList. Extracted topic keywords. ~10 tokens. Used for cheap pre-filtering. |
| **content_short** | String. Max ~200 characters (~50 tokens). Brief summary for relevance scanning. |
| **content_abstract** | String. Max ~2000 characters (~500 tokens). Paragraph-level summary. |
| **content_full** | String. No limit. Complete processed content. |
| **source_ref** | String. Pointer to the raw unprocessed source on the filesystem. |
| **embedding_*** | Vector properties. One per pyramid level. Each independently searchable. |

## Epistemic Metadata

| Term | Definition |
|------|-----------|
| **temporality** | How long this knowledge remains valid. `immutable` (definitional truths), `durable` (stable until contradicted), `temporal` (time-bound, will decay), `ephemeral` (minutes/hours lifespan). |
| **confidence** | Float64 (0.0–1.0). How likely correct. Used for retrieval scoring. |
| **importance** | Float64 (0.0–1.0). Retrieval priority floor. Prevents high-importance records from decaying. |
| **epistemic_status** | Qualitative state: `well_established`, `probable`, `speculative`, `contested`, `refuted`. Distinct from confidence — a refuted record has high confidence (we're confident it's wrong). |
| **knowledge_type** | What kind: `episodic` (events), `semantic` (facts), `procedural` (how-to), `conceptual` (definitions), `reference` (lookup data). |

## Query & Retrieval

| Term | Definition |
|------|-----------|
| **Filter → Rank → Traverse** | Gramaton's primary query pattern. (1) Filter nodes by metadata, (2) rank by vector similarity, (3) traverse edges from top results. |
| **Tier 1: Discovery** | `gramaton search`. Cheap, broad. Returns lightweight results (ID, keywords, short summary, metadata). |
| **Tier 2: Inspection** | `gramaton inspect`. Medium cost. Returns full content, all metadata, one-hop related nodes. |
| **Tier 3: Exploration** | `gramaton explore`. Graph traversal. Returns a subgraph fragment. |
| **Raw source (via `source_ref`)** | The `source_ref` property on a record holds a pointer to the original source (URL or filesystem path). There is no dedicated `gramaton raw` command in the current CLI — callers resolve the reference themselves. |
| **Spreading activation** | Accessing a node increments `access_count`, updates `last_accessed`, and adds to `activation_boost` on direct neighbors (weighted by edge weight × attenuation factor). Single-hop only. |
| **Decay** | Retrieval priority decreases over time. Exponential decay with rate determined by temporality: ephemeral (4h half-life), temporal (3d), durable (90d), immutable (never). Computed at query time, not stored. |
| **activation_boost** | Float64 property on a node. Accumulated indirect activation from neighbor access. Decays over time independently. Part of the scoring model. |
| **effective_score** | Computed at query time. Weighted combination of vector similarity, freshness, ACT-R activation, and confidence. Importance acts as a floor. Never stored. |

## Capture & Processing

| Term | Definition |
|------|-----------|
| **Transparent capture** | The agent decides autonomously to store knowledge, spawning a subagent without interrupting the user's conversation. |
| **Transparent retrieval** | The agent searches Gramaton as part of normal reasoning without the user explicitly asking. |
| **Subagent** | A separate agent context spawned by the main agent to handle a delegated task. Originally part of the pre-session capture pattern (classification + storage in a side context); currently more commonly used for other delegated work (code-review, research spanning large file sets). The session prepare/commit flow replaces most capture-time subagent usage. |
| **Processing status** | `captured` (raw, unclassified), `pending` (queued for enrichment), `processed` (fully classified). |
| **Chunking** | Splitting long content into overlapping fragments for embedding. Each chunk becomes a child node. |

## Architecture

| Term | Definition |
|------|-----------|
| **Embeddable** | A library linked into your application — one process, no network. (This is what Gramaton is NOT — it's a server.) |
| **Content-addressed storage** | Data identified by hash of content. Same content always produces the same hash. Enables deduplication and efficient diffing. |
| **StorageBackend** | Go interface abstracting the on-disk format. All storage access goes through this — no component assumes files, directories, or any physical layout. |
| **VectorIndex** | Go interface abstracting the vector search implementation. HNSW for large sets, flat scan for small filtered sets. Dynamic switching. |
| **HNSW** | Hierarchical Navigable Small World. Algorithm for approximate nearest neighbor search in vector spaces. The primary vector index. |
| **Embedding** | A numerical vector representing the "meaning" of text. Semantically similar texts produce similar vectors. Generated by an embedding provider (Ollama, OpenAI-compatible API, or AWS Bedrock), not an LLM. |
| **EmbeddingProvider** | Go interface abstracting how embeddings are generated. Four shipped implementations: pure-Go BERT (default, in-process inference, no external runtime), Ollama (local HTTP), OpenAI-compatible (HTTP), AWS Bedrock (AWS SDK). |

## Versioning

| Term | Definition |
|------|-----------|
| **Commit** | An immutable snapshot of the graph state, with parent pointers and metadata (author, timestamp, message). Every mutation creates a commit. |
| **Branch** | A named mutable pointer to a commit. Enables speculative reasoning, curation safety, and per-project isolation. |
| **Three-way merge** | Conflict resolution: find common ancestor, diff both sides, combine non-conflicting changes, surface conflicts. |
| **Knowledge diffing** | Querying what changed between two points in the graph's history, optionally scoped to a topic. Answers "what evolved" not just "what exists." |
| **Speculative branching** | Creating a branch to explore a design option or hypothesis without polluting the main store. Merge if adopted, discard if rejected. Maps to hippocampal working memory in neuroscience. |
| **Audit trail** | The commit history of a specific record — when it was created, how it changed, why confidence was adjusted, what contradicted it. Enables provenance-aware reasoning by agents. |
| **Rollback** | Atomic revert of any commit. Undoes a batch of captures or a bad curation run cleanly. |

## Neuroscience-Inspired

| Term | Definition |
|------|-----------|
| **Engram** | The physical trace of a memory in the brain. Gramaton's spiritual ancestor. |
| **Dual-store model** | Fast capture + slow integration. Maps to: immediate storage with embeddings (fast) + deferred LLM classification (slow). |
| **Default mode network** | Brain network active during rest that consolidates memory. Maps to Gramaton's curation — background maintenance during idle time. |
| **Principled forgetting** | Not all forgetting is bad. Low-value records are intentionally deprioritized to keep retrieval efficient. |

## Sessions & Collections

| Term | Definition |
|------|-----------|
| **Session** | A conversation thread bound to a client by `client_session_id`. Sessions hold committed segments and optionally an archived transcript. Identified by `session_id` (ULID). Created lazily on `gramaton_session_start`. |
| **Session segment** | A single extracted unit of knowledge from a conversation, committed via `gramaton_session_commit`. BM25-indexed and reachable by session-scoped queries. Each segment can also create a linked Memory record. |
| **Memory record** | A knowledge record in the Memory store — vector-embedded, full lifecycle (classification, supersession, decay). Can be created directly via `gramaton_capture` / `gramaton_intake` / `gramaton ingest`, or as a by-product of a session commit with `promote_to_memory: true`. |
| **`promote_to_memory`** | Boolean flag on each session segment. `true` (default) creates a linked Memory record alongside the Session segment; `false` keeps the segment Session-only so exploration and dead ends remain findable without competing in Memory's vector space. |
| **`extracted_as` edge** | Edge connecting a Session segment to its linked Memory record, created when a segment is committed with `promote_to_memory: true`. |
| **Session archive** | A compressed on-disk copy of a session's raw transcript, optionally created via `gramaton session archive` (or the shipped `hooks/claude-code/pre-compact.sh`). Path is recorded on the session node as `archive_path`; the archive is not indexed for search. |
| **Collection** | A named container with exhaustive retrieval semantics and optional schema enforcement. `gramaton_collection_items` returns every item — no ranking, no top-N. Items are also graph nodes and can be linked to Memory records. |
| **Collection schema** | Optional structure enforcing typed fields (`string`, `number`, `boolean`, `date`, `enum`, `enum[]`) with optional `required` flags. Validated on every `add` / `update`. |
| **`client_session_id`** | The caller-provided identifier that ties a conversation to a Gramaton `session_id`. Multiple client session IDs can map to different Gramaton sessions; binding is set at `gramaton_session_start`. |

## Named stores

| Term | Definition |
|------|-----------|
| **Named store** | An isolated Gramaton store with its own data directory and optional per-store config, selected via `gramaton --store <name>`. Lives at `~/.gramaton/stores/<name>/`. The default (unnamed) store lives at `~/.gramaton/data/`. |
| **`LoadWithFallback`** | Config loader that tries a per-store `config.yaml` first and falls back to the global config if the per-store file is missing. Full-replace, not deep-merge — a partial per-store config silently zero-values unspecified sections. |

## api / transports

| Term | Definition |
|------|-----------|
| **api (canonical surface)** | The `api/` Go package. One file per operation, each declaring `XxxRequest`, `XxxResponse`, `XxxDescription`, and `func (a *API) Xxx(...)`. Every transport (HTTP, MCP, CLI proxy) consumes these types and methods via hand-written binding tables. Locking discipline lives here. |
| **Transport** | Any of the three surfaces that expose the api layer to clients: HTTP routes (`server/bindings_*.go`), MCP tool registrations (`server/mcp.go` + bindings clusters, or `cli/mcp_proxy_*.go` for stdio proxy), or Cobra CLI commands (`cli/*.go`). |
| **MCP cluster registrar** | A function in `server/bindings_*.go` or `server/mcp_*.go` that registers a cluster of related MCP tools. Nine clusters as of T-02: records, search, intake, maintenance, history, admin, collections, sessions, guide. |
