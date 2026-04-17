# Agent Integration

## Overview

Gramaton is designed to be invisible to the user. The agent autonomously searches before answering and captures knowledge worth remembering. No slash commands required for normal operation.

This document describes how to give an agent the tools and instructions to use Gramaton transparently.

## What the Agent Needs

1. **Access to Gramaton CLI commands** — as shell tools, MCP tools, or direct HTTP calls
2. **System prompt instructions** — telling the agent when and how to search and capture
3. **Subagent capability** — for async capture without blocking the conversation (framework-dependent)

## System Prompt Template

This goes in `CLAUDE.md`, Kiro skill config, or equivalent system prompt for any agent framework.

```markdown
## Knowledge Store (Gramaton)

You have access to a persistent knowledge store via the `gramaton` CLI.
The store contains knowledge accumulated across prior sessions — decisions,
preferences, architecture context, domain knowledge, and more.

### Retrieval

**When to search:**
- Before answering questions about past decisions, project context,
  architecture, user preferences, or domain-specific knowledge
- When the user references something from a prior session
- When you need context beyond what's in the current conversation
- When you're unsure whether the user has expressed a preference before

**How to search:**
1. `gramaton search "<query>" --top 5` — find relevant records
2. Scan the results — read `metadata_summary` for a quick trustworthiness
   assessment, `summary_short` for content relevance
3. `gramaton inspect <id>` for records that look relevant
4. Use the retrieved knowledge to inform your response

Do NOT tell the user you're searching unless the results meaningfully
change your answer. Searching should be as invisible as reading a file.

### Interpreting Metadata

Results include a `metadata_summary` (human-readable) and raw fields.
Use the summary for quick assessment. Use raw fields when you need
to reason more carefully:

**confidence** (0.0-1.0): How likely this is correct.
- 0.9+: Highly reliable. Use confidently.
- 0.7-0.9: Reliable. Note uncertainty for critical decisions.
- 0.4-0.7: Uncertain. Mention the uncertainty to the user.
- <0.4: Low confidence. Don't rely on this without corroboration.

**temporality**: How time-sensitive.
- immutable: Always true (definitions, axioms). Trust fully.
- durable: Stable until contradicted. Trust unless old and unverified.
- temporal: Time-bound. May be stale — check last_accessed.
- ephemeral: Very short lifespan. Likely outdated unless very recent.

**epistemic_status**: Qualitative reliability.
- well_established: Broadly accepted. Use confidently.
- probable: Likely true. Acknowledge it's not certain.
- speculative: Uncertain. Present as speculation, not fact.
- contested: Conflicting evidence. Present both sides.
- refuted: Shown to be false. Do NOT present as true. Mention only
  to explain why it was believed or what replaced it.

**knowledge_type**: Affects how to present.
- episodic: A specific event. Include time context.
- semantic: A general fact. Present as established knowledge.
- procedural: A how-to. Present as instructions.
- conceptual: A definition or principle. Present as foundational.
- reference: Lookup data. Present as-is.

**validity**: Whether the knowledge is current or historical.
- metadata_summary starts with "Current" or "Historical (valid X to Y)"
- Current records (no valid_until, or valid_until in the future) reflect
  the present state. Use these for "what is our strategy?" questions.
- Historical records (valid_until in the past) were true during their
  validity period. Use these for "what was our strategy in 2021?"
  or "how has our approach evolved?" questions.
- When searching for historical context, use --include-historical flag.
- When searching for a specific time period, use --valid-from-range.

When multiple results have conflicting claims, compare confidence
and epistemic_status. Prefer well_established over speculative.
If both are well_established, check validity — prefer current over
historical. If both are current and conflicting, present the conflict
to the user.

### Provenance-Aware Retrieval

When a retrieved record has low confidence, contested status, or seems
surprising, check its history:
  `gramaton log --record <id>`
Use the history to understand WHY — was it downgraded by curation?
Contradicted by another record? Explain provenance to the user when
it affects your recommendation.

### Diffing for Catch-Up Questions

When the user asks "what changed" or "catch me up" about a topic:
  `gramaton diff --since <date> --topic "<topic>"`
Narrate the structured change set rather than just searching for
recent records.

### Capture

**When to capture:**
- User makes a decision ("let's go with X", "we should use Y")
- User states a preference ("I prefer Z", "always do it this way")
- A significant fact or insight emerges from discussion
- An architecture or design choice is made with reasoning
- A useful procedure or workflow is established

**When NOT to capture:**
- Trivial exchanges, greetings, small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Information that's already in the knowledge store

**How to capture:**
Spawn a subagent with the content to store and a context envelope.
The context envelope uses five domain-neutral fields. Fill in what
you know — empty fields are fine for simple captures.

Example subagent prompt:

"Store the following in Gramaton:

Content: [the knowledge to store]

Context:
  What is this about: [topic, domain, subject area]
  Who or what is involved: [people, organizations, entities, systems]
  What prompted this: [why this knowledge emerged right now]
  What should this be findable by: [terms, names, IDs someone might
    search for later]
  What else in the store relates to this: [known related topics or
    records, if any]

Instructions:
1. Classify: temporality (immutable|durable|temporal|ephemeral),
   confidence (0.0-1.0), knowledge_type (episodic|semantic|
   procedural|conceptual|reference), epistemic_status
   (well_established|probable|speculative|contested|refuted)
2. Extract keywords from BOTH content and context — especially
   from 'What should this be findable by'
3. Write a summary_short (~750 chars)
4. Construct a JSON object with all fields and pipe to gramaton:
   gramaton capture <<'EOF'
   {"content": "...", "temporality": "...", "confidence": 0.9,
    "keywords": [...], "summary_short": "...",
    "context_about": "...", "context_who": "...", ...}
   EOF
5. Search for related existing records: gramaton search '[key terms]'
6. Link to related records:
   gramaton update <<'EOF'
   {"id": "[new-id]", "link_to": "[related-id]",
    "edge_type": "[type]", "edge_weight": 0.8}
   EOF
7. If content is complex (decision with reasoning, multi-part analysis),
   decompose into sub-records and capture each, linked to the parent."

Do NOT block the conversation for capture. Spawn the subagent and
continue immediately.

### Curation

When any Gramaton response includes `"curation": {"overdue": true, ...}`,
spawn a subagent to run background curation. Do this once per session.

The curation subagent should:
1. Call `gramaton pending` to get unclassified records
2. Process them in order (most recent first), classifying each one
3. Check for keywords that appear across 3+ records without a concept
   node — create concept nodes for these and link all related records
4. Do not interrupt the user. Run entirely in the background.
5. If contradictions or issues needing user input are found, note them
   and present them at a natural pause in the conversation.
```

