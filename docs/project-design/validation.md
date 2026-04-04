# Validation Strategy

How we prove Gramaton's core hypothesis: that epistemic metadata makes LLM retrieval meaningfully better than raw vector search.

This is separate from unit/integration testing (correctness). This is about measuring whether the product works — does metadata actually help?

## What We're Measuring

### 1. Retrieval Relevance

Does metadata filtering surface better results than vector search alone?

**Test:** Same query against the same knowledge base, two configurations:
- **Baseline:** Vector similarity only (embeddings, top-k, no metadata)
- **Gramaton:** filter → rank → traverse (metadata filtering, then similarity, then graph)

**Evaluation:**
- Does Gramaton exclude stale/low-confidence records that baseline returns?
- Does Gramaton surface superseding records that baseline misses?
- Does Gramaton find related knowledge via graph traversal that baseline can't reach?

**Example scenarios:**
- Query "caching strategy" when the store has a superseded Redis record (confidence downgraded) and a current Memcached record. Baseline returns both ranked by similarity. Gramaton should rank the current record higher via confidence and supersession metadata.
- Query "retry logic" when the store has a durable architecture decision and a temporal sprint-specific workaround. Baseline treats them equally. Gramaton should rank the durable record higher and potentially filter out the temporal one.

### 2. Agent Reasoning Quality

Does the agent give better answers when it has metadata and provenance?

**Test:** Give an agent the same question, same knowledge base, two tool sets:
- **Baseline tools:** `search(query) → [{id, content}]` (content only)
- **Gramaton tools:** `search`, `inspect`, `explore`, `log`, `diff` (full metadata + versioning)

**Evaluation (human judgment):**
- Does the agent qualify its answers based on confidence/epistemic status?
- Does the agent mention when knowledge is contested or has been superseded?
- Does the agent explain provenance ("based on the architecture decision from February, which was validated by load testing in March")?
- Does the agent avoid recommending superseded approaches?

### 3. Token Efficiency

Does the retrieval funnel reduce token spend?

**Test:** Measure total tokens consumed during retrieval for the same set of queries:
- **Baseline:** Agent reads full content of top-k results
- **Gramaton:** Agent reads keywords/short summaries first, inspects only the relevant ones

**Evaluation:**
- Total input tokens consumed before the agent has enough context to answer
- Number of records the agent reads in full
- The funnel should mean the agent reads fewer records in full while still finding the right ones

### 4. Knowledge Evolution Queries

Can the agent answer "what changed" questions?

**Test:** Queries that require understanding change over time:
- "What changed about our auth architecture since the rewrite?"
- "When did we stop using Redis for caching, and why?"
- "Has our retry strategy been updated recently?"

**Evaluation:**
- **Baseline:** Can't answer these at all — no temporal awareness
- **Gramaton:** Should answer accurately using `gramaton diff` and `gramaton log`

These queries are binary — either the system supports them or it doesn't. If Gramaton handles them well, that's a clear differentiator.

### 5. Concept Emergence Quality

Do emergent concept nodes actually improve retrieval?

**Test:** Seed a store with records that mention "Kafka" across 5+ records but no concept node exists yet. Run curation to trigger concept promotion. Then:
- Query "what do we know about event streaming" — does the Kafka concept node help surface all related records?
- Query "Kafka" — does the concept node provide a better entry point than searching individual records?

**Evaluation:**
- Compare recall (how many relevant records found) with and without concept nodes
- Compare the graph traversal path — does landing on a concept hub fan out to more useful knowledge?

### 6. Capture Quality

Does the agent capture the right things with the right metadata?

**Test:** Feed an agent a set of reference conversations containing known important knowledge — decisions, preferences, facts, procedures, speculative discussions, and trivial exchanges. The conversations should cover multiple domains to test tenet 7 (domain-neutral).

Compare what the agent captures against a human-labeled gold standard.

**Evaluation:**
- **Precision:** Did it capture things that should have been captured? Or did it capture noise (trivial exchanges, questions without answers)?
- **Recall:** Did it miss important decisions, preferences, or facts?
- **Classification accuracy:** For captured records, are temporality, confidence, knowledge_type, and epistemic_status reasonable compared to human judgment?
- **Context envelope quality:** Did the agent fill in the structured fields? Are the "findable by" terms useful? Would the record be found by someone searching for the topic/project/entity?
- **Decomposition quality:** For complex content (decisions with reasoning), did the agent decompose appropriately? Are sub-records linked with the right edge types?

This is the highest-risk area of the system. If capture quality is poor, no amount of good retrieval helps. Measure early, iterate on the system prompt and subagent template based on results.

## Test Knowledge Base

A synthetic but realistic knowledge base for validation. Should include:

**Record variety:**
- Architecture decisions (durable, high confidence)
- Sprint-specific facts (temporal, medium confidence)
- User preferences (durable, high confidence)
- Superseded decisions (durable, was high confidence, now low)
- Speculative discussions (temporal, low confidence)
- Refuted claims (well, if contextual_role is in scope)
- Procedural knowledge (durable, step-by-step)
- Reference data (immutable)

**Temporal variety:**
- Records from different dates
- Some records that were modified (confidence changed, superseded)
- A commit history showing evolution over time

**Graph variety:**
- Concept nodes with multiple inbound edges
- Records that contradict each other
- Records linked by justifies/defeats/supersedes edges
- Chains: decision → constraints → rejected alternatives

**Scale:** Start with ~200-500 records. Enough to be non-trivial but small enough to manually verify results.

## How to Build the Test Harness

**Phase 1: Manual evaluation.** Seed the test store. Run queries by hand. Compare results qualitatively. This is enough for early validation — does it feel better?

**Phase 2: Scripted evaluation.** Write a set of query/expected-result pairs. Run both baseline and Gramaton configurations. Score automatically where possible (did record X appear in top 5?), flag for human review where needed.

**Phase 3: LLM-as-judge.** Use an LLM to evaluate answer quality. Give it the question, the knowledge base ground truth, and the two answers (baseline vs Gramaton). Ask it to score which answer is better and why. Not perfectly reliable, but scalable.

## When to Build This

Not first. The test harness needs a working Gramaton to test against. But the test scenarios should be written before the system is feature-complete — they define what "working" means.

**Build order:**
1. Build core engine (graph, indexes, storage, CLI)
2. Seed a test knowledge base
3. Run Phase 1 (manual queries, qualitative evaluation)
4. Iterate on retrieval scoring, metadata filtering, decay formulas based on results
5. Build Phase 2 (scripted evaluation) for regression testing
6. Phase 3 when the system is stable enough to benchmark
