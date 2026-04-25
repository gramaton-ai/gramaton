---
name: benchmark-extract
description: Use to ingest a LongMemEval-style benchmark dataset into the isolated benchmark gramaton store via Claude Code sub-agents. Each unique haystack session becomes one Gramaton session via the production `session_prepare` → `session_commit` path. User-triggered by "run benchmark extraction", "ingest LongMemEval-S", "load benchmark data". Requires the `gramaton-bench` MCP server live on port 7338; see docs/benchmarks.md for setup.
---

# benchmark-extract

Drives the production session-extraction code path against a benchmark
dataset through Claude Code Agent sub-agents, one sub-agent per unique
haystack session. All writes go to the `gramaton-bench` MCP toolset
(never `gramaton`). Deliberate isolation — see docs/benchmarks.md for
why.

## When to run

- User explicitly requests ingestion of a benchmark dataset (LongMemEval-S,
  LongMemEval-M, MuSiQue, etc.).
- Always preceded by a design alignment on subset size (pilot vs full).

Do NOT run autonomously. Extraction spends significant subscription quota
and wall-time; the user drives the cadence.

## Preconditions

1. **Dataset file** exists at the path the user specifies (for
   LongMemEval-S: `~/workspaces/gramaton-benchmarks/longmemeval/raw/longmemeval_s_cleaned.json`).
2. **Benchmark store running** on port 7338 with `gramaton-bench` MCP
   tools available in this Claude Code session. Verify with a
   `mcp__gramaton-bench__gramaton_stats` call; if it fails, stop and ask
   the user to start the server per `docs/benchmarks.md`.
3. **Personal store is NOT the target.** Any `mcp__gramaton__*` call in
   this skill is a bug.

## Session id convention

Upstream ids (e.g. `sharegpt_yywfIrx_0`, `85a1be56_1`, `answer_280352e9`)
are used verbatim with a dataset prefix: `lme-s-<haystack_session_id>`.
Prefix makes origin unambiguous in the bench store; preserving the
upstream id preserves traceability back to the dataset.

## Flow

### 1. Load and parse

Read the dataset JSON. For LongMemEval-S it's an array of 500
instances; each has `question_id`, `question`, `answer`,
`question_date`, `haystack_session_ids[]`, `haystack_dates[]`,
`haystack_sessions[]` (list of list of `{role, content}` turns),
`answer_session_ids[]`, `question_type`.

Build a map `session_id → (transcript_turns, session_date,
source_instances[])`. Many sessions recur across instances (~19k unique
across 24k refs for LongMemEval-S). Deduplicate: each unique session_id
is processed exactly once.

### 2. Determine subset

The user specifies: full dataset, first-N instances, or a list of
question_ids. Collect the union of `haystack_session_ids` across the
chosen instances.

### 3. Resume via store query

Query the bench store for already-committed session_ids:

```
mcp__gramaton-bench__gramaton_search(
    store="sessions",
    prefix_session_id="lme-s-",
    top=10000,
)
```

Build a set `already_done`. Skip any candidate session_id already in
that set. Store is the single source of truth; no append-only ledger
file.

### 4. Dispatch sub-agents

Parallelism: **4 concurrent sub-agents** (user-configurable; start at 4,
raise after observing rate-limit behavior and wall-time).

Each sub-agent receives:

- The literal `session_id` to use (`lme-s-<haystack_session_id>`).
- The transcript formatted as a single text block (role-prefixed,
  turn-separated — see "Transcript formatting" below).
- The session date (`haystack_dates[i]`), to be passed as
  `asserted_as_of` context.
- An explicit instruction to use ONLY `mcp__gramaton-bench__*` tools.

Launch them in batches of 4 via the Agent tool. Track completions.

### 5. Sub-agent contract

Each sub-agent's prompt is self-contained. The transcript is NOT
embedded in the prompt (too large for many sessions); the prompt
passes a filesystem path and the sub-agent reads it.

Template:

