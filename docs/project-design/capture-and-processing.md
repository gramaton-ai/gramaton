# Capture & Processing

## Overview

Knowledge enters Gramaton through the `gramaton capture` CLI command. The service handles mechanical processing (storage, embeddings, dedup). The agent handles intelligent processing (classification, summarization, decomposition) via subagents.

This split exists because most users don't have separate LLM API access. The only LLM available is the one powering their agent session (Claude Code, Kiro CLI, etc.). Rather than build LLM infrastructure into the server, we use the agent itself as the processor.

## Capture Flow

### Step 1: Agent Decides to Capture (Agent-Side)

The agent, guided by system prompt instructions, recognizes knowledge worth storing. It spawns a subagent with the content and a **context envelope**.

### Step 2: Subagent Classifies and Stores (Agent-Side)

The subagent:
1. Reads the content and context envelope
2. Classifies metadata (temporality, confidence, knowledge_type, etc.)
3. Extracts keywords and generates a short summary
4. Extracts entities from both content and context
5. Calls `gramaton capture` with all metadata flags
6. Searches Gramaton for related existing nodes
7. Creates edges to existing nodes via `gramaton update`
8. If content is complex, decomposes into sub-records and captures each

### Step 3: Service Processes (Server-Side, Immediate)

On receiving `gramaton capture`:
1. Validates input
2. Stores raw content as a node with provided properties
3. Generates vector embeddings via the configured provider (Ollama, API, or Bedrock)
4. Checks for duplicates via vector similarity
5. Detects potential relationships via vector similarity
6. If content exceeds chunk threshold, creates chunk child nodes with `part_of` edges
7. Returns record ID

### Diagram

```
Main Agent Session
  │
  │ "Let's go with Kafka. 50k events/sec rules out RabbitMQ."
  │
  │ [Agent recognizes: durable decision, worth storing]
  │
  ├─► Spawns subagent with:
  │     Content: "We chose Kafka over RabbitMQ..."
  │     Context envelope:
  │       What is this about: Event pipeline architecture decision
  │       Who or what is involved: Platform Engineering team, Sara (manager)
  │       What prompted this: Decision was due Friday, load testing completed
  │       What should this be findable by: PLAT-847, kafka decision, event-pipeline, platform-engineering
  │       What else in the store relates to this: Previous RabbitMQ and Pulsar evaluations
  │
  │ [Main agent continues conversation immediately]
  │
  └─► Subagent (separate context):
        │
        ├─ Classifies: temporality=durable, confidence=0.9,
        │  knowledge_type=episodic, epistemic_status=well_established
        │
        ├─ Extracts keywords: [kafka, rabbitmq, event-pipeline,
        │  PLAT-847, architecture-decision, platform-engineering]
        │
        ├─ Summary: "Team chose Kafka over RabbitMQ for event
        │  pipeline — 50k events/sec and existing team experience."
        │
        ├─ gramaton capture --content "..." --temporality durable
        │  --confidence 0.9 --knowledge-type episodic
        │  --keywords "kafka,rabbitmq,..." --summary-short "..."
        │  ← returns record ID abc-123
        │
        ├─ gramaton search "Event Pipeline" → finds existing project node
        ├─ gramaton update abc-123 --link-to [project-node] --edge-type part_of
        │
        ├─ gramaton search "Kafka" → finds existing concept node
        ├─ gramaton update abc-123 --link-to [kafka-node] --edge-type discusses
        │
        ├─ Decomposes into sub-records:
        │  ├─ gramaton capture --content "Must handle 50k events/sec"
        │  │  --knowledge-type semantic --temporality durable
        │  │  → abc-124, then link: abc-124 --justifies--> abc-123
        │  │
        │  ├─ gramaton capture --content "Team has Kafka experience"
        │  │  --knowledge-type semantic --temporality temporal
        │  │  → abc-125, then link: abc-125 --justifies--> abc-123
        │  │
        │  └─ gramaton capture --content "RabbitMQ benchmarked at 12k/sec"
        │     --knowledge-type episodic --temporality durable
        │     → abc-126, then link: abc-123 --defeats--> abc-126
        │
        └─ Done. Context discarded.
```

## The Context Envelope

The most important concept in capture. The context envelope is everything the agent knows that ISN'T in the content itself.