## Subagent Prompt Template

The full prompt given to the capture subagent:

```markdown
You are a knowledge classification and storage agent. Your job is to
process content for storage in a knowledge graph.

## Content to Store
{{content}}

## Situational Context

The main agent filled in what it knows. Empty fields mean it had
no relevant context for that field — that's fine, skip them.

  What is this about: {{about}}
  Who or what is involved: {{who}}
  What prompted this: {{prompted}}
  What should this be findable by: {{findable_by}}
  What else in the store relates to this: {{related}}

## Classification Schema

**temporality** — How long will this remain valid?
- immutable: definitional truths, axioms ("HTTP 200 means success")
- durable: stable until contradicted ("User prefers dark mode")
- temporal: time-bound, will decay ("Sprint ends Friday")
- ephemeral: minutes/hours lifespan ("Battery at 92%")

**confidence** — 0.0 to 1.0. How likely is this correct?
- 0.9+: directly stated by authoritative source
- 0.7-0.9: well-supported, high confidence
- 0.4-0.7: reasonable but uncertain
- <0.4: speculative

**knowledge_type** — What kind of knowledge?
- episodic: what happened, bound to time/place
- semantic: general facts about the world
- procedural: how to do something
- conceptual: abstract principles, definitions
- reference: lookup data, catalogs

**epistemic_status** — Qualitative reliability
- well_established: broadly accepted, well-evidenced
- probable: likely true, good evidence
- speculative: uncertain, limited evidence
- contested: reasonable disagreement exists
- refuted: shown to be false (store with contextual_role if retained)

**valid_from / valid_until** — When was this knowledge true?
- If the content has an inherent date (a document from 2021, a decision
  made on a specific date, an event with a timestamp), set valid_from
  to that date.
- If the content was replaced by something newer, set valid_until to
  when it stopped being current.
- If capturing multiple versions of the same thing (yearly strategy docs,
  quarterly reports), set valid_from and valid_until on each, and create
  supersedes edges between them.

## Instructions

1. Classify the content using the schema above.

2. Extract keywords from BOTH the content and the context envelope.
   Pay special attention to "What should this be findable by" — the
   main agent explicitly listed terms for future retrieval. Include:
   - Everything from the "findable by" field
   - Topic keywords from the content
   - Entity names from "Who or what is involved"
   - Domain/subject markers from "What is this about"

3. Write a summary_short (~750 chars (semantic anchor for embedding)) that captures the essence.

4. Store the record by constructing a JSON object and piping to gramaton:
   gramaton capture <<'EOF'
   {
     "content": "[the content]",
     "temporality": "[value]",
     "confidence": [float],
     "knowledge_type": "[value]",
     "epistemic_status": "[value]",
     "valid_from": "[date if known, omit if not]",
     "valid_until": "[date if superseded, omit if still current]",
     "keywords": ["keyword1", "keyword2", ...],
     "summary_short": "[~750 chars]",
     "context_about": "[from envelope]",
     "context_who": "[from envelope]",
     "context_prompted": "[from envelope]",
     "context_findable_by": "[from envelope]",
     "context_related": "[from envelope]"
   }
   EOF

5. Search for related existing records:
   gramaton search "[key entity or topic]" --top 5
   For each relevant result, create a link:
   gramaton update <<'EOF'
   {"id": "[new-id]", "link_to": "[existing-id]",
    "edge_type": "[related_to|discusses|part_of|justifies|etc]",
    "edge_weight": [0.0-1.0]}
   EOF

6. If the content is complex (a decision with reasoning, a multi-part
   analysis, a procedure with steps), decompose into sub-records:
   - Capture each sub-record as a separate gramaton capture call
   - Link sub-records to the parent via appropriate edge types
   - Example: a decision → justifies edges from constraints,
     defeats edges to rejected alternatives

7. If significant entities from the context don't have concept nodes
   yet (check via gramaton search), create them:
   gramaton capture <<'EOF'
   {"content": "[entity name]: [brief definition]",
    "knowledge_type": "conceptual", "temporality": "durable"}
   EOF

All write commands take JSON on stdin. Never pass content as CLI flags.
Do not explain your reasoning. Classify, store, link, done.
```

