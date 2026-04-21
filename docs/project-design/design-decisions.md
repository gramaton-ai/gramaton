# Design Decisions

Every major decision with the reasoning behind it. Newest first.

---

### D40: `WriteSession` Pattern for Batched Index Writes (P2-06)

**Decision:** The stashed `batch *bolt.Tx` field on BboltPropertyIndex / BboltBM25Index / BboltSecondaryIndex / BboltCollectionCache / BboltEdgeStore is replaced by an explicit `core.WriteSession` type that owns the shared bbolt transaction and the BM25/Edge companion caches. Mutating methods on the five types grow `*Tx`-suffixed variants that take the tx directly. `Engine.WithWriteBatch`'s fn signature changes from `func() (bool, error)` to `func(*WriteSession) (bool, error)` so callers inside a batch operate through the session. `Graph.AddEdge` gains an `AddEdgeTx(tx, batch, ...)` companion used internally by `WriteSession.AddEdge`. The 139 existing `Graph.AddEdge` call sites outside batches are unchanged. Engine-level methods (`SetProp`, `IndexNode`, etc.) retain their current signatures — callers outside a batch continue to use them as before; callers inside a batch use `WriteSession` methods instead.

The refactor lands in three stages: (1) hoist companion caches into value types (landed in commit `2994c30`), (2) add Tx-suffixed variants to the five types + Graph and remove the stashed `batch` field + `SetBatch`/`ClearBatch`, (3) introduce `WriteSession`, flip `WithWriteBatch`'s fn signature, and migrate curation/observe and curation/deterministic.

**Why:** The `SetBatch(tx)` + stashed-pointer pattern was flagged as a race hazard (P2-06) under hypothetical finer-grained locking, with three distinct failure modes: (A) torn pointer read of `idx.batch`, (B) stale-pointer use by a goroutine outside the batch-owning goroutine, (C) companion-map race for BM25 posting cache and EdgeStore adjacency cache. The current engine write lock makes all three impossible today, but the pattern has already caused one concrete incident (P1-78 `CollCache.AddMember` deadlock inside `BatchIndexWrites`), the implicit invariant ("caller must hold the engine write lock for the full batch lifetime") lives in doc comments and rots, and every new index joining the batch pattern extends the tax.

Four options were weighed:

(a) **Document the invariant, defer the fix to a future fine-grained-locking pass.** Zero LOC. Rejected because documented invariants are the first thing to rot, and the P1-78 incident showed the class is already producing real bugs rather than just hypothetical ones.

(b) **`atomic.Pointer[bolt.Tx]` on the stashed field.** ~20 LOC, fixes failure mode A only. Rejected because it *looks* safer than it is — future readers see `atomic.Pointer` and assume the whole batch state is race-free, but the companion maps remain unprotected. Half-fixes are worse than documented fragile state because they invite misplaced confidence.

(c) **`sync.Mutex` gating the SetBatch lifetime.** ~60 LOC. Serializes two concurrent `SetBatch` calls but doesn't prevent a third-party goroutine from calling `Add` while a batch is installed and seeing someone else's tx. Still fails mode B.

(d) **Explicit threading via `WriteSession`.** ~400-500 LOC. Picked. Only option that closes all three failure modes. The type signature is self-documenting: `func(ws *WriteSession) (bool, error)` makes the batched path obvious at every call site. No implicit invariant to uphold. The 139-call-site blast radius feared for "full tx threading" doesn't materialize because non-batched callers use the existing `AddEdge`/`SetProp`/etc. unchanged — only code inside `WithWriteBatch` closures changes, which is ~6 call sites across `curation/observe.go` and `curation/deterministic.go`.

One architectural concession accepted: `Graph.AddEdgeTx` takes a `*bbolt.Tx` parameter, which leaks bbolt into the graph package. The graph package could in principle serve a non-bbolt edge store (MemoryEdgeStore exists for tests), so the parameter is nominally storage-specific. This is honest leakage — Gramaton ships one production backend (bbolt), the MemoryEdgeStore impl ignores the tx, and the alternative (neutral opaque handle via a new `txbatch` package) adds a package boundary for a use case that has one real implementation. Documented as a known concession rather than pretending the graph layer is storage-agnostic.

