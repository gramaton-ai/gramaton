# Design Decisions

Every major decision with the reasoning behind it. Newest first.

---

### D30: Two Time Signals — Exponential Access Decay + Power Law Knowledge Freshness

**Decision:** Retrieval scoring uses two independent time signals with different curves. Access recency (exponential, keyed off `last_accessed`) handles "how recently was this useful." Knowledge freshness (power law, keyed off `valid_from` or `created_at`) handles "how old is the underlying knowledge." For records with explicit `valid_from` / `valid_until` dates, hard validity filtering is preferred over soft scoring. Default search behavior prefers currently-valid records. `--include-historical` overrides for history/evolution queries.

**Why:** A single exponential decay keyed off ingestion time fails when multiple records about the same topic are ingested simultaneously with different real-world dates (e.g., five years of strategy docs). Exponential decay also crushes anything older than a few months to zero — wrong for meaningful knowledge that follows Bahrick's "permastore" pattern. The power law has a long tail: a 5-year-old durable record scores 0.43, not 0.000003. Knowledge freshness is gated by temporality: immutable records always score 1.0 (Leibniz's calculus doesn't age), durable uses gentle exponent (0.5), temporal/ephemeral use steeper (1.0). Validity filtering (`valid_until`) is the primary mechanism for dated knowledge — the freshness score is a secondary soft signal for undated captures.

---

### D29: Flat Package Structure, Consumer-Defined Interfaces, No God Packages

**Decision:** No `internal/`, no `pkg/`. Flat domain-named packages at root level. Tools are separate packages (search/, capture/, inspect/, etc.) not one `tool/` package. Interfaces defined at consumers (unexported), not providers — except `embed/` which has multiple implementations. `main.go` is the composition root. Types live with their operations (graph.Node, not models.Node).

**Why:** Research-validated against Prometheus, Hugo, Dolt, CockroachDB, and Go community best practices. `internal/` is for libraries with external consumers — we're a single binary. `pkg/` is an anti-pattern with no information value. A unified `tool/` package would import everything and become a god package. Consumer-defined interfaces give better testability (test fakes are local), tighter coupling boundaries, and follow Go's "accept interfaces, return structs" idiom. Flat structure makes packages easy to find, rename, and split.

---

### D28: Auto-Start Server, Never Think About It

**Decision:** The server starts automatically in the background when any CLI command needs it. Users never type `gramaton serve` in normal use. Auto-stops after configurable idle (default: 30m). `gramaton serve` exists for power users who want foreground logs. `gramaton stop` for explicit shutdown. `gramaton status` for visibility.

**Why:** The server model (D8) is an architectural choice for concurrency and write serialization. The user shouldn't have to care about it. "Install, init, use" — no server management in the flow. Precedent: Docker Desktop, Ollama, and other developer tools auto-start their daemons transparently. A pure Go server starts in milliseconds and uses negligible memory when idle.

---

### D27: One Store Default + Export/Import for Sharing

**Decision:** Default is one store per user containing all knowledge. The value is in cross-domain connections — splitting into separate stores loses that. A `--data-dir` flag exists as an escape hatch for genuinely isolated stores. Export/import provides portable subsets: full export, recursive node export, topic-filtered, branch, or time range. Import is additive with content-hash deduplication.

**Why:** The graph's power is connections between knowledge from different domains. A Kafka decision in project A linking to Kafka expertise from project B is the whole point. Metadata, keywords, concept nodes, and edges handle organization within one graph — separate stores are rarely needed. Export/import covers sharing, backup, and seeding without the complexity of multi-tenant servers or store federation.

---

### D26: Merge Conflict Resolution — Timestamp Wins for v0.1

**Decision:** Property conflicts (both sides changed the same property on the same node) resolved by most recent timestamp wins. Duplicate concept nodes merged automatically. Node-modified-vs-deleted: keep the node (tenet 8). All resolutions logged in the merge commit. Interactive three-way merge is a future feature.

**Why:** Curation branches in v0.1 are short-lived (created and merged within one session). Conflicts are rare — main mostly gets new captures, branches mostly modify existing records. Auto-resolution is sufficient. The commit history preserves the losing change, so nothing is lost. Interactive resolution adds significant UX complexity for a scenario that barely occurs in v0.1.

---

### D25: ULIDs for Node and Edge IDs

**Decision:** All nodes and edges identified by ULIDs. 26 characters, case-insensitive, no hyphens, time-sortable, globally unique. Content hash is a separate value used by the storage layer — the ID is stable across mutations.

**Why:** CLI-friendly (agents type these IDs in commands — 10 chars shorter than UUID, no hyphens). Time-sortable (chronological ordering for free). Case-insensitive (Crockford Base32 avoids ambiguous characters). UUID v7 was the alternative (RFC 9562 standard) but the 36-char hyphenated format is worse for CLI ergonomics, and nothing in Gramaton requires UUID compatibility with external systems.

