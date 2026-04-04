# Gramaton

A knowledge store for AI agents.

Gramaton stores knowledge in a versioned property graph with vector search and epistemic metadata -- fields like confidence, temporality, and provenance that help retrieval tools decide what's still relevant and what's gone stale. Agents interact through a CLI that filters and ranks by metadata before returning results.

## What It Does

Most AI memory solutions store text with timestamps. Gramaton adds structure:

- **Metadata on every record.** Temporality (is this a permanent fact or a time-bound decision?), confidence (how sure are we?), epistemic status (well-established, speculative, contested, or refuted), knowledge type, and source provenance.
- **A versioned property graph.** Records are nodes. Relationships are typed, weighted edges. Every mutation is a commit with full history. You can diff knowledge over time, inspect why a record's confidence changed, branch for speculative exploration, and roll back bad captures.
- **Vector similarity search with metadata filtering.** The query pattern is filter (narrow by metadata), rank (sort by embedding similarity), traverse (follow graph edges to related knowledge) -- in that order, as a single operation.
- **Concept emergence.** Keywords that appear across multiple records graduate to concept nodes -- graph hubs that connect related knowledge across domains.
- **Decay.** Records lose retrieval priority over time at rates determined by their temporality classification. Ephemeral knowledge fades in hours. Durable knowledge persists for months. High-importance records resist decay.

## How It Works

Two parts:

**A server** -- a single Go binary with no native dependencies. Stores the graph, manages vector indexes (embeddings delegated to Ollama, OpenAI-compatible APIs, or AWS Bedrock), handles versioning, and serves queries via CLI. No LLM runs inside the server.

**An agent integration kit** -- prompt patterns and subagent templates that give the user's existing LLM agent the ability to store and retrieve knowledge. The agent classifies and decomposes knowledge using its own LLM capabilities, then writes structured records to Gramaton via the CLI. Retrieval happens inline during the agent's normal reasoning. The goal is transparent operation -- the agent searches and captures without the user needing to think about it.

## Quick Start

```bash
# Build
go build -o gramaton .

# Initialize
gramaton init

# Configure embedding (optional, enables semantic search)
# Edit ~/.gramaton/config.yaml and set embedding.provider to "ollama"
# Requires: ollama pull nomic-embed-text

# Capture knowledge
gramaton capture <<'EOF'
{
  "content": "We chose Kafka over RabbitMQ for the event pipeline.",
  "temporality": "durable",
  "confidence": 0.9,
  "knowledge_type": "episodic",
  "keywords": ["kafka", "rabbitmq", "event-pipeline"],
  "summary_short": "Chose Kafka for event pipeline"
}
EOF

# Search
gramaton search "event pipeline" --confidence-min 0.5

# Inspect a record
gramaton inspect <record-id>

# Explore the graph
gramaton explore <record-id> --depth 2
```

## CLI Commands

### Read Commands

| Command | Description |
|---------|-------------|
| `gramaton search <query> [flags]` | Tier 1: discover relevant records |
| `gramaton inspect <id>` | Tier 2: full content, metadata, related edges |
| `gramaton explore <id> [flags]` | Tier 3: graph traversal from a node |
| `gramaton pending` | List records awaiting classification |
| `gramaton status` | Store health, counts, embedding status |
| `gramaton log [--last N]` | Commit history |
| `gramaton diff [ref1..ref2]` | Structural diff between commits |

### Write Commands (JSON on stdin)

| Command | Description |
|---------|-------------|
| `gramaton capture` | Store a knowledge record |
| `gramaton classify` | Classify a pending record |
| `gramaton update` | Modify properties or create edges |
| `gramaton ingest <files>` | Bulk-load text files |
| `gramaton reembed [--batch N]` | Re-embed records after model change |

### Versioning

| Command | Description |
|---------|-------------|
| `gramaton branch create <name>` | Fork a branch from HEAD |
| `gramaton branch list` | List all branches |
| `gramaton branch checkout <name>` | Switch to a branch |
| `gramaton branch merge <name>` | Merge branch into main |
| `gramaton branch discard <name>` | Discard a branch |
| `gramaton revert <commit>` | Rollback to a prior commit |

### Repair Tools

| Command | Description |
|---------|-------------|
| `gramaton delete <id> --reason "..."` | Soft delete (recoverable via revert) |

## Agent Integration

Gramaton includes prompt templates and subagent instructions for transparent agent integration:

- `integration/claude-code/CLAUDE.md` -- System prompt for Claude Code
- `integration/claude-code/subagent-capture.md` -- Capture subagent instructions
- `integration/claude-code/subagent-curate.md` -- Curation subagent instructions
- `integration/docs/custom-agents.md` -- Guide for custom agent frameworks

See [Agent Integration](docs/project-design/agent-integration.md) for the full design.

## What Makes It Different

**Metadata-aware retrieval.** Most vector databases return results ranked by similarity alone. Gramaton filters by confidence, temporality, and epistemic status before ranking. A superseded architecture decision doesn't compete with its replacement.

**Knowledge diffing.** "What changed about our caching strategy since January?" is a query that vector search can't answer. Gramaton's versioned graph can -- it diffs two points in history and returns what was added, modified, or superseded.

**The agent is the LLM.** Instead of building LLM infrastructure into the server (API keys, model providers, async processing queues), Gramaton uses the LLM the user already has. Classification happens in the agent session via subagents. The server stays simple.

**Epistemic honesty.** Records can be marked as refuted but retained for context. A superseded decision stays in the graph, linked to what replaced it. The system tracks not just what's true, but what used to be true and why it changed.

## Documentation

The [project design docs](docs/project-design/) cover the full system design:

| Document | What It Covers |
|----------|---------------|
| [Tenets](docs/project-design/tenets.md) | Guiding principles |
| [Foundations](docs/project-design/foundations.md) | Research influences |
| [Architecture](docs/project-design/architecture.md) | System design, components, package structure |
| [Data Model](docs/project-design/data-model.md) | Graph structure, property types, metadata schema |
| [Capture and Processing](docs/project-design/capture-and-processing.md) | How knowledge enters the system |
| [Retrieval](docs/project-design/retrieval.md) | Query pattern, CLI, versioning tools, scoring model |
| [Embedding](docs/project-design/embedding.md) | Providers, vector indexing, chunking |
| [Curation](docs/project-design/curation.md) | Background maintenance |
| [Agent Integration](docs/project-design/agent-integration.md) | System prompts, subagent patterns |
| [Case Studies](docs/project-design/case-studies.md) | Real-world examples testing the design |
| [Data Integrity](docs/project-design/data-integrity.md) | Threat model, failure modes, defenses |
| [Validation](docs/project-design/validation.md) | How we test the core hypothesis |
| [Design Decisions](docs/project-design/design-decisions.md) | Decision log with rationale |
| [Glossary](docs/project-design/glossary.md) | Terminology reference |

## License

[Apache 2.0](LICENSE)

## Author

Brandon Lattin -- [github.com/brandonlattin](https://github.com/brandonlattin)