**Why it matters:** "We chose Kafka over RabbitMQ" is meaningless without knowing it's for the Event Pipeline project on the Platform Engineering team, related to PLAT-847. Without the context envelope, this record is only findable by searching for "Kafka" or "RabbitMQ" — not by searching for "platform team decisions" or "PLAT-847."

**The context envelope uses a lightweight structured prompt** — five domain-neutral fields that work for any knowledge domain (engineering, research, health, legal, creative work). The structure acts as a checklist so the agent doesn't forget to include relevant context. Empty fields are fine — simple captures won't fill all of them.

| Field | What It Asks | Examples Across Domains |
|-------|-------------|------------------------|
| **What is this about** | Topic, domain, subject area | "Event pipeline architecture" / "2024 election polling methodology" / "Blood pressure medication adjustment" / "Chapter 3 antagonist motivation" |
| **Who or what is involved** | People, organizations, entities, systems | "Platform Engineering team, Sara (manager)" / "Nate Silver, FiveThirtyEight" / "Dr. Martinez, lisinopril" / "The harbor scene, protagonist" |
| **What prompted this** | Why this knowledge emerged right now | "Decision was due Friday" / "New poll data released today" / "Annual physical results" / "Beta reader feedback" |
| **What should this be findable by** | Terms, names, IDs someone might search for later | "PLAT-847, kafka decision, event-pipeline" / "polling methodology, silver model, 538" / "blood pressure, lisinopril, side effects" / "antagonist backstory, harbor" |
| **What else in the store relates to this** | Known related records or topics, if any | "Previous RabbitMQ evaluation" / "Prior polling accuracy analysis" / "Last year's BP readings" / "Chapter 1 introduction of antagonist" |

The last field — "what should this be findable by" — explicitly asks the agent to think about future retrieval at capture time. The agent knows the user's vocabulary and what they'd search for. This produces better keywords than relying on the subagent to extract them from content alone.

The subagent interprets the context envelope and decides what becomes keywords, concept nodes, edges, and metadata. The main agent's job is just to fill in what it knows.

## Capture Without a Subagent

Not every capture needs full classification. Two lighter-weight paths:

### Quick Capture (Agent Provides Metadata)

The agent classifies inline and calls the CLI directly. No subagent. Suitable when the content is simple and the agent has enough context to classify quickly.

```bash
gramaton capture --content "User prefers dark mode in all IDEs" \
  --temporality durable --confidence 0.95 --knowledge-type semantic \
  --keywords "dark-mode,IDE,user-preference"
```

### Raw Capture (No Metadata)

The agent sends content with no classification. Gramaton stores it with embeddings only. The record is searchable but unclassified. Can be enriched later via `/gramaton-process`.

```bash
gramaton capture --content "User prefers dark mode in all IDEs"
```

Record gets `processing_status: captured`. A future `/gramaton-process` skill invocation can classify pending records in bulk.

## Chunking Long Documents

When `content_full` exceeds the chunk threshold (default: 512 tokens, configurable):

1. Summary pyramid levels (keywords, short, abstract) are embedded directly — they're short enough
2. Full content is split into overlapping chunks (default: 512 tokens, 128 overlap, configurable)
3. Each chunk becomes a child node with its own embedding and a `part_of` edge to the parent
4. Parent node holds all metadata. Chunk nodes hold chunk text + embedding only.

A search matching chunk 12 returns the chunk. The agent follows the `part_of` edge to the parent for full context.

**Without LLM (no subagent classified this):**
- Keywords: extracted via TF-IDF or similar heuristic
- Short summary: first ~200 characters of content
- Abstract: first ~2000 characters of content
- Chunking: works the same (mechanical splitting)

**With LLM (subagent classified this):**
- Keywords: properly extracted by the agent
- Short summary: agent-generated, high quality
- Abstract: agent-generated, high quality
- Decomposition: agent may also extract constituent parts as linked sub-records

## Processing Status Lifecycle

```
captured  →  pending  →  processed
   │                        ▲
   │                        │
   └────────────────────────┘
         (if agent provides full metadata at capture time,
          skips pending and goes straight to processed)
```

- `captured` — stored with embeddings only, no LLM classification
- `pending` — queued for LLM enrichment (agent will process via `/gramaton-process`)
- `processed` — fully classified with metadata, summary pyramid, and relationships