---

### D24: Embedding Model Migration via embedding_model Property

**Decision:** Every node stores `embedding_model` (the identifier of the model that generated its embeddings). When the current provider's model differs from stored `embedding_model`, records are flagged as stale and excluded from vector similarity. `gramaton reembed` re-processes stale records through the current provider. `gramaton status` reports embedding health.

**Why:** Users will change embedding models — switching providers, updating Ollama, or upgrading to a better model. Old and new embeddings are from different vector spaces; similarity between them is meaningless. Without migration detection, retrieval quality silently degrades. The `embedding_model` property makes this detectable and the `reembed` command makes it fixable.

---

### D23: Store Raw Context Envelope on Knowledge Records

**Decision:** The five context envelope fields are stored as String properties on the knowledge record (`context_about`, `context_who`, `context_prompted`, `context_findable_by`, `context_related`). All optional. Not indexed for search — extracted keywords handle that. Stored for inspectability.

**Why:** The subagent extracts keywords and creates edges from the envelope, but the raw fields contain information that keyword extraction might miss. "What prompted this: decision was due Friday, load testing completed" is richer than any keyword the subagent extracts. Storing it supports tenet 6 (transparent by default) and preserves the full capture context for debugging, auditing, and future re-processing. The storage cost is negligible — five short strings per record.

---

### D22: Unified Scoring Model with activation_boost and Research-Backed Decay Rates

**Decision:** Single authoritative scoring model defined in retrieval.md. `effective_score` is a weighted combination of 5 factors: vector similarity, recency (exponential decay by temporality), access frequency, activation boost, and confidence. Importance acts as a floor. New `activation_boost` Float64 property added to the data model — the storage target for spreading activation. Decay rates based on Ebbinghaus forgetting curves, Generative Agents (Park et al. 2023), and practical analysis: ephemeral 4h half-life, temporal 3d, durable 90d, immutable never. All weights and rates in config.

**Why:** The scoring model was inconsistent across two documents (retrieval.md had 6 vague factors, curation.md had a 2-factor formula referencing a non-existent property). An implementer could not build either version. This reconciles them into one coherent, fully specified model with every stored property listed in the data model and every computed value defined as a formula. The decay rates are empirically informed starting points — the formulas are the design decision, the specific constants are tunable.

---

### D21: Ollama Default, OpenAI-Compatible + Bedrock as Alternatives

**Decision:** Embedding is delegated to external providers via a pure Go interface. Default is Ollama (local HTTP API). Also supports any OpenAI-compatible API and AWS Bedrock (with IAM profiles, roles, short-lived keys, and long-lived keys). No bundled model, no CGo, no native dependencies in the Go binary. Gramaton works without embeddings configured (keyword + property + graph search still work).

**Why:** No production-ready pure Go embedding inference exists. CGo-based solutions (ONNX Runtime, HuggingFace tokenizers) create cross-platform build pain and 3p import friction for enterprise environments. Building our own is risky (numerical precision, tokenizer edge cases, maintenance burden). Ollama runs optimized native inference and is increasingly common among our target users. The Go binary stays pure Go — trivial cross-compilation, clean enterprise import. Engineering time goes to Gramaton's differentiating features, not solved problems.

---

### D20: Metadata Exposure — Raw Fields + LLM-Readable Summary

**Decision:** CLI output includes both raw metadata fields (for filtering and programmatic use) and a `metadata_summary` string (natural language, generated by the tool layer). Also includes `effective_score` (computed retrieval score after decay/similarity). The system prompt teaches the agent how to interpret raw fields for edge cases; `metadata_summary` handles the 90% case.

**Why:** Two layers of metadata use. Layer 1: tools use metadata to filter/rank before the agent sees anything (the primary value — handled by CLI flags). Layer 2: metadata exposed to the agent for reasoning (secondary value — handled by the output format). Raw field names like `epistemic_status: "contested"` are interpretable by LLMs but not reliably enough. The `metadata_summary` ("Durable, high-confidence, well-established. Last accessed 19 days ago.") gives the agent a natural language read it can use immediately. Raw fields remain available for when the agent needs to reason more carefully.

---

### D19: Flat Hash Lists for Commit Storage in v0.1

**Decision:** Each commit stores a flat list of all node/edge content hashes. Branching, diffing, and merging operate on these lists. Upgrade to prolly trees behind the same StorageBackend interface if scale demands it.