The three-stage landing keeps each commit reviewable and reversible. Stage 1 was a pure refactor (no API change, internal restructure of companion caches); Stages 2 and 3 are coupled (interface signature changes require caller updates to compile) and land together in a single commit to avoid broken intermediate state.

Detailed stage-by-stage execution plan: [p2-06-writesession-plan.md](p2-06-writesession-plan.md).

---

### D39: Cost Caps Supplement Count Caps, Not Replace Them

**Decision:** Gramaton gains two USD-denominated safety caps on LLM spend: `llm_curation.max_cost_usd_per_run` (per curation cycle) and `llm.max_cost_usd_per_day` (across the day). Both default to 0 (disabled). They coexist with the existing `max_calls_per_run` / `max_calls_per_day` / `max_calls_per_session` count caps rather than replacing them. When a cost cap is enabled and tripped, curation breaks out of the current cycle; the daily cap pauses `llm.Metered` so all subsequent LLM calls return `ErrCapped` until the daily boundary rolls over. Cost is estimated via `llm.EstimateCost` using the per-task token counts reported by providers plus the per-model pricing table in `llm/pricing.go`.

**Why:** Count caps were the only safety net when D38 landed, and they had a visible failure mode: the contradiction-detection pool-drain bug burned 950 Sonnet calls (~$17) at a steady ~60 calls/hour while staying comfortably within the default 20 calls/cycle count cap — because cycles reset. What matters for a cost incident isn't "how many calls" but "how many dollars." A USD cap addresses that directly.

Three options were considered:

(a) **Replace count caps with cost caps.** Simpler config surface; one knob per scope. Rejected because it requires every model to have a pricing entry — the pricing table covers anthropic (claude 3/4 tiers) and openai (gpt-4o, gpt-4o-mini), but CLI-shim providers (`claudecli`, `kirocli`) currently report 0 tokens, and any user-added custom model or future Bedrock Titan/Llama entry would silently evade the cap. Cost-only means the safety net has holes proportional to how many models are missing from the table.

