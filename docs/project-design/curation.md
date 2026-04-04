# Curation

## Overview

Curation is the background maintenance that keeps the knowledge store healthy over time. Analogous to the brain's default mode network — consolidating, connecting, and pruning during idle time.

Curation tasks split into two categories based on whether they need an LLM.

## Deterministic Curation (Always Runs, No LLM)

These run inside the Gramaton server process on a schedule or trigger. They're pure computation — no judgment calls.

| Task | What It Does | Trigger |
|------|-------------|---------|
| **Lifecycle transitions** | Expire nodes past `valid_until`. Flag ephemeral nodes that haven't been accessed recently. | Scheduled (periodic) |
| **Staleness flagging** | Find records that should be re-verified based on age, temporality, and access patterns. | Scheduled (periodic) |
| **Access statistics** | Compute aggregate stats: record counts by type, domain coverage, temporal distribution. | Scheduled (periodic) |
| **Manifest rebuild** | Regenerate the store manifest (domains, topics, counts, temporal range). | Scheduled or on-demand |
| **Decay scoring** | Not a separate task — decay is computed at query time as pure math. No writes needed. | On every query |

### Decay and Scoring

Decay scoring and spreading activation are defined in [Retrieval — Scoring Model](retrieval.md#scoring-model). That is the single authoritative definition.

The short version: decay is computed at query time (pure math, no writes). Spreading activation writes `activation_boost` to neighbors on access. Both are retrieval-layer concerns, not curation jobs. The only curation-relevant aspect is that **lifecycle transitions** (expiring records past `valid_until`, flagging stale ephemeral nodes) are eager — they're deterministic curation tasks, not decay math.

## LLM-Requiring Curation (Via Agent Skills)

These need judgment. They run in agent sessions via subagents or explicit skills, not inside the server.

| Task | What It Does | How It Runs |
|------|-------------|-------------|
| **Process pending records** | Classify unclassified records (temporality, confidence, knowledge_type, keywords, summaries) | Piggyback or `/gramaton-process` |
| **Concept promotion** | Promote frequently-occurring keywords to concept nodes when they cross the emergence threshold | Piggyback or `/gramaton-curate` |
| **Contradiction detection** | Find records that conflict with each other, flag or resolve | `/gramaton-curate` skill |
| **Concept consolidation** | Merge duplicate concept nodes ("k8s" and "Kubernetes") | `/gramaton-curate` skill |
| **Summary regeneration** | Re-generate summaries for records whose content or understanding has changed | `/gramaton-curate` skill |
| **Gap analysis** | Identify areas where the knowledge store has thin coverage | `/gramaton-curate` skill |

### Piggyback Curation (Best-Effort, Every Session)

LLM curation runs opportunistically during normal agent sessions — no explicit user action needed.

**Important: piggyback curation is best-effort, not guaranteed.** The server signals `curation_overdue` in CLI responses. The agent's system prompt instructs it to spawn a curation subagent. But LLMs may ignore this signal, especially under conversation pressure or when the user's request is urgent. This is acceptable — the system degrades gracefully:

- Records stay at `processing_status: captured` longer (still searchable by embedding, just less metadata)
- Concept promotion is delayed (keywords still work for retrieval)
- Contradictions go undetected longer (existing records still return normally)

Nothing breaks. Quality improves more slowly. The explicit `/gramaton-curate` skill is the guaranteed fallback for users who want a full curation pass.

**How it works:**

1. The Gramaton server tracks when curation last ran and what's pending
2. When an agent makes any CLI call (e.g., `gramaton search`), the response includes curation status:
   ```json
   {
     "results": [...],
     "curation": {
       "overdue": true,
       "pending_count": 14,
       "last_curated": "2026-04-01T10:00:00Z",
       "promotion_candidates": 3
     }
   }
   ```
3. The agent's system prompt tells it to spawn a curation subagent when it sees `overdue: true`
4. The subagent processes pending records in priority order (most important/recent first) and promotes eligible concepts
5. Runs once per session, in the background, without interrupting the user

**Priority ordering for pending records:**
- Records with higher importance signals first (explicit importance flag, or heuristics like length, source credibility)
- More recent records before older ones
- Records that have been accessed (searched/returned) but are unclassified get priority — they're already proving useful

**Token cost:** A typical piggyback run (10-20 pending records, a few concept promotions) takes 1-2 minutes and consumes roughly the same tokens as a medium-length conversation exchange. For large backlogs (200+ records), it may take 10+ minutes — the priority ordering ensures the most valuable records are processed first even if the session ends before completion.

### Explicit Skills (User-Initiated)

For comprehensive curation or when the user wants direct control.

**`/gramaton-process`** — Full classification pass on all pending records.

1. Agent calls `gramaton pending` — gets list of unclassified records
2. For each record, agent reads the content
3. Agent classifies and calls `gramaton classify <id> --temporality ... --confidence ...`
4. Agent searches for related nodes and creates edges

**`/gramaton-curate`** — Full maintenance pass including judgment-heavy tasks.

1. Agent calls `gramaton search` with broad queries to find potential issues
2. Evaluates contradictions, duplicates, stale records
3. Scans for keywords that should be promoted to concept nodes
4. Resolves via `gramaton update` calls (update metadata, create/remove edges, merge concepts)
5. For destructive operations (merging concepts, marking records as refuted), presents findings to the user for confirmation

### Curation Autonomy

- **Fully autonomous:** Deterministic tasks (lifecycle, staleness, stats). No user involvement. Runs server-side.
- **Piggyback autonomous:** Pending record classification and concept promotion. Runs via subagent without user involvement when the server signals it's overdue.
- **User-confirmed:** Contradiction resolution, concept merging, record refutation. Agent presents findings, user decides. Runs via `/gramaton-curate`.

### Scheduled Autonomous Curation (v2)

For users who want fully autonomous curation without relying on active agent sessions, v2 will support an optional direct LLM API provider on the server. This enables scheduled curation (cron, launchd, Task Scheduler) that runs independently. Not needed for v0.1 — piggyback curation covers the primary use case.

## Branch-Based Curation Safety

All LLM-requiring curation operates on branches, not directly on main. This applies to both piggyback curation and explicit `/gramaton-curate`.

```
main ─────────────────────────────────●────────►
                                     ╱
curation-2026-04-03 ───●───●───●───●
                       │   │   │   │
                   classify │  promote │
                       pending  concepts
```

The curation subagent:
1. Creates a branch: `gramaton branch create "curation-<date>"`
2. Runs all curation operations on the branch
3. Diffs the branch: `gramaton diff main..curation-<date>`
4. Self-reviews the diff — flags anything suspicious
5. Merges unambiguous changes: `gramaton branch merge "curation-<date>"`
6. Queues suspicious changes for user review at next natural pause

This prevents bad curation from corrupting the main store. If a subagent misclassifies records or makes bad concept promotions, the branch is discarded — not undone.

## Store Manifest

A lightweight summary of what the knowledge store contains. Used by agents to decide whether to search Gramaton for a given query.

```json
{
  "domains": ["API design", "infrastructure", "security"],
  "projects": ["Event Pipeline", "Auth Service Rewrite"],
  "source_types": ["architecture decisions", "post-mortems", "user preferences"],
  "temporal_range": {"earliest": "2025-06", "latest": "2026-04"},
  "record_count": 2403,
  "concept_count": 347,
  "pending_count": 12,
  "strengths": "Strong on infrastructure decisions and incident analysis. Weak on frontend patterns."
}
```

Injected into the agent's system prompt (not fetched via tool call) so the agent knows what the store covers before deciding whether to search.

**Maintenance:** Stats (counts, domains, ranges) regenerated automatically by deterministic curation. The qualitative section (strengths, gaps) updated by `/gramaton-curate` or manually.
