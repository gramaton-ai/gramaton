# Data Model

## Fundamentals

Gramaton uses a property graph. The two primitives are **nodes** and **edges**.

### Node and Edge IDs

All nodes and edges are identified by **ULIDs** (Universally Unique Lexicographically Sortable Identifiers).

Example: `01H5K9E2GJ7A8NQXR5VT3M4BCW`

- 26 characters, no hyphens, case-insensitive
- Time-sortable (first 10 characters encode millisecond timestamp)
- Globally unique without coordination
- CLI-friendly — short enough to type, no ambiguous characters (Crockford Base32 omits I/L/O/U)
- Go library: `github.com/oklog/ulid` (MIT, 4.4k stars, stable)

The node ID is **stable across mutations** — when properties change, the ID stays the same. The content hash (used by the storage layer for content-addressing) is a separate value that changes with every property update. They serve different purposes:

```
id:           01H5K9E2GJ7A8NQXR5VT3M4BCW   (stable, assigned at creation)
content_hash: a3f8c2b7d1e4...               (changes when properties change)
```

### Nodes

A node is a bag of typed key-value properties with a unique ULID. The graph engine doesn't know or care what the properties mean — it stores, indexes, and retrieves them.

Properties are always flat. No nested objects, no maps. If something needs to be queried independently or shared between nodes, it's a separate node connected by an edge.

A property is either present or absent. No nulls.

### Edges

An edge is a first-class object connecting two nodes. It has:

- **ID** — unique identifier
- **Source ID** — the node this edge comes from
- **Target ID** — the node this edge points to
- **Type** — a string describing the relationship (e.g., `part_of`, `justifies`, `related_to`)
- **Weight** — a float (0.0–1.0) representing strength
- **Properties** — optional typed key-value pairs (same rules as node properties)

Three indexes are maintained for efficient traversal:
- source_id → edges (outbound from a node)
- target_id → edges (inbound to a node)
- type → edges (all edges of a given type)

### Property Types

Eight types. No exceptions.

| Type | Description | Example |
|------|-------------|---------|
| `String` | UTF-8 text, any length | `"We chose Kafka..."` |
| `Float64` | 64-bit floating point | `0.85` |
| `Int64` | 64-bit integer | `42` |
| `Bool` | true/false | `true` |
| `Timestamp` | UTC datetime | `2026-04-03T14:30:00Z` |
| `Vector` | Array of float32 | `[0.023, -0.118, ...]` |
| `StringList` | Array of strings | `["kafka", "rabbitmq"]` |
| `Bytes` | Raw byte array | (bloom filters, binary data) |

### Indexes

**Property indexes** — Enable fast filtering on any property field. Support exact match, range queries, and substring search.

**Vector indexes** — Enable similarity search on Vector properties. HNSW for large candidate sets, flat scan for small filtered sets. Dynamic switching based on candidate set size after metadata filtering. Both behind a `VectorIndex` interface.

---

## Metadata Schema (v1)

The graph engine stores properties without understanding them. The metadata schema is a convention enforced by the tool layer — it defines what properties knowledge records have and what values are valid.

### Summary Pyramid

Multiple representations of the same content at different token costs. Stored as properties on a single node. A retrieval optimization — cheap triage before expensive reads.

| Property | Type | Description | Token Cost |
|----------|------|-------------|------------|
| `content_keywords` | StringList | Extracted topic keywords | ~10 tokens |
| `content_short` | String | Max ~200 characters. Brief summary for relevance scanning. | ~50 tokens |
| `content_abstract` | String | Max ~2000 characters. Paragraph-level summary. | ~500 tokens |
| `content_full` | String | Complete processed content. No limit. | Variable |
| `source_ref` | String | Pointer to raw unprocessed source on filesystem. | N/A |

Each content level has a corresponding embedding:

