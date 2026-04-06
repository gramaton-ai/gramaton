# Observe Pipeline: Auto-Extraction with Quality Gates

Date: 2026-04-05
Status: Design approved, pre-implementation

## Problem Statement

Gramaton's current capture model depends on agent diligence. The agent must
recognize knowledge worth keeping, classify it with metadata, and call
gramaton_capture. This produces high-quality records but fails silently when
the agent is busy, distracted, or simply doesn't recognize something as
worth capturing.

The Mem0 deep dive (2026-04-05) showed what happens when you go to the
opposite extreme: capture everything, classify nothing. A production audit
found 97.8% of stored memories were junk. Root causes: no quality gate
between extraction and storage, no feedback loop prevention, no metadata to
distinguish signal from noise.

The right model follows the brain: capture broadly, filter aggressively,
promote selectively, forget deliberately.

## Design Decision: Dual-Mode Capture

Gramaton will have two distinct capture modes:

**Primary mode: Capture** (existing)
Intentional, agent-classified, high-quality. Agent decides what to store,
provides full metadata (temporality, confidence, knowledge_type, etc.).
Records can be `processed` immediately. This is the high-signal channel.

**Secondary mode: Observe** (new)
Passive safety net. Agent sends conversation chunks to the server at natural
breakpoints. Server extracts facts, runs quality gates, stores survivors as
deferred captures. Records always start as `captured/ephemeral/0.3 confidence`.
The existing curation pipeline handles promotion, decay, and cleanup.

These are not competing mechanisms. Capture is for knowledge the agent
recognizes as important. Observe catches what Capture misses.

## Architecture

```
Agent Conversation
        |
        |--- [explicit] ---> gramaton_capture (classified, high-quality)
        |
        |--- [breakpoint] --> gramaton_observe (raw conversation)
                                    |
                                    v
                            [Server accepts, returns immediately]
                                    |
                                    v (async)
                            [1. LLM Extraction] --> candidate facts
                                    |
                                    v
                            [2. Quality Gates] --> skip noise
                                    |
                                    v
                            [3. Deferred Store] --> captured/ephemeral/0.3
                                    |
                                    v
                            [4. Curation Pipeline] --> promote or ignore
                                    |
                                    v
                            [5. Natural Decay] --> unengaged records fade
                                    |
                                    v
                            [6. Garbage Collection] --> hard delete debris
```

## The gramaton_observe Tool

### API

```
gramaton_observe(
  messages=[...],      // Raw conversation turns (optimal path)
  -- OR --
  facts=["...", ...],  // Pre-extracted facts (fallback path)
)
```

Returns immediately: `{accepted: true}`
Processing happens asynchronously on the server.

No session_id required. Feedback loop detection uses server-side
retrieval tracking (automatically populated by search/inspect/explore)
and recency heuristics (via last_accessed timestamps). See "Feedback
Loop Detection" section for rationale and alternatives considered.

### Two Input Modes

**Messages mode (optimal):**
Agent sends raw conversation turns. Server uses its configured LLM to
extract facts. Requires a server LLM to be configured. This is the
recommended path because:
- Agent effort is zero beyond the call
- Extraction quality is controlled by server LLM configuration
- User chooses their model (Opus, Sonnet, Bedrock, Ollama)
- Quality gates are guaranteed server-side

**Facts mode (fallback):**
Agent pre-extracts facts and sends them as strings. Server skips LLM
extraction and goes straight to quality gates. Works without a server LLM.
This is for:
- Users who don't have a server LLM configured
- Agents that want to control extraction quality
- Privacy-sensitive environments (only facts leave the agent, not raw
  conversation)

If `messages` is provided but no server LLM is configured, the tool
returns an error suggesting the facts mode instead.

### Three-Tier Capability Model

| Tier | Capture | Observe | Server LLM | Quality |
|------|---------|---------|------------|---------|
| Basic | Explicit only | Not available | No | High (agent-classified) |
| Standard | Explicit + observe/facts | Fallback mode | No | High + medium |
| Full | Explicit + observe/messages | Optimal mode | Yes | High + high |

This aligns with the existing capability tiers:
- No embedding provider: keyword/property search only
- Embedding provider: vector search
- LLM provider: autonomous curation + observe/messages

## UX Design

### Agent Instructions (CLAUDE.md)