(b) **Cost cap on a new `LimitsConfig` field; count cap stays on `LLMConfig`.** The instinct was that dollars feel "limits-like" and belong with `max_json_size`. Rejected because symmetry matters more than category: `max_calls_per_day` already lives on `LLMConfig` for good reason (it's an LLM-specific cap, not a request-body cap), and splitting count cap from cost cap by section forces operators to look in two places to reason about spend.

(c) **Cost cap as a supplement: new fields next to the existing count caps, same scope units.** Picked. `LLMCurationConfig.MaxCostUSDPerRun` sits next to `MaxCallsPerRun`; `LLMConfig.MaxCostUSDPerDay` sits next to `MaxCallsPerDay`. Both operate post-call — they read accumulated cost after each completion and break on the next iteration. Unknown-model cost reads as 0, so in that regime the count cap is the real backstop.

The two caps answer different questions: "have we used too many calls" (cheap, always-correct) vs "have we spent too much" (accurate when pricing data is available). Keeping both means operators can tune the cost cap as the primary knob and leave the count cap as the "just in case" fallback. Docs explicitly tell operators to keep count caps set when adding a cost cap — the combination is much safer than either alone.

The per-run cost cap is a post-call check: the cycle may exceed the cap by one in-flight call's worth of cost before the next iteration breaks, since the recorder only updates after the provider response parses. Acceptable for the intended use (bound damage, not guarantee dollar-precision).

---

### D38: Persist Contradiction-Check Negative Results as `no_contradiction` Edges

**Decision:** The autonomous contradiction-detection pass (`curation/autonomous.go` `detectContradictions`) now writes a `no_contradiction` edge with a `checked_at` timestamp property on every pair the LLM evaluated and reported as neither contradicting nor superseding. The read-phase `hasEdge` guard (which was already treating any inter-pair edge as "already handled") now also skips these marks, which drains the candidate pool across cycles on any store where the LLM finds no contradictions. Positive findings continue to produce `contradicts` or `supersedes` edges as before. No config knob for a recheck-interval TTL in this change — the `checked_at` property is recorded so a future TTL can be added without migration, but day-one behavior is "once checked, permanently marked."

**Why:** Prior behavior had a feedback-loop cost bug. The read phase found candidate pairs in the `ContradictionMinSim`..`ContradictionMaxSim` similarity window (defaults 0.5..0.85) and skipped pairs with any existing edge. The LLM phase sent pairs to a Sonnet-grade model. The write phase added edges **only** for `contradicts` or `supersedes` results. "No contradiction" / "unrelated" / any other verdict produced zero persistent state. Next cycle, the shuffle picked the same (or different) pairs from the same pool; the LLM re-confirmed "no contradiction"; nothing was written; repeat. On 2026-04-19→20, this pattern burned ~16 hours unbroken at ~60 Sonnet calls/hour, ~950 contradiction_batch calls total, 0 contradictions found, ~$17 in API cost. The credit card exhausted before the pool drained because the pool was structurally un-drainable on a store where the LLM's correct answer for every pair was "no." The bug predates the current autonomous pipeline (the negative-result drop was never implemented in the first place); audit would not surface it because the code path was consistent-but-pointless rather than inconsistent.

Three fixes were considered:

(a) **Widen the cycle interval + narrow the similarity band + route to Haiku.** Reduces bleed rate; does not fix the structural issue. On a large store, the pool still grows faster than the drain even at 5-minute intervals.

(b) **Write a `no_contradiction` edge on every negative result, with a `checked_at` timestamp.** Picked. The existing `hasEdge` guard becomes the drain mechanism. Pool drains linearly at `MaxContradictionChecks` pairs per cycle until empty, then per-cycle cost goes to zero for stable stores and rises only with genuine new-pair creation (new captures, session commits, ingest). Adding a TTL config to re-check stale negatives later requires no migration because `checked_at` is already persisted; the read-phase guard just needs a stale-edge carveout.

(c) **Disable the task entirely.** Rejected as default — the task finds real contradictions when they exist, and keeping autonomous curation unbalanced (contradictions off, classification on) would be surprising. Operators who don't want the task can still set `llm_curation.max_contradiction_checks: 0`.

Option (b) is correct even if contradiction detection is rarely valuable: the cost on a "nothing to contradict" store is now zero, which means the feature is usable by default without requiring operators to tune it to avoid bleed.

---

### D37: Collapse `dedup.action` to `supersede | reject`

**Decision:** `DedupConfig.Action` accepts only `supersede` (default) and `reject`. The previous `flag` value was removed. Legacy configs with `action: flag` are silently coerced to `supersede` at load (`config.Load()`) for one release cycle; any other unrecognized value errors at load so typos surface. The three capture paths (`api/capture.go`, `api/sessions.go`, `server/service_records.go`'s `serviceCapture`) all explicitly describe the default behavior as "supersede" in their comments; curation's dedup pass (`curation/deterministic.go`) was already threshold-driven and unchanged.

**Why:** A 2026-04 audit of the dedup pipeline surfaced that `flag` and `supersede` had identical behavior across all three capture readers — both wrote `valid_until` + `resolution=superseded` + a `supersedes` edge. The config comment described `flag` as "mark but don't delete," implying a warn-only mode, but no code implemented that intent. `config_test.go` asserted the default was `flag` and that `reject` round-tripped, but nothing exercised a behavioral difference between `flag` and `supersede`. The fused behavior predated T-02 (confirmed by diffing `server/service_records.go` at `3f37f48^`). So the enum carried two functionally-equivalent values whose config-level distinction was a lie to operators.

Three resolution options were considered before picking (b):

(a) **Implement a real warn-only `flag` mode** — skip the supersession block at capture, instead return a warning on `CaptureResponse.Warnings` and optionally add a `possible_duplicate` edge. Rejected because curation's dedup pass runs every minute (default) and does not read `Dedup.Action` — it would silently undo the "keep both records" state within 60 seconds. Making `flag` meaningful at capture time would require also teaching curation to respect the action, which forces either (i) persisting the capture-time action on records/edges (surprising when the global config later changes), or (ii) applying the current config retroactively (triggers cascading supersessions on a `flag`→`supersede` switch). Both are worse than the status quo.

(b) **Collapse to `supersede | reject`** — picked. Resolves the user-visible complaint (two functionally identical values) with minimum scope. Preserves existing behavior under the explicit `supersede` label. Legacy configs coerce silently since behavior doesn't change.

(c) **Add a `NearDuplicates` response field** listing below-threshold near-misses on every capture — rejected. Use cases turned out to be thin: agents following the recommended "search-before-capture" pattern already have this information earlier and richer; operators wanting post-hoc near-miss visibility can use `gramaton_duplicates` with a lower threshold; operators wanting bulk-ingest dry-run are better served by a dedicated `--dry-run` flag on the ingest path. Adding a default-empty response field to every capture call for a marginal convenience wasn't worth the surface expansion.

The warn-only mode remains possible as a future feature if someone articulates a concrete need (most likely as part of a bulk-ingest dry-run story); the enum can be extended then rather than pre-implementing a capability without clear demand.

---

### D36: Tiered LLM Models with Per-Task Effort Dials

**Decision:** LLM configuration splits into a single `llm.model` (default for general calls) and an optional `llm.models` block with three tiers — `low`, `medium`, `high`. The autonomous curation pipeline (`llm_curation:`) names an effort level per task (`classification_short_effort`, `classification_long_effort`, `summarization_effort`, `contradiction_effort`, `concept_effort`, `manifest_effort`), and each effort maps to one of the three tiers. Default assignments are Haiku-grade for short classification / summarization / manifest rollup, Sonnet-grade for long classification / contradiction detection / concept synthesis.

**Why:** Curation tasks span a wide range of cognitive demand. Enum picking for classification of a one-sentence record doesn't need Sonnet; contradiction detection across near-duplicate semantically-similar records benefits from Sonnet. Routing them all through one model either overpays for easy work or underserves hard work. Three tiers (Haiku / Sonnet / Opus-grade) plus per-task effort dials let operators tune cost/quality per workload rather than per call site. Effort names rather than raw model names keep the config portable across providers (Anthropic, OpenAI, Bedrock) — each provider maps low/medium/high to its own family.

---

### D35: Named Stores via LoadWithFallback

**Decision:** Gramaton supports multiple isolated stores per binary via the `gramaton --store <name>` flag. Each named store lives at `~/.gramaton/stores/<name>/` with its own `data/` and an optional per-store `config.yaml`. Per-store config uses `LoadWithFallback` (`config/config.go`): if the store config file exists, it is loaded standalone; if it does not, the global config is used. The unnamed default store remains at `~/.gramaton/data/`.

**Why:** Different workloads need different isolation. A benchmark run that ingests 20k LongMemEval sessions would wreck retrieval quality in a personal knowledge store. A per-project store keeps domain-specific knowledge from bleeding into general memory. Named stores give a clean "which context am I in right now" boundary without forcing users to juggle multiple binaries or data directories outside `~/.gramaton/`. Defers D27's "one store per user default" — the default is still one store, but the escape hatch is now first-class rather than a `--data-dir` workaround.

Known sharp edge: per-store config load is full-replace, not deep-merge. A partial per-store `config.yaml` that only overrides one section causes the other sections to be zero-valued rather than inherited from the global — silently disables features or causes startup to refuse. Tracked for improvement; workaround is to copy the full global config into the store directory and edit only what needs to change.

---

### D34: Pure-Go BERT as Default Embedder

**Decision:** The default embedding provider is a pure-Go BERT encoder built into the Gramaton binary (`embed/bert/`), running `bge-small-en-v1.5` (BAAI, 384-dim). Model weights download from HuggingFace on first use and cache at `~/.gramaton/models/<model>/`. Ollama, OpenAI-compatible, and AWS Bedrock remain available as alternatives. Supersedes D21's Ollama-as-default stance.

**Why:** Single-binary install, no external runtime, offline-first after first-run model download. The Quick Start becomes `go install && gramaton init` — one tool, one command, no separate daemon to manage. A ~130MB model download is acceptable for a knowledge store that otherwise owns its state end-to-end. Ollama remains the right escape hatch for users who want larger or multilingual encoders than the shipped BERT.

Open follow-up (dev collection item): the amd64 build currently falls back to a pure-Go matmul (`embed/bert/matmul_generic.go`); arm64 has a hand-written NEON kernel (`matmul_arm64.s`). An AVX2 kernel for amd64 is the direct path to closing the perf gap on Intel/AMD hardware. User accepted this regression to proceed with the default-flip rather than gate it on assembly work.

---

### D33: api/ as the Canonical Operations Surface (T-02)

**Decision:** Every operation Gramaton exposes — capture, search, session prepare/commit, collection lifecycle, versioning — is defined once in the `api/` package as a `XxxRequest` / `XxxResponse` / `XxxDescription` / `func (a *API) Xxx(...)` tuple. Every transport (HTTP, MCP stdio + Streamable HTTP, CLI MCP proxy) consumes those types and that method through a hand-written binding table. No per-transport request struct, no codegen, no reflection. Locking discipline (`engine.Lock()` / `Unlock()`) lives inside api methods; transport handlers never hold locks.

**Why:** Before T-02, the same operation had up to three distinct shapes — an HTTP request struct in `server/`, an MCP tool args struct, a CLI flag set — which drifted. Bugs like "the MCP tool accepts this field but HTTP doesn't" were recurrent and expensive. Collapsing to one definition eliminates that whole bug class. Hand-written bindings (rather than codegen) keep the transport layer honest: when you add a transport, you read the api type, map the wire format, and call the method. The work is mechanical and local. The `jsonschema` tags on request fields double as MCP tool descriptions (the MCP SDK reads them via reflection when the struct is passed as a tool args type), which means a single tag update propagates to both HTTP and MCP simultaneously.

---

### D32: Two-Tier Session Model with promote_to_memory

**Decision:** A session is a two-phase structure. Phase 1 is `gramaton_session_prepare`, which returns extraction instructions and the session state so far. Phase 2 is `gramaton_session_commit`, which submits extracted segments. Each segment creates a Session segment node (BM25-indexed, part of the conversational thread) and, when `promote_to_memory: true` (the default), a linked Memory record (vector-embedded, full lifecycle, auto-supersession). Segments with `promote_to_memory: false` stay Session-only — searchable within session-scoped queries, not competing in Memory's vector space. An `extracted_as` edge links each Session segment to its Memory record.

**Why:** Two distinct retrieval patterns need distinct storage. Conversational recall ("what did we discuss in the April 12 session?") wants thread-preserved, session-scoped results; semantic knowledge retrieval ("what did we decide about auth?") wants ranked cross-session results with epistemic metadata. Keeping them separate lets each retrieval path give good answers without the other's noise. The `promote_to_memory` flag gives agents a way to capture exploration, dead ends, and open questions honestly — findable by session lookup without polluting Memory's vector space with speculation. This supersedes the older "observe" pipeline (`gramaton_observe`) which ran a quality-gate model over raw conversation chunks — extraction quality is dramatically better when an LLM with context synthesizes segments than when a server-side quality gate classifies pre-extracted facts.

---

### D31: Three Storage Paths with the Decision Rule

**Decision:** Gramaton offers three first-class storage paths with distinct retrieval guarantees. Memory: best-match fuzzy retrieval by composite score (vector similarity + BM25 + freshness + activation + confidence). Sessions: automatic two-phase extraction from conversations with optional promotion to Memory. Collections: named containers with optional schema enforcement; `_items` returns every matching item exhaustively. The decision rule: "Will missing one item be a failure? Yes → Collection. No → Memory (direct or via session extraction)."

**Why:** One retrieval guarantee does not fit all knowledge shapes. A task list where a missing item is a bug has completely different retrieval needs from an architecture decision where low-relevance results can be ignored. Ranking a backlog by composite score is a bug; enumerating every architecture decision for every question is waste. Exposing the three guarantees as three paths with distinct tools makes the integration decision ("which path?") legible rather than forcing a generic store to approximate all three. The decision rule is the single most important integration choice an agent-builder makes; every other retrieval design question flows downstream of it.

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