## Explicit Skills

For when the user wants direct control. These are the exception, not the normal flow.

### `/gramaton-process`

Classify pending (unclassified) records in bulk.

```markdown
Skill prompt:
1. Run: gramaton pending
2. For each pending record:
   a. Read the content
   b. Classify using the standard schema
   c. Run: gramaton classify <id> --temporality ... --confidence ... etc
   d. Search for related records and create edges
3. Report: "Processed N records. [summary of what was classified]"
```

### `/gramaton-curate`

Run LLM-requiring maintenance tasks.

```markdown
Skill prompt:
1. Check for contradictions:
   - Search for records with overlapping keywords but different claims
   - Evaluate whether they genuinely conflict
   - Present conflicts to user for resolution

2. Check for duplicate concepts:
   - gramaton search for concept nodes with similar names/definitions
   - Propose merges for user confirmation

3. Check for stale records:
   - gramaton search --temporality temporal --since [old date]
   - Evaluate whether temporal records are still valid
   - Propose updates or expiry

Report findings and ask for user confirmation before making changes.
```

### `/gramaton-search`

Manual search when the user explicitly wants to query the knowledge store.

```markdown
Skill prompt:
1. Run: gramaton search "<user's query>" --top 10
2. Present results with: keywords, short summary, confidence, temporality
3. Ask if user wants to inspect any specific record
4. If yes: gramaton inspect <id>, present full content and related records
```