```
When you finish a major task or significant discussion, call
gramaton_observe with the recent conversation messages. This catches
knowledge you may not have explicitly captured. Do this at natural
breakpoints -- not every turn.

Do NOT announce that you are observing. It should be invisible to
the user, like searching the store.
```

### What the User Sees

1. Agent finishes a task
2. Brief tool call flash: `gramaton_observe` (< 200ms, fire-and-forget)
3. Agent continues with next topic
4. User sees nothing else

The agent should NOT:
- Say "Let me save our discussion..."
- Wait for extraction results
- Report what was extracted
- Pause the conversation

### When to Trigger

Natural breakpoints:
- After completing a multi-step task
- After a significant design discussion
- After a decision is made and implemented
- When transitioning between topics
- Approaching session end

NOT:
- After every response (too expensive)
- During active coding/debugging (disruptive)
- For trivial exchanges ("thanks", "okay")

## Server-Side Processing

### Fire-and-Forget Pattern

The MCP tool handler:
1. Validates input (messages or facts present)
2. Queues the work for background processing
3. Returns `{accepted: true}` immediately
4. Background goroutine processes the observation

This ensures the agent is never blocked waiting for extraction.

### Extraction Pipeline (messages mode)

1. **LLM extraction**: Send messages to the server LLM with an extraction
   prompt. The prompt should:
   - Focus on decisions, facts, preferences, procedures, constraints
   - Include negative examples (what NOT to extract)
   - Request structured output: `{facts: ["fact1", "fact2"]}`
   - Be domain-neutral (not assume software engineering)

2. **Fact normalization**: Clean up extracted facts:
   - Trim whitespace
   - Reject empty or trivially short facts (< 20 chars)
   - Reject facts that are questions without answers
   - Truncate to MaxContentLength

### Quality Gates (both modes)

For each candidate fact, embed it once, then run through layered gates:

**Gate 1: Dedup (broad, store-wide)**
- Search the store for top-5 similar records
- If any similarity >= 0.92: skip
- This is the same threshold as auto-supersession at capture time
- Catches: exact duplicates, trivially rephrased duplicates
- Log: "skipped: duplicate of <id>"

**Gate 2: Recency Check (heuristic, time-based)**
- From the same top-5 results, check `last_accessed` timestamps
- If any similarity >= 0.85 AND `last_accessed` within last N hours: skip
- Catches: recently-discussed knowledge that the extraction LLM rephrased
- This is a heuristic -- it uses time proximity as a proxy for "this was
  part of the current conversation"
- Log: "skipped: recently accessed similar record <id>"

**Gate 3: Retrieval Tracking (precise, server-tracked)**
- Check similarity against records in the server's `recentlyRetrieved` set
- If any similarity >= 0.7: skip
- Lower threshold because we KNOW these records were served to the agent
- Catches: heavily rephrased versions of retrieved knowledge where
  similarity has dropped below the 0.85 recency threshold
- See "Feedback Loop Detection" section below
- Log: "skipped: similar to retrieved record <id>"

**Gate 4: Substance Filter**
- Reject facts shorter than 20 characters
- Reject facts that match common trivial patterns
- Log: "skipped: trivial"

**Gate 5: System Prompt Filter** (future consideration)
- Compare against known boilerplate (system prompts, tool descriptions)
- This was the #1 junk source in Mem0 (52.7% of junk)
- May not be needed immediately since our extraction prompt can exclude it

**Why three similarity gates (1, 2, 3)?**

Each gate catches what the others miss:

| Scenario | Gate 1 (0.92) | Gate 2 (0.85+recent) | Gate 3 (0.7+retrieved) |
|----------|--------------|---------------------|----------------------|
| Exact duplicate | Caught | -- | -- |
| Light rephrase (sim 0.88) | Missed | Caught if recent | Caught if retrieved |
| Heavy rephrase (sim 0.75) | Missed | Missed | Caught if retrieved |
| New take on same topic (sim 0.6) | Missed | Missed | Passes (correct) |
| Unrelated (sim 0.2) | Missed | Missed | Passes (correct) |

Gate 1 is the broadest but least sensitive. Gate 3 is the most sensitive
but only applies to records the server actually served. Gate 2 is the
middle ground using time as a proxy.

This layering means the feedback loop risk decreases from "3-5 variants
before stabilizing" (Gate 1 only) to "0-1 variants" (all three gates).

