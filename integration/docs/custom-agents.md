# Custom Agent Integration

How to integrate Gramaton with any agent framework.

## What You Need

1. **Shell access** -- the agent must be able to run `gramaton` CLI commands
2. **System prompt** -- instructions telling the agent when to search and capture
3. **Subagent support** (optional) -- for async capture without blocking

## CLI Interface

All interaction happens through the `gramaton` CLI. Commands accept
flags for read operations and JSON on stdin for write operations.

### Read Commands

```bash
gramaton search <query> [flags]       # Tier 1: find records
gramaton inspect <record-id>          # Tier 2: full content + edges
gramaton explore <record-id> [flags]  # Tier 3: graph traversal
gramaton pending                      # List unclassified records
gramaton status                       # Store health and counts
gramaton log [--last N]               # Commit history
gramaton diff [ref1..ref2]            # What changed between commits
```

### Write Commands (JSON on stdin)

```bash
gramaton capture <<'EOF'
{"content": "...", "temporality": "durable", "confidence": 0.9,
 "keywords": ["k1", "k2"], "summary_short": "..."}
EOF

gramaton classify <<'EOF'
{"id": "...", "temporality": "durable", "confidence": 0.9}
EOF

gramaton update <<'EOF'
{"id": "...", "link_to": "...", "edge_type": "justifies", "edge_weight": 0.9}
EOF
```

### Search Flags

```
--confidence-min <float>       Filter: minimum confidence
--confidence-max <float>       Filter: maximum confidence
--temporality <value>          Filter: immutable|durable|temporal|ephemeral
--knowledge-type <value>       Filter: episodic|semantic|procedural|conceptual|reference
--epistemic-status <value>     Filter: well_established|probable|speculative|contested|refuted
--include-historical           Include records with valid_until in the past
--top <int>                    Number of results (default: 10)
```

## Capture Modes

### Mode 1: Subagent Capture (Preferred)

The main agent spawns a separate context to classify and store.
Zero context pollution in the main conversation.

Use when: The framework supports subagents or parallel invocations.

### Mode 2: Inline Capture

The main agent classifies and stores directly in the conversation.

Use when: No subagent support. Best for simple, quick captures.

### Mode 3: Raw Capture

Store content with no classification. Classified later by curation.

```bash
gramaton capture <<'EOF'
{"content": "The thing to remember"}
EOF
```

Use when: Minimal overhead needed, or bulk ingestion.

## System Prompt Template

Add to your agent's system prompt (see integration/claude-code/CLAUDE.md
for the full template). The key instructions:

1. **Search before answering** questions about past decisions or context
2. **Capture decisions and preferences** via subagent when they emerge
3. **Don't capture your own output** -- only user-stated or external knowledge
4. **Use metadata_summary** for quick trustworthiness assessment
5. **Run curation** when the system signals it's overdue

## Output Format

All commands return JSON. Search results include:

```json
{
  "id": "01H5K9E2GJ...",
  "keywords": ["kafka", "rabbitmq"],
  "summary_short": "Chose Kafka for event pipeline",
  "metadata_summary": "Current. Durable, confidence 0.90, well-established",
  "confidence": 0.9,
  "temporality": "durable",
  "effective_score": 0.78
}
```

The `metadata_summary` field is designed to be read by LLMs -- it's
a natural language trustworthiness assessment.