| Property | Type | Description |
|----------|------|-------------|
| `embedding_keywords` | Vector | Embedding of keywords |
| `embedding_short` | Vector | Embedding of short summary |
| `embedding_abstract` | Vector | Embedding of abstract |
| `embedding_full` | Vector | Embedding of full content (or first chunk) |

All Vector properties are independently searchable via the vector index.

### Epistemic Metadata

| Property | Type | Description |
|----------|------|-------------|
| `temporality` | String | How long this knowledge remains valid. Values: `immutable` (definitional truths), `durable` (stable until contradicted), `temporal` (time-bound, will decay), `ephemeral` (minutes/hours lifespan). |
| `confidence` | Float64 | 0.0–1.0. How likely is this to be correct? Used for retrieval scoring. |
| `importance` | Float64 | 0.0–1.0. Retrieval priority. Prevents high-importance records from decaying. Initially assigned by agent, adjusted by curation. |
| `epistemic_status` | String | Qualitative epistemic state. Values: `well_established`, `probable`, `speculative`, `contested`, `refuted`. Distinct from confidence — a refuted record can have high confidence (we're confident it's wrong). |
| `knowledge_type` | String | Values: `episodic` (what happened), `semantic` (general facts), `procedural` (how to do something), `conceptual` (abstract principles/definitions), `reference` (lookup data). |
| `contextual_role` | String | Why false/superseded knowledge is retained. Values: `foundational` (needed to understand what replaced it), `illustrative` (useful as example/teaching tool), `attributed_belief` (the belief itself is knowledge), `counterfactual` (hypothetical used for reasoning). Absent for normal knowledge. Only set on records with `epistemic_status: refuted` or `deprecated`. |

### Lifecycle Metadata

