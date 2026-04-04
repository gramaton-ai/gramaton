# Foundations

Gramaton didn't start as a software project. It started as a research question: **how should AI agents remember things?**

The design is grounded in three research tracks — philosophy, neuroscience, and technical knowledge representation — each of which directly shaped specific features. This document captures those influences so the reasoning behind the design is preserved even as the implementation evolves.

---

## Philosophy of Knowledge (Epistemology)

The question: **what does it mean to "know" something, and how is that different from merely storing information?**

### Key Influences

**Justified True Belief (Plato, Gettier).** The classical definition of knowledge requires three things: you believe it, it's true, and you have justification. Gettier showed you can have all three and still be wrong by accident. This directly shaped Gramaton's metadata model — confidence, epistemic status, and provenance are first-class fields, not afterthoughts. A record isn't just stored text; it carries its own justification chain.

**Reliabilism (Goldman).** Knowledge is justified if it was produced by a reliable process. It doesn't matter what the knowledge says — what matters is how it was acquired. This became `source_credibility` and `testimony_hops`. A record from a primary source with high credibility is treated differently from a third-hand claim, regardless of what they say.

**Tacit Knowledge (Polanyi).** Some things you know but can't easily articulate. An expert's intuition, a skill learned through practice. This influenced the summary pyramid — summarization is inherently lossy, so the raw source is always preserved. The summary is for efficient retrieval; the original is the source of truth.

**Knowing-That vs Knowing-How (Ryle).** Factual knowledge ("Paris is the capital of France") behaves differently from procedural knowledge ("how to deploy the service"). They need different metadata and different retrieval patterns. This became the `knowledge_type` field: episodic, semantic, procedural, conceptual, reference.

**DIKW Hierarchy (Ackoff).** Data → Information → Knowledge → Wisdom. Gramaton operates at the Knowledge level — structured, contextualized information with epistemic metadata. Raw data enters through capture; the processing pipeline elevates it to knowledge by adding context, classification, and relationships.

**Conditions of Applicability.** Knowledge that's only true in a specific context. "Use retry with exponential backoff" is good advice for transient failures but bad advice for auth errors. This influenced the design's awareness that knowledge has scope — though the formal `scope` field is deferred to v2.

**Never Delete, Always Supersede (Kuhn).** Old knowledge isn't destroyed — it's marked as superseded and linked to what replaced it. Newtonian mechanics is "wrong" but foundational. A refuted political belief is false but contextually essential. This became tenet 8 and the `epistemic_status: refuted` + `contextual_role` design pattern.

### What Philosophy Gave Us

- Confidence and provenance as first-class fields, not optional metadata
- The epistemic status enum (well_established → refuted) as distinct from confidence
- The principle that false knowledge can be contextually necessary
- The retrieval funnel as an epistemic practice (scanning → skimming → reading → source examination)
- "Never delete, always supersede" as a core tenet

---

## Neuroscience of Memory

The question: **how does the brain organize, consolidate, and retrieve memory — and what can we borrow?**

### Key Influences

**Episodic vs Semantic Memory (Tulving).** The brain stores specific events ("I had coffee with Alice Tuesday") separately from general facts ("Paris is the capital of France"). Episodic memories are time-bound and contextual; semantic memories are abstracted and durable. This directly shaped the `knowledge_type` distinction and the relationship between knowledge records (episodic, source-bound) and concept nodes (semantic, source-independent).

**Complementary Learning Systems (McClelland et al.).** The brain uses two systems: the hippocampus for fast capture of new experiences, and the neocortex for slow integration into existing knowledge. New memories are initially hippocampus-dependent; over time, the statistical patterns are extracted into the neocortex. This became Gramaton's "capture fast, enrich later" model — records are stored immediately with minimal processing, then classified and integrated by curation over time.

**Concept Emergence from Repeated Exposure.** The neocortex doesn't form concepts from a single experience. It slowly extracts patterns across many episodes. This directly became the concept emergence model (D15) — keywords stay as keywords until they appear across 3+ records, then they graduate to concept nodes. The curation layer acts as the neocortex.

**Spreading Activation (Collins & Loftus).** Accessing a memory activates related memories along associative links. Frequently activated memories have lower retrieval thresholds. This became Gramaton's spreading activation mechanism — accessing a node boosts its neighbors along weighted edges. Knowledge that's frequently used stays easy to find.

**Levels of Processing (Craik & Lockhart).** Deep processing (engaging with meaning, building connections) produces more durable memories than shallow processing (surface features). This influenced the summary pyramid — keywords are shallow processing, the abstract is deeper, and the full content with relationship edges is the deepest. Deeper representations are more durable and more useful for retrieval.