### Deferred Storage

Facts that pass all gates are stored with conservative defaults:

```
processing_status: "captured"
temporality: "ephemeral"
confidence: 0.3
importance: 0.0
source_credibility: 0.5
testimony_hops: 1
context_about: <extracted from conversation if possible>
source_ref: "observe:<timestamp>"
```

These records:
- Score very low in search results (low confidence, ephemeral decay)
- Are visible to autonomous curation for classification
- Can be promoted by curation (higher confidence, durable temporality)
- Decay naturally if never accessed or promoted
- Are eligible for garbage collection if criteria are met

## Feedback Loop Detection

### The Problem

Agent retrieves record X from the store. Agent discusses X in the
conversation. Observe extracts "X" as a fact. Server stores it as a new
record. Next cycle, it gets retrieved again. The loop amplifies.

This was Mem0's #2 junk source. One hallucination multiplied to 808 copies.

### Why This Is Hard

The extracted fact won't be identical to the original record. The agent
rephrases knowledge when discussing it. The extraction LLM rephrases it
again. So the original record says "We chose JWT tokens for authentication
because session cookies don't meet compliance requirements" and the
extracted fact says "JWT is used for auth due to compliance constraints."
Same knowledge, different words, cosine similarity ~0.75-0.85.

This similarity range falls below the 0.92 dedup threshold. Without
additional detection, we'd accumulate 3-5 variants before the
similarity to ANY of them crosses 0.92.

### Solution: Three-Layer Detection

We use three complementary mechanisms. Each catches what the others miss.

**Layer 1: Store-wide dedup (Gate 1)**
Threshold: 0.92 similarity against any record in the store.
Catches exact and near-exact duplicates.
No new infrastructure. Same threshold as capture-time auto-supersession.

**Layer 2: Recency heuristic (Gate 2)**
Threshold: 0.85 similarity against records accessed in last N hours.
Uses `last_accessed` timestamps already on every record.
No new infrastructure. Just a time-conditional similarity check.
Catches recently-discussed knowledge that was lightly rephrased.

**Layer 3: Retrieval tracking (Gate 3)**
Threshold: 0.7 similarity against records the server KNOWS it served.
Uses a server-side set populated automatically by search/inspect/explore.
Lowest threshold but highest precision: only applies to records we are
certain the agent saw.
Catches heavily rephrased versions of retrieved knowledge.

### Retrieval Tracking Implementation

**Data structure:**

```go
// On the Server struct, protected by its own mutex.
type retrievalTracker struct {
    mu      sync.Mutex
    entries map[string]time.Time  // nodeID -> when served
}
```

**Populated automatically** by existing handlers (no agent changes):
- `handleSearch`: all result IDs added after search execution
- `handleGetRecord` (inspect): inspected record ID added
- `handleExplore`: all traversed node IDs added
- MCP equivalents: same logic in gramaton_search, gramaton_inspect,
  gramaton_explore tool handlers

The agent does not need to pass session_id, retrieved_ids, or any new
parameter. The server tracks what it served, period.

**Consulted by observe quality gates:**
For each candidate fact, the observe pipeline queries this set and
computes similarity against the stored embeddings of tracked records.

**Cleanup:**
- Entries older than `observe.feedback_loop_hours` (default 4h) are
  pruned on each observe call
- If set exceeds 500 entries, oldest are dropped (memory bound)
- Set is in-memory only, lost on server restart (acceptable -- server
  restart resets the conversation context anyway)

### Design Considerations and Trade-offs

**Why not session_id?**

We considered requiring agents to pass a session_id on every tool call
(search, inspect, observe) so the server could track retrievals per
session. We rejected this because:
- Adds a parameter to every existing tool (breaking change to API surface)
- Agent must generate and consistently pass the ID
- Adds complexity for a mechanism that works almost as well with just a
  time-windowed set
- Sessions are an artificial construct -- the server doesn't know when
  conversations start or end

The retrieval tracker achieves 90% of session tracking's precision with
0% of its API surface cost.

**Why not agent-passed retrieved_ids?**

We considered having the agent track which records it retrieved and pass
them to gramaton_observe:

```
gramaton_observe(messages=[...], retrieved_ids=["01KN...", ...])
```