```
You are ingesting one benchmark chat session into the gramaton-bench
store via Gramaton's production session-extraction path. Treat the
transcript at the provided path as "the conversation" that Gramaton's
extraction instructions refer to.

Do these steps exactly:

1. Read the transcript file at <TRANSCRIPT_PATH>. The first few lines
   are metadata (session_id, source_session_id, session_date,
   source_instance). The transcript itself follows the `---`
   separator, formatted as [ROLE] blocks.

2. Call mcp__gramaton-bench__gramaton_session_start with:
     client_session_id = "<CLIENT_SESSION_ID>"
   Record the returned `id` field (a ULID). This is the SESSION_ULID
   used for the next two calls. If `resumed: true` and topics is
   non-empty, a prior run already committed — return "already done:
   <CLIENT_SESSION_ID>" without calling commit again.

3. Call mcp__gramaton-bench__gramaton_session_prepare with:
     session_id = SESSION_ULID

4. Read the extraction instructions returned by session_prepare.
   Apply them to the transcript (treat the transcript's user/assistant
   turns as the conversation the instructions refer to). Produce a
   list of segments.

5. Call mcp__gramaton-bench__gramaton_session_commit with:
     session_id = SESSION_ULID
     segments   = <your extracted segments>
   Default `promote_to_memory: true` — do NOT override.

6. Return a single line: "committed <N> segments for
   <CLIENT_SESSION_ID>". Nothing else.

Rules:
- Only use mcp__gramaton-bench__* tools. Never mcp__gramaton__*.
- Do not invent content beyond what's in the transcript file.
- session_prepare takes the ULID (step 2 result), not the
  client_session_id. Using the wrong id returns "not_found".
- Do not call session_commit without calling session_prepare first.
```

Sub-agent type: `general-purpose`. Isolated context keeps each
transcript from competing for the main-agent's window.

### 6. Transcript formatting

Each haystack session is a list of turns `[{role, content}, ...]`.
Format as:

```
[USER]
<content>

[ASSISTANT]
<content>

[USER]
<content>
...
```

No JSON. Plain text is what a real conversation would look like to the
extraction LLM. Preserve turn order.

### 7. Track per-sub-agent result

For each completed sub-agent:

- On success: increment `committed_count`, record session_id.
- On failure: record `(session_id, error)`. Retry ONCE after a brief
  back-off. If second attempt fails, log and move on — do not block the
  batch.

### 8. Final report

When all dispatched sub-agents have returned, emit one report:

```
Benchmark extraction complete
  Dataset: LongMemEval-S cleaned
  Subset:  <N instances> → <M unique sessions>
  Already done (skipped): <K>
  Committed this run:     <C>
  Retried once:           <R1>
  Failed after retry:     <F>
  Wall time:              <T>
```

If `F > 0`, list the failed session_ids so the user can re-drive a
targeted retry.

## What this skill does NOT do

- **Load questions/gold evidence into a collection.** Separate step
  after all sessions are in — uses `gramaton_collection_add_batch`
  (P1-78) against a `longmemeval-s-questions` collection.
- **Snapshot to ndjson.** Reproducibility artifact is a separate
  pass; extraction and snapshotting are decoupled so re-extraction
  doesn't force a re-snapshot.
- **Run eval.** Eval is a separate pass against the populated
  bench store.

These are sibling tasks, not promised future skills — they happen
on the user's drive when needed.

## Gotchas

- The prepare/commit pair rejects `commit` if `prepare` wasn't called
  first for that session_id. Each sub-agent MUST call prepare, not
  assume a prior state.
- `promote_to_memory: true` is the default — the benchmark test depends
  on the memory-store records being present. Never override to `false`
  in this skill.
- If the bench server isn't actually running on 7338, the MCP calls
  return stale-client errors that look like tool misuse. Verify server
  liveness with `mcp__gramaton-bench__gramaton_stats` before dispatch.
- Large transcripts (>30k tokens) may trip sub-agent context issues.
  If a sub-agent fails with context-exceeded, skip that session and
  record it in the failure list — don't truncate silently.

## Rollout

1. **First run: 10-instance pilot.** ~470 unique sessions. Target wall
   time under 2 hours. Observe quota burn rate and failure mode
   distribution. Record in `~/workspaces/gramaton-benchmarks/RUNS.md`.
2. **Second run: 50 instances** if pilot is clean. Decision point on
   parallelism.
3. **Full LongMemEval-S (500 instances, ~19k sessions)** only after
   pilots validate.
4. **LongMemEval-M and full run driver decision (subscription vs
   metered API)** deferred to a separate discussion.

## Reference

- Design doc: `~/workspaces/gramaton-inspection/bulk-ingest.md` Parts B, C, D.
- Bench-store setup: `docs/benchmarks.md`.
- Extraction prompt (authoritative): whatever `session_prepare`
  returns; do not re-tune in this skill.