**Synaptic Homeostasis / Principled Forgetting (Tononi & Cirelli).** The brain actively prunes low-value memories during sleep to keep retrieval efficient. Not all forgetting is bad. This became the decay system — ephemeral and low-importance records are intentionally deprioritized over time. The brain doesn't hoard; neither should a knowledge store.

**Schemas (Bartlett, Tse et al.).** Prior knowledge structures accelerate learning. New information that fits an existing schema integrates rapidly. This influenced how concept nodes work as retrieval hubs — they're the schemas that new knowledge records attach to. Finding a relevant concept node fans out to all the evidence that supports it.

**Default Mode Network.** The brain's background processing during rest — consolidating, connecting, reorganizing. This became the curation layer. Gramaton's curation is the default mode network: background maintenance that runs during idle time (piggyback curation) or on explicit request.

**Reconsolidation.** When you recall a memory, it becomes temporarily malleable. Each recall is an opportunity to update. This influenced the decision that accessing a record can trigger metadata updates (access count, last accessed) and that curation can modify records during review.

### What Neuroscience Gave Us

- The dual-store model (fast capture → slow integration)
- Concept emergence via evidence accumulation, not declaration
- Spreading activation along weighted edges
- Decay as a feature, not a bug (principled forgetting)
- The curation layer as a default mode network (background consolidation)
- The summary pyramid as hierarchical levels of processing
- Access-based reinforcement (frequently used knowledge stays accessible)

---

## Technical Knowledge Representation

The question: **how have 50+ years of AI and knowledge engineering approached storing and retrieving structured knowledge?**

### Key Influences

**RDF/OWL/SHACL (Semantic Web).** Formal ontologies provide rich structure but create a knowledge acquisition bottleneck — someone has to define the schema before you can store anything. Gramaton takes the opposite approach: schema emerges from the data through concept promotion and curation. The formal structure is a result, not a prerequisite.

**Knowledge Graphs (Hogan et al.).** Property graphs (nodes and edges with typed properties) are more expressive than RDF triples for knowledge with rich metadata. Every relationship can carry its own confidence, timestamp, and provenance. This became Gramaton's data model — a property graph where both nodes and edges have typed key-value properties.

**TBox/ABox Distinction (Description Logics).** Terminological knowledge (definitions, schemas) vs assertional knowledge (specific facts). Concept nodes are TBox-like — they define what a thing IS. Knowledge records are ABox-like — they make claims about specific instances. This distinction is what makes concept nodes useful as graph hubs.

**Generative Agents (Park et al. 2023).** Memory streams with importance scoring, recency weighting, and relevance ranking. The retrieval formula (importance × recency × relevance) is the best published approach to memory retrieval scoring. This directly influenced Gramaton's decay function and the effective_score computation.

**GraphRAG (Edge et al. 2024).** Adding graph structure to RAG improves retrieval by enabling relationship traversal, not just vector similarity. This validated the filter → rank → traverse query pattern — metadata filtering and vector ranking are necessary but insufficient without graph traversal.

**Wikidata's Rank System.** Multiple statements about the same thing can coexist, with one marked as "preferred." This influenced the `epistemic_status` model — a superseded record isn't deleted, it's ranked lower while the successor is ranked higher.

**Open World Assumption.** The absence of a fact doesn't mean it's false — it means we don't know. This is Gramaton's default. If the store doesn't have information about something, that's not evidence against it. The agent should recognize the gap and search elsewhere.

### What Technical KR Gave Us

- Property graph as the data model (over RDF triples or relational tables)
- The TBox/ABox distinction mapped to concept nodes vs knowledge records
- filter → rank → traverse as the primary query pattern
- Importance × recency × relevance for retrieval scoring
- The principle that schema should emerge, not be predefined
- Open world assumption as the default

---

## The Fusion

No single research track produces Gramaton's design. The value is in the intersections:

| Intersection | What It Produced |
|---|---|
| Philosophy × Neuroscience | Confidence as a spectrum (Bayesian credences) + access-based reinforcement (spreading activation) = retrieval scoring that combines epistemic trustworthiness with usage patterns |
| Philosophy × Technical KR | "Never delete, always supersede" (Kuhn) + property graph edges (epistemic relationships) = a graph where supersession, contradiction, and justification are first-class typed relationships |
| Neuroscience × Technical KR | Dual-store model (hippocampus/neocortex) + fast capture with deferred processing = the capture-then-curate architecture |
| All three | The concept emergence model: philosophical insight that concepts are universals extracted from particulars + neuroscience showing the neocortex slowly extracts patterns from episodes + technical KR's TBox/ABox distinction = keywords that graduate to concept nodes via evidence accumulation |