**Why:** Branching is cheap (just a pointer). Writing to a branch only creates new chunks for changed nodes — unchanged nodes share storage via content-addressing. Merging diffs the hash lists and applies non-conflicting changes. At v0.1 scale (thousands to low tens of thousands of nodes), a flat list of hashes is ~320KB and diffs in milliseconds. A typical piggyback curation branch (50 mutations) merges in sub-millisecond time regardless of total store size. Prolly trees (what Dolt/Git use for efficient diffing at scale) are the right upgrade path at 100K+ nodes but are significant implementation effort not justified at v0.1 scale.

---

### D18: Apache 2.0 License, No CLA

**Decision:** Apache 2.0 license. No Contributor License Agreement for now.

**Why:** Maximizes adoption and career signal — the two primary goals. Patent grant protects users. Permissive license means companies can evaluate and adopt freely. No license practically prevents a large company from building the same thing independently — what matters is the public record of the design and implementation. CLA adds friction that discourages early contributors and isn't needed until the project has enough traction that relicensing becomes realistic. Add CLA later if growth warrants it.

---

### D17: Versioning as Agent-Facing Tools in v0.1

**Decision:** Versioning (commits, branches, diff, merge, log, revert) is a v0.1 feature, not deferred. Exposed as CLI commands that agents use for knowledge diffing, audit trails, speculative branching, and rollback.

**Why:** These are differentiating features, not just infrastructure. Knowledge diffing ("what changed about authentication since March") is a fundamentally new retrieval pattern that vector databases can't do. Commit audit trails let agents explain WHY a record has low confidence, not just that it does. Speculative branching lets agents explore design options without polluting the store. These capabilities change how agents reason, not just how they retrieve. LLMs are very good at narrating structured diffs and reasoning about provenance — these tools play directly to LLM strengths.

---

### D16: Piggyback Curation

**Decision:** LLM-requiring curation runs opportunistically during normal agent sessions. The server includes curation status (overdue, pending count) in CLI responses. The agent's system prompt tells it to spawn a curation subagent when curation is overdue. Processes pending records in priority order (most important/recent first). Runs once per session, in background.

**Why:** Curation that only runs when the user remembers `/gramaton-curate` won't run. The brain's default mode network runs during rest, not on demand. Piggyback curation is the zero-config path — no cron jobs, no headless sessions, no separate LLM provider. Optional direct API-driven curation deferred to v2.

---

### D15: Concept Emergence via Evidence Accumulation

**Decision:** Concept nodes are not created on first mention. Keywords are stored on knowledge records. When a keyword appears across multiple records (default threshold: 3), it graduates to a concept node. Exception: explicit concept definitions (`knowledge_type: conceptual`) create concept nodes immediately.

**Why:** Grounded in neuroscience — the brain doesn't form semantic concepts from a single episode. The neocortex slowly extracts patterns across many episodic memories. Hippocampus = fast capture of individual records with keywords. Neocortex = slow extraction of concept nodes from repeated keywords. The curation layer acts as the neocortex. This avoids the "concept boundary problem" (when does a keyword become a concept?) by replacing a categorical judgment with an evidence threshold.

---

### D14: Agent-Autonomous Capture and Retrieval

**Decision:** Agents decide when to search and when to capture without user involvement. Capture happens via subagent to avoid blocking the conversation.

**Why:** The goal is transparency. If users have to remember slash commands, they won't use the system consistently. The agent is already in the conversation — it knows when a decision is being made, when context would help, and when something is worth remembering. Let it act on that.

**Risk:** Agents may capture too much (noise) or too little (missed knowledge). This is a prompt engineering problem — tunable via the system prompt instructions without changing the architecture.

---

### D13: Context Envelope for Capture

**Decision:** When capturing knowledge, the agent packages both the content and a freeform "context envelope" containing everything it knows about the situation (project, team, tickets, prior discussions) that isn't in the content itself.

**Why:** "We chose Kafka over RabbitMQ" is meaningless without knowing it's for the Event Pipeline project, PLAT-847, Platform Engineering team. The agent has this context from the conversation. Without packaging it, the record is only findable by its literal content. The context envelope is what turns a stored string into findable, linked knowledge.

---

### D12: Agent Is the LLM Provider

**Decision:** No LLM infrastructure in the Gramaton server. All LLM work (classification, summarization, decomposition, LLM-requiring curation) happens in the agent session via subagents or skills.

**Why:** Most users (Claude Code, Kiro CLI) don't have separate API keys. The only LLM available is the one powering their session. Building LLM provider infrastructure into the server adds complexity (API key management, provider abstraction, async workers) that's unnecessary when the agent can do the work. This dramatically simplifies the server.

**Tradeoff:** Scheduled autonomous curation (e.g., nightly contradiction detection) can't run without an agent session. This is acceptable for v0.1 — curation runs when the user invokes `/gramaton-curate`.

---

### D11: Two-Part System (Service + Agent Integration Kit)