Rejected because:
- Agent must track and accumulate IDs throughout the session
- IDs list could be large (50-100 records in a heavy session)
- Adds agent-side state management
- Agent might forget or lose track

The server already handles these requests. Tracking them server-side
is simpler and more reliable.

**What if the server restarts mid-session?**

The retrieval tracker is in-memory. Server restart clears it. This means
the first observe call after a restart has no retrieval data and Gate 3
is ineffective for that call. Gates 1 and 2 still protect against
duplicates and recently-accessed records. The risk window is small: one
observe call with degraded feedback loop detection.

**What about multiple concurrent agents?**

The retrieval tracker is global, not per-agent. If two agents are using
the store simultaneously, records retrieved by Agent A affect Gate 3 for
Agent B's observe calls. This is conservative (more likely to skip) but
could suppress legitimate captures. For the current single-user use
case, this is not a concern. For future multi-agent scenarios, the
tracker could be partitioned by a lightweight agent identifier.

**Could the 0.7 threshold on Gate 3 be too aggressive?**

Yes, potentially. A record about "JWT authentication" retrieved by the
agent, followed by an observe extraction about "OAuth authentication"
might have 0.7 similarity and get incorrectly skipped. They're related
but not duplicates.

Mitigations:
- The threshold is configurable (`observe.retrieval_similarity`)
- Gate 3 only applies to records in the retrieval set (not store-wide)
- If the fact is genuinely new knowledge, the agent should explicitly
  capture it via gramaton_capture (which bypasses observe gates)
- We can tune the threshold based on observed false-positive rates

**This is a two-way door.** The retrieval tracker can be disabled
(`observe.retrieval_tracking: false`) without affecting the rest of the
observe pipeline. Gates 1 and 2 still function. If Gate 3 proves too
aggressive or not aggressive enough, we can adjust the threshold, change
the mechanism, or disable it entirely. The extraction pipeline, quality
gates architecture, and deferred storage are all independent of the
specific feedback loop detection mechanism.

### Feedback Loop Amplification Analysis

Without detection, how bad could feedback loops get?

Scenario: Record X exists. Each observe cycle creates a variant.

- Cycle 1: X exists. Observe creates Y (sim 0.8 to X).
- Cycle 2: X and Y exist. Observe creates Z (sim 0.8 to X, 0.85 to Y).
  Gate 1 (0.92): misses. Gate 2 (0.85 recent): maybe catches via Y.
- Cycle 3: X, Y, Z exist. Observe creates W (sim to at least one > 0.85).
  Increasingly likely to be caught.

The amplification is self-dampening, not exponential. Each new variant
increases the chance the next one is caught. Worst case without any
detection: 3-5 variants before stabilizing. With the three-layer detection:
0-1 variants.

Compare to Mem0: no detection, no dedup, no decay. One fact grew to 808
copies because nothing stopped the cycle.

### Configuration

```yaml
observe:
  retrieval_tracking: true           # enable Gate 3 (retrieval tracker)
  retrieval_similarity: 0.7          # Gate 3 similarity threshold
  feedback_loop_hours: 4             # Gate 2 recency window
  feedback_loop_similarity: 0.85     # Gate 2 similarity threshold
```

All thresholds are configurable. The entire retrieval tracking mechanism
can be disabled independently of the rest of the observe pipeline.

## Garbage Collection

### Purpose

Hard delete records that every signal says are noise. This is the
"principled forgetting" mechanism -- the synaptic homeostasis equivalent.

### Qualification Criteria (ALL must be true)

| Signal | Threshold | Rationale |
|--------|-----------|-----------|
| processing_status | still "captured" | Never classified by any process |
| age | > 30 days | Had time to be noticed |
| access_count | 0 | Never retrieved |
| confidence | < 0.3 | Low/default confidence |
| importance | 0.0 | No assigned importance |
| edge_count | 0 | Nothing links to it |
| temporality | "ephemeral" | Short-lived by classification |

### Implementation

- Runs during deterministic curation (same cycle as lifecycle transitions)
- Configurable: `curation.gc_enabled` (default: false)
- Starts in dry-run mode: `curation.gc_dry_run` (default: true)
- Configurable age threshold: `curation.gc_min_age_days` (default: 30)
- Logs what it deletes (or would delete in dry-run)
- Hard deletes from graph and indexes
- Historical commits still contain the records (recoverable via revert)

### Safety