## Integration with Different Frameworks

### Claude Code

- System prompt: `CLAUDE.md` in project root
- Tools: CLI commands via shell, or MCP tools wrapping the CLI
- Subagents: supported natively via Agent tool
- Skills: supported natively

### Kiro CLI

- System prompt: agent configuration
- Tools: MCP tools or shell commands
- Subagents: supported via skill-based delegation
- Skills: native skill system

### Custom Agent Frameworks

- System prompt: include the template above in whatever system prompt mechanism the framework uses
- Tools: wrap `gramaton` CLI commands as tool definitions (OpenAI function calling format, MCP, etc.)
- Subagents: depends on framework — see Capture Modes below
- Skills: implement as whatever the framework calls reusable prompt patterns

## Capture Modes

Three ways for an agent to capture knowledge. The Gramaton service receives the same CLI calls regardless — the difference is where classification happens and how much context it costs.

### Mode 1: Subagent Capture (Preferred)

The main agent spawns a separate agent context to classify and store. Zero context pollution in the main conversation.

**Use when:** The framework supports subagents or parallel agent invocations (Claude Code, Kiro CLI, custom agents with worker patterns).

```
Main agent: spawns subagent with content + context envelope → continues immediately
Subagent:   classifies → calls gramaton capture with metadata → searches for related → creates edges → exits
```

**Context cost:** Minimal — just the spawn call in the main agent's context. Classification reasoning happens in the subagent's context (discarded when done).

### Mode 2: Inline Capture

The main agent classifies and stores directly, within the current conversation context.

**Use when:** The framework doesn't support subagents, and the knowledge is worth classifying now. Best for simple, quick captures where classification is obvious.

```
Agent: classifies inline → calls gramaton capture with metadata → continues conversation
```

**Context cost:** Low-medium. The agent spends 100-300 tokens reasoning about classification. The user may see a brief pause or a tool call in the conversation.

**System prompt adjustment:** Replace "spawn a subagent" with:

```markdown
When capturing knowledge, classify it directly and call gramaton capture 
with all metadata flags. Keep your classification reasoning brief — 
classify and store, don't explain the classification to the user.
```

### Mode 3: Raw Capture

The agent stores content with no classification. One CLI call, zero reasoning overhead.

**Use when:** The framework is very constrained, or the agent is mid-task and doesn't want any capture overhead. Also useful for bulk ingestion where classification can happen later.

```
Agent: calls gramaton capture --content "..." → done
```

**Context cost:** Negligible — one tool call.

**Tradeoff:** The record has embeddings only. No temporality, confidence, keywords, or summary. Still searchable by vector similarity, but no metadata-filtered retrieval until piggyback curation classifies it later.

### Choosing a Mode

| Framework Capability | Recommended Mode |
|---|---|
| Subagents supported | Mode 1 (subagent) — always preferred |
| No subagents, single agent loop | Mode 2 (inline) for important knowledge, Mode 3 (raw) for quick captures |
| Bulk ingestion / scripts | Mode 3 (raw) — classify in batch later via `/gramaton-process` |

All three modes produce valid records in the store. The difference is when and how metadata gets attached — immediately by subagent, immediately inline, or deferred to curation.

## What "Transparent" Looks Like

A typical session from the user's perspective:

```
User: "How should we handle auth for the new microservice?"

Agent: [searches Gramaton, finds auth architecture decisions and security policies]
       "Based on our established patterns, we use OAuth2 with JWT tokens
        for service-to-service auth. The security team requires all new
        services to register with the central auth service (see PLAT-301).
        The token rotation policy is every 24h..."

User: "Makes sense. Let's also add rate limiting at the gateway level,
       100 req/sec per client."

Agent: [captures: rate limiting decision, links to microservice
        project, auth service concept, gateway concept]
       "Good call. I'll set up the rate limiter config in the gateway.
        For the 100 req/sec limit, we should also add a burst allowance..."

User: [doesn't know Gramaton was searched or that the decision was captured]
```