| Property | Type | Description |
|----------|------|-------------|
| `created_at` | Timestamp | When the record was created in the system. |
| `last_accessed` | Timestamp | When the record was last returned to a consumer. Updated on any retrieval. |
| `access_count` | Int64 | How many times returned to a consumer. Internal engine operations don't count. |
| `activation_boost` | Float64 | Accumulated indirect activation from neighboring node access. Decays over time. Updated by spreading activation, never by direct access. See [Retrieval — Scoring Model](retrieval.md#scoring-model). |
| `valid_from` | Timestamp | When this knowledge became true in the world (bitemporal). |
| `valid_until` | Timestamp | When this knowledge stopped being true. Absent if still valid. |
| `processing_status` | String | Values: `captured` (raw, awaiting classification), `processed` (LLM-enriched), `stuck` (exhausted classify retries — see `classify_attempts` and `last_classify_error` for triage), `deleted` (soft-deleted, retained for provenance). |
| `classify_attempts` | Int64 | How many times autonomous classification has failed on this record. Reset to 0 on successful classify. At `llm_curation.max_classify_attempts` (default 3), `processing_status` flips to `stuck`. |
| `last_classify_error` | String | Truncated reason (max 200 runes) for the most recent classify failure. Captured for operator triage. May contain provider error fragments — redact before sharing exports. |
| `summary_attempts` | Int64 | How many times summary generation has failed on this record. Reset to 0 on successful summary. At `llm_curation.max_summary_attempts` (default 3), the record is skipped at summary selection time on subsequent cycles. |
| `last_summary_error` | String | Truncated reason (max 200 runes) for the most recent summary failure. Same redaction guidance as `last_classify_error`. |
| `synthesis_attempts` | Int64 | (Concept nodes only.) How many times concept synthesis has failed on this concept. Reset to 0 on successful synthesis. At `llm_curation.max_synthesis_attempts` (default 3), `synthesis_status` flips to `stuck`. |
| `last_synthesis_error` | String | (Concept nodes only.) Truncated reason (max 200 runes) for the most recent synthesis failure. Same redaction guidance as `last_classify_error`. |
| `embed_attempts` | Int64 | How many times gramaton_reembed has failed to produce an embedding for this record. Reset to 0 on successful re-embed. At `llm_curation.max_embed_attempts` (default 3), the record is excluded from reembed candidate selection. |
| `last_embed_error` | String | Truncated reason (max 200 runes) for the most recent embed failure. Same redaction guidance as `last_classify_error`. |
| `observation_extract_attempts` | Int64 | How many cycles the deterministic observation extractor has failed to embed observations for this parent. Reset to 0 on successful extraction. At `curation.max_observation_attempts` (default 5), the parent is skipped at observation-extraction selection time. |
| `last_observation_extract_error` | String | Truncated reason (max 200 runes) for the most recent observation-extract failure. Same redaction guidance as `last_classify_error`. |

### Provenance

| Property | Type | Description |
|----------|------|-------------|
| `source_credibility` | Float64 | 0.0–1.0. How reliable is the source? Separate from record confidence. |
| `testimony_hops` | Int64 | Distance from primary source. 0 = primary, 1 = someone told me, etc. |

### Context Envelope (Capture Provenance)

Stored from the agent's context envelope at capture time. All optional — absent if the field was empty. Not indexed for search (extracted keywords handle that). Stored for inspectability: when reviewing a record, you can see the full situational context that existed at capture time.

| Property | Type | Description |
|----------|------|-------------|
| `context_about` | String | What this is about — topic, domain, subject area. |
| `context_who` | String | Who or what is involved — people, organizations, entities, systems. |
| `context_prompted` | String | What prompted this — why this knowledge emerged right now. |
| `context_findable_by` | String | What this should be findable by — terms, names, IDs for future retrieval. |
| `context_related` | String | What else in the store relates to this — known related topics or records. |

### Embedding Provenance

| Property | Type | Description |
|----------|------|-------------|
| `embedding_model` | String | Identifier of the model that generated this node's embeddings (e.g., `bge-small-en-v1.5`, the current default). Set at embedding time. Used to detect model changes and flag stale embeddings. |

---

## Node Types (by Convention)

The graph engine treats all nodes the same. "Types" are expressed through property values, not separate structures. The tool layer enforces conventions.

### Knowledge Record

The primary node type. A piece of knowledge with provenance, metadata, and content.

All 20 metadata properties above apply. Created by `gramaton capture`. May have child chunk nodes for long content.

### Concept Node

A source-independent concept that accumulates evidence over time. Acts as a hub in the graph — many knowledge records link to it. Analogous to neocortical semantic memory — general knowledge extracted from many specific episodes.

Key properties:
- `knowledge_type: "conceptual"`
- `temporality: "immutable"` or `"durable"`
- `content_full`: concise definition
- `content_keywords`: canonical name + aliases
- `evidence_count`: Int64 — how many knowledge records reference this concept

#### How Concept Nodes Emerge

Concept nodes are **not created on first mention.** They emerge through evidence accumulation — the same mechanism the brain uses to form semantic concepts from episodic memories.

1. **At capture time:** Entities and topics are extracted as **keywords on the knowledge record**, not as concept nodes. "kafka", "rabbitmq", "rate-limiting" are keywords.

2. **Emergence via accumulation:** When a keyword appears across multiple records (crossing the `emergence_threshold`, default: 3), it graduates to a concept node. This happens two ways:

   - **Reactive (at capture):** The subagent searches for existing records sharing a keyword. If enough records reference it and no concept node exists, the subagent creates one and links all related records.
   - **Proactive (during curation):** The curation skill scans keywords across records, finds frequently-occurring keywords without concept nodes, and promotes them.

3. **Exception — direct concept capture:** If the agent is capturing something that IS a concept definition ("Kafka is a distributed event streaming platform..."), it creates the concept node immediately regardless of evidence count. Classified as `knowledge_type: conceptual`.

4. **Concept nodes accumulate over time.** Each new record that references a concept increments `evidence_count` and may refine the definition. The concept outlives any single source.

This is grounded in neuroscience: the hippocampus stores individual episodes (knowledge records), the neocortex slowly extracts patterns across episodes (concept nodes). The curation layer acts as the neocortex.

```yaml
# Config
concepts:
  emergence_threshold: 3   # records sharing a keyword before it becomes a concept node
  auto_promote: true        # subagents create concept nodes when threshold is met during capture
```

### Chunk Node

A fragment of a long document. Created automatically by the **server** when content exceeds the chunking threshold. Chunks are retrieval infrastructure — they make long documents searchable by embedding, not meaningful standalone knowledge.

Key properties:
- `content_full`: the chunk text
- `embedding_full`: the chunk embedding
- Edge: `chunk_of` → parent knowledge record

Chunk nodes do NOT have their own metadata (no temporality, confidence, keywords, etc.). They inherit the parent's classification for filtering purposes.

**Search behavior:** When a chunk matches a query, `gramaton search` returns the **parent record's ID**, not the chunk ID. The chunk is a match path, not a result.

### Sub-Record Node

A semantically meaningful piece of knowledge extracted from a larger record by the **subagent**. Created when the agent decomposes a complex decision, analysis, or procedure into constituent parts.

Example: "We chose Kafka over RabbitMQ" decomposes into sub-records for each constraint, the decision itself, and rejected alternatives.

Sub-records are full knowledge records — they have their own metadata, keywords, and classification. They appear independently in search results.

Key properties:
- All standard metadata properties (temporality, confidence, knowledge_type, etc.)
- Own keywords and summary pyramid
- Edges to parent via semantic types: `justifies`, `constrains`, `defeats`, `part_of`, etc.

### Chunk Nodes vs Sub-Record Nodes

A record can have both. A 15-page architecture doc gets chunked by the server (for embedding coverage) AND decomposed by the subagent (for knowledge structure). They serve different purposes and are distinguished by edge type.

| | Chunk Node | Sub-Record Node |
|---|---|---|
| Created by | Server (automatic) | Subagent (LLM judgment) |
| Edge to parent | `chunk_of` | `justifies`, `defeats`, `constrains`, `part_of`, etc. |
| Own metadata? | No — inherits parent's | Yes — independently classified |
| Own keywords? | No | Yes |
| Appears in search results? | No — surfaces the parent | Yes — independently findable |
| Purpose | Embedding coverage for long content | Knowledge structure for complex content |

---

## Relationship Types

Edge types are strings — not a fixed enum. The following are conventions used by the agent integration kit.

| Category | Types | Usage |
|----------|-------|-------|
| **Structural** | `part_of`, `chunk_of`, `has_property` | Parent-child, composition, chunking |
| **Associative** | `related_to`, `similar_to` | General association, often created by vector similarity |
| **Epistemic** | `justifies`, `contradicts`, `supersedes`, `defeats` | Knowledge relationships — reasoning chains |
| **Temporal** | `precedes`, `causes`, `enables` | Time and causation |
| **Referential** | `discusses`, `provides_evidence_for`, `defines`, `exemplifies` | Knowledge record → concept node links |
| **Curation markers** | `no_contradiction`, `contradiction_check_skipped` | Pair-state for the contradiction-detection pipeline. `no_contradiction` is an LLM affirmation that two similar records do not conflict (drains the candidate pool). `contradiction_check_skipped` carries an `attempts` counter for pairs whose check has failed; soft-skip until threshold, hard-skip after. |

---

## Design Constraints

### Flat Properties Only
No nested objects or maps. If you need structured sub-data, model it as a separate node with an edge. The test: "would I ever want to start a query FROM this thing?" If yes → node. If no → property.

### No Nulls
A property is present or absent. No three-valued logic.

### Cascading Edge Deletion
Deleting a node automatically deletes all its inbound and outbound edges. Enforced at the engine level. Prevents dangling edges.

### No Magic Values
All tunable constants (decay rates, activation factors, HNSW parameters, chunk sizes) exposed in config with sensible defaults.

### Storage Layout Isolation
No component assumes anything about how data is physically stored. All access through the `StorageBackend` interface. The on-disk format can change without touching other layers.