- Off by default. Must be explicitly enabled.
- Dry-run by default when enabled. Must explicitly switch to active.
- Very high qualification bar (ALL criteria must be met).
- Records that were EVER accessed, linked, classified, or promoted are
  exempt forever.
- Only affects records from the observe pipeline (explicit captures have
  higher confidence and are typically classified).

### Tenet Update

Original: "Never delete, always supersede."

Revised: **"Never delete knowledge. Always supersede understanding.
Forget noise that nothing valued."**

The distinction:
- Knowledge: classified, accessed, linked, or otherwise engaged with.
  Never delete. Supersede with edges.
- Noise: unclassified, unaccessed, unlinked, low-confidence debris that
  no process ever promoted. Subject to garbage collection after a waiting
  period.

## Configuration

New config fields:

```yaml
observe:
  enabled: true                    # master switch for observe pipeline
  extraction_prompt: ""            # custom extraction prompt (optional)
  max_facts_per_call: 20           # cap on facts extracted per observe call
  default_confidence: 0.3          # confidence assigned to observed records
  default_temporality: "ephemeral" # temporality assigned to observed records
  substance_min_length: 20         # minimum fact length in characters

curation:
  gc_enabled: false                # garbage collection master switch
  gc_dry_run: true                 # log what would be deleted without deleting
  gc_min_age_days: 30              # minimum age before GC eligibility
```

## Future Enhancements

### Hook-Based Automatic Observation (not in v1)

A Claude Code hook that fires at session end, sending the conversation
transcript to `gramaton observe` CLI. Zero agent involvement. Truly
automatic. Requires:
- Hook access to conversation transcript
- CLI support for observe
- Robust session boundary detection

This is a natural evolution once we validate that server-side extraction
works well.

### System Prompt Filtering (not in v1)

Compare extracted facts against known boilerplate (system prompts, tool
descriptions, common preambles). Skip facts that are just restating the
system prompt. This was Mem0's #1 junk source but may not be needed if
our extraction prompt is specific enough.

### Extraction Prompt Customization (v1 but optional)

Allow users to customize the extraction prompt via config. Similar to
Mem0's `custom_fact_extraction_prompt`. Default prompt is domain-neutral.
Users can specialize for their use case ("only extract architectural
decisions and API contracts").

## Build Order

1. Retrieval tracker on server (populated by search/inspect/explore handlers)
2. `gramaton_observe` MCP tool + HTTP endpoint (fire-and-forget, async processing)
3. Extraction pipeline (LLM extraction from messages)
4. Quality gates (Gate 1 dedup, Gate 2 recency, Gate 3 retrieval, Gate 4 substance)
5. Facts-mode fallback (accept pre-extracted facts, skip extraction, same gates)
6. GC in deterministic curation (dry-run mode first)
7. Config additions (observe section, GC settings)
8. CLAUDE.md + integration doc updates
9. Tests throughout -- each stage tested independently before integration

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Extraction LLM produces junk | Quality gates + deferred classification + decay + GC |
| Feedback loops | Session retrieval tracking + similarity check |
| High LLM cost per observe | Fire at breakpoints, not every turn; cap facts per call |
| Store bloat from low-quality records | Ephemeral decay + GC cleans up debris |
| Privacy: raw conversation to server | User chooses LLM provider; facts mode as alternative |
| Agent never calls observe | Graceful degradation: explicit capture still works |
| GC deletes something valuable | Off by default, dry-run first, very high qualification bar, recoverable via revert |

## Relationship to Research

This pipeline maps to the brain's memory processing:

| Brain Stage | Gramaton Equivalent |
|-------------|-------------------|
| Sensory input | Conversation stream |
| Sensory memory | Raw messages in observe call |
| Attention filter | Quality gates |
| Working memory | Deferred capture (ephemeral, low confidence) |
| Consolidation | Curation pipeline (classify, promote, link) |
| Long-term memory | Promoted records (higher confidence, durable) |
| Synaptic homeostasis | Garbage collection |
| Forgetting curve | Temporal decay in scoring model |

The research foundation (McClelland et al., 1995 -- Complementary Learning
Systems; Tononi & Cirelli, 2006 -- synaptic homeostasis) identified this
architecture. The current implementation has stages 4-7 (working memory
through forgetting). The observe pipeline adds stages 1-3 (perception
through attention).