**Decision:** Gramaton is a server that handles storage/embeddings/queries (no LLM) plus an agent integration kit of prompts/skills that provide the LLM intelligence.

**Why:** Clean separation of concerns. The server is reliable, simple, pure Go, and delegates embedding to an external provider (Ollama/API/Bedrock). The integration kit adapts to whatever agent framework the user has. Neither part is useful alone — the server without the kit is a vector database, the kit without the server has nowhere to store knowledge.

---

### ~~D10: Local Bundled Embedding Model~~ — SUPERSEDED by D21

**Original decision:** Bundle a 33M-parameter embedding model in INT8 ONNX format with the Gramaton binary.

**Superseded because:** No production-ready pure Go inference exists. CGo-based solutions (ONNX Runtime) create cross-platform build pain and enterprise import friction. Ollama provides the same local embedding experience without any of these costs. See D21.

---

### D9: Unified Project

**Decision:** One project, one binary, well-separated internal packages. The knowledge management layer and the storage engine are not separate projects — they're packages within the same codebase.

**Why:** In the service model, everything compiles into the same binary. Separate projects would add naming overhead and conceptual complexity without a practical benefit. The storage engine isn't distributed separately. Clean package boundaries achieve the same isolation.

---

### D8: Service Model with CLI Primary Interface

**Decision:** Gramaton runs as a server process. Agents interact via CLI commands, HTTP, or MCP. Not an embeddable library.

**Why:** Multiple agents need safe concurrent access to the same knowledge store. A server handles this naturally. CLI is the most portable interface — every agent framework can shell out. HTTP and MCP are additional interfaces for frameworks that prefer them. The latency difference (milliseconds for CLI vs microseconds for in-process) is irrelevant when the bottleneck is LLM thinking.

---

### D7: Go (Pivoted from Rust + PyO3)

**Decision:** Write Gramaton in Go, not Rust.

**Why:** The original Rust + PyO3 decision was made when the design was an embeddable library with Python bindings. The pivot to a service model removed the two strongest Rust arguments (no-GC for embedding, PyO3 for Python bindings). Go's strengths dominate for a server: single binary distribution, trivial cross-compilation, goroutines for concurrent access, developer familiarity, and a mature ecosystem for infrastructure/data services.

---

### D6: Retrieval Funnel Enforced by API Shape

**Decision:** Deeper retrieval tools require record IDs that can only be obtained from narrower search tools first.

**Why:** Prompt instructions telling LLMs to "search first, then read" are unreliable. LLMs will bypass efficiency if structurally allowed to. The funnel is enforced structurally: `gramaton inspect` requires an ID that only `gramaton search` returns. Same pattern as web search — titles and snippets first, full pages on click-through.

---

### D5: Summary Pyramid as Naming Convention

**Decision:** Multiple content/embedding properties per node (content_keywords, content_short, content_abstract, content_full, with corresponding embedding_* properties). The engine doesn't know they're related — they're just properties.

**Why:** Keeps the graph engine generic. No special "summary" concept baked into the engine. The convention is enforced by the tool layer and the agent integration kit. New summary levels can be added without engine changes.

---

### D4: Property Graph with Typed Properties

**Decision:** 8 property types (String, Float64, Int64, Bool, Timestamp, Vector, StringList, Bytes). Flat only, no nesting, no nulls. First-class bidirectional edges with type, weight, and properties.

**Why:** Flat properties keep the engine simple and queries fast. No nested objects means every value is directly indexable and filterable. If something needs to be queried independently, it's a node, not a nested property. Bidirectional edge indexes enable efficient graph traversal in both directions.

---

### D3: Filter → Rank → Traverse

**Decision:** The primary query pattern combines metadata filtering, vector similarity ranking, and graph traversal in a single operation.

**Why:** This is what LLM retrieval actually needs: "Find me high-confidence durable knowledge about retry strategies, and show me what's related." No existing embeddable engine combines all three. Building this as a first-class operation rather than composing separate systems avoids coordination overhead and enables optimization.

---

### D2: Content-Addressed Storage

**Decision:** Data identified by hash of content. Directory of content-addressed chunks on disk. All access through a StorageBackend interface.

**Why:** From Dolt/Git — content addressing enables efficient diffing (skip identical hashes), deduplication (same content shares storage), and future versioning (commits reference content hashes). The StorageBackend interface means the on-disk format can change without touching other layers.

---

### D1: Metadata Is the Product

**Decision:** The core value is the epistemic metadata (temporality, confidence, knowledge_type, provenance), not the storage engine.

**Why:** A knowledge store without metadata is just a vector database. The metadata is what tells future consumers whether to trust a piece of knowledge and how to use it. Storage engines are solved problems. The unsolved problem is wrapping knowledge in metadata that preserves its epistemic context.
