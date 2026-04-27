# How to use Gramaton

Practical tips for driving Gramaton through Claude or another agent. If
you've installed Gramaton (via `gramaton init`) and your agent's MCP
client picks up the `gramaton_*` tools, this is the doc that tells you
what to actually say to make those tools earn their keep.

For the operator reference (config knobs, providers, backups), see
[docs/configuration.md](docs/configuration.md). For building tools or
skills *on top of* Gramaton, see
[docs/integrator-guide.md](docs/integrator-guide.md). For how the
internals fit together, see [docs/architecture.md](docs/architecture.md).

All examples below use invented content — none of it represents real
data from any specific user.

## The 30-second mental model

Gramaton is a knowledge store with three buckets. Knowing which bucket
to ask for changes what your agent does:

| You ask                         | Bucket    | What happens                              |
|----------------------------------|-----------|-------------------------------------------|
| "remember this"                  | Memory    | Direct capture; ranked retrieval later   |
| "add a TODO" / "track this"      | Collection| Structured item; exhaustive listing later|
| (nothing — just talk)            | Session   | Conversation auto-extracted at checkpoint|

You only have to know one rule: **will missing one item be a
failure?** If yes (action items, tickets, packing lists), it's a
collection. If no (decisions, research, preferences, "we figured out
X works better than Y"), it's memory.

Sessions are how Gramaton harvests memory automatically from
conversation, without you asking. You don't have to think about
sessions much — your agent triggers them at natural breakpoints.

## Capturing knowledge

### When to ask explicitly

Ask the agent to capture when:

- **You make a decision you'll want to recall.** "We're going with
  per-tenant rate limits keyed by API token, not IP." → "Capture that
  as a decision."
- **You learn a non-obvious fact about the system.** "The retry policy
  in the payments service uses jittered exponential backoff with a
  max of 30s." → "Remember that."
- **You discover a constraint that should outlive this conversation.**
  "The compliance team requires audit logs to live in cold storage for
  seven years." → "Save that — it'll come up next time we touch logs."

Ask via natural language. Examples:

- "Capture this: the payment retry budget is 5 attempts over 90 seconds."
- "Save the gist of what we just figured out about cache invalidation."
- "Remember that the rate-limit threshold is per-org, not per-user."

### What NOT to capture

- Trivial chat. "Hi", "thanks", small talk.
- Questions without answers.
- Half-formed ideas you're still working through. Wait until they
  solidify.
- Things that are already in the codebase. Code is its own source of
  truth; don't duplicate "we use Redis" into a memory record when the
  config file says it.

### Don't pre-summarize

If you find yourself writing a tight one-liner before asking the agent
to capture, stop. Hand it the **raw decision** — the actual text, the
actual reasoning. Gramaton's curation pipeline generates the summary,
keywords, and embeddings. If you summarize first, you lose the
reasoning that made the decision useful, and the embedding gets
generated from a tagline instead of the substance.

Bad:

> "Capture: we picked Postgres."

Good:

> "Capture this: we evaluated Postgres vs MySQL for the new service
> and went with Postgres because the team's already running three
> Postgres clusters and the tooling investment is paid down. MySQL
> would have been ~10% faster on the dominant query but the
> operational duplication wasn't worth it."

The curation pipeline turns the second one into a searchable record
with `keywords: [postgres, mysql, database, decision]`,
`temporality: durable`, `epistemic_status: well_established`, and a
useful `summary_short`. The first one becomes a record that says
"we picked Postgres" and not much else.

### Sessions handle most captures for you

You don't have to manually capture every interesting thing. Your
agent runs `gramaton_session_prepare` and `gramaton_session_commit`
at natural checkpoints (decision lands, topic pivots, you say "ship
it"). The session extracts segments from the conversation, including
the reasoning, and creates Memory records automatically.

Manual captures still matter for:

- Decisions you want to commit *immediately* (don't wait for the
  next checkpoint).
- Knowledge from outside the conversation (you found a fact
  externally, want to file it).
- Long-form knowledge — research notes, design docs in conversation.

## Searching

### The phrasings that work well

Gramaton's search blends vector similarity, BM25 keyword match, and
metadata filters. You usually don't need to think about this; just
phrase the query as you'd ask a coworker:

- "What did we decide about caching?"
- "Show me anything related to the rate limiter design."
- "What's our current thinking on auth?"
- "Find the decision about Postgres vs MySQL."

### When the ranked answer isn't enough

Search returns ranked top-N. For some questions you want **every**
match:

- **Tickets / TODOs.** "Show me the open tickets in the dev backlog"
  — that's a collection listing, not a search. Use the collection
  tool (`gramaton_collection_items`).
- **Records missing a field.** "Find every record without a
  classification" → search with `missing=[temporality]`. Your agent
  can do this when you ask.
- **Literal text match.** "Find any record mentioning 'RWMutex' as a
  literal substring" → ask for `match=` rather than vector search.

### "What did I tell you yesterday?"

Sessions index the conversation itself. If you're trying to recall
something from a past conversation, ask the agent to search Sessions
specifically:

- "What did we discuss about caching last week?" → searches sessions.
- "Pull up the conversation where we picked Postgres." → same.

The agent's `gramaton_search` call with `store=sessions` narrows to
conversation-derived content.

## Backlogs, TODOs, and tickets

Gramaton's collections are the right home for action items —
anything where missing one is a failure. Common patterns:

- **A dev backlog**: tickets with status (`open` / `in_progress` /
  `resolved` / `abandoned`) and severity (`P0` / `P1` / `P2` / `P3`).
- **A reading list** with status (`unread` / `reading` / `read`).
- **A packing list**, shopping list, etc. — small, exhaustive,
  status-tracked.

### Filing items

The agent files via `gramaton_collection_add`:

- "File a ticket for the auth middleware refactor — high priority,
  blocked on the new IAM library."
- "Add a TODO: write integration tests for the contradiction
  detector."
- "Track this as a P2: the manifest cache misses too often when
  records have keyword-only updates."

The collection schema validates the fields. If you ask for a status
the schema doesn't recognize, you'll get an error — fix the request,
not the schema, unless the schema is genuinely missing a state.

### Closing items — explicit, every time

When work on a ticket lands, the ticket is **not closed
automatically**. You have to ask:

- "Close ticket 01KZZZAUTHFIX as completed."
- "Mark these three tickets done: 01KZZZ..., 01KZZZ..., 01KZZZ..."
- "Resolve the auth-middleware tracker — note that we shipped
  commit abc123."

Why not automatic? The closure flips `valid_until`, writes a
resolution note, and creates an audit edge — it's a deliberate state
change, not an inference from conversation. Auto-closing on "looks
like we finished it" produces false positives that silently lose
state.

> ### Important: session prepare/commit does NOT close tickets
>
> If you ask Gramaton to do a session checkpoint ("wrap up and
> capture what we did"), the session captures the **conversation**
> (decisions, learnings, context) but does NOT touch your collection
> tickets. You'll see Memory records about the work, but the
> tickets stay `status: open` until you explicitly close them.
>
> Close tickets first, **then** ask for a session commit. Or close
> them after — order doesn't matter mechanically. What matters is
> that closure is its own ask. "We finished those three tickets"
> doesn't close them; "close those three tickets" does.

### Listing items

`gramaton_collection_items` returns every item — exhaustive, not
ranked. This is the difference vs Memory.

- "List all open tickets in the dev backlog."
- "Show me everything in the reading list, including read items."
- "Filter the dev backlog to severity=P1 or P0."

The output includes every match. If you want a top-N, your agent
sorts after retrieval; the tool itself doesn't truncate.

## Common pitfalls

**"I asked you to remember it, but it doesn't show up in search."**

Curation classifies and embeds in the background (default: every
minute). Right after a capture, the record exists but isn't yet
classified — temporality, knowledge_type, summary_short are
unset, and the embedding may not be generated. Wait a cycle, then
search. If it's still missing after ~5 minutes, ask the agent to
run `gramaton_curation(action="status")` to see if curation is
running.

**"The session said we finished those tickets, but they still
show open."**

See above — sessions don't close tickets. Close them explicitly.

**"There are duplicates of the same decision."**

Gramaton auto-supersedes when a new capture is very similar (cosine
>= 0.92) to an existing record — the older one gets `valid_until`
and a `supersedes` edge. If you're seeing duplicates anyway, the
two phrasings probably embedded too far apart for the threshold.
Ask the agent to run `gramaton_duplicates(threshold=0.85)` to find
near-misses, then merge or supersede manually.

**"I asked for a summary and got pre-digested content back."**

Gramaton stores three layers of compression: `content_full`
(unbounded), `content_short` (~750 char embedded anchor),
`keywords` (3-8 terms). When the agent's reading from search
results, it sees `content_short` first. If you want the full
content, ask the agent to `gramaton_inspect` a specific record —
that returns `content_full`.

**"Why is this old decision still ranking high?"**

Memory records have a temporality. `durable` records (most
decisions) decay slowly over years. If something stale keeps
surfacing, the right move is usually to capture the *new* state
explicitly — auto-supersession will retire the old one. Don't try
to "fix" search ranking by tweaking config; the freshness signal
works.

**"The agent keeps saying it's stuck on classification."**

A pathological record — oversized, content-policy refusal,
malformed input — can flunk classification multiple times in a row.
After three attempts, Gramaton flips `processing_status` to
`stuck` and stops trying. To find them: ask the agent to
`gramaton_search(processing_status="stuck")`. To unstick: fix the
record (`gramaton_update`) or resolve it.

## Verifying things landed

After a capture or commit, you can confirm it stuck:

- "Search for the decision I just captured." — vector + keyword.
- "Show me the most recent records." — `gramaton_search(sort=created_at)`.
- "Inspect the record you just created." — by ID.
- "List all items in the dev backlog." — exhaustive collection.

If a record is *missing* and not stuck:

- Curation may not have run yet — wait, or check status.
- The capture may have failed silently — ask the agent to retry,
  or check the server log (`~/.gramaton/data/server.log`).
- The classification may have produced a different
  knowledge_type than you expected — try a broader search.

## When something feels off

A few diagnostic asks:

- "What's the curation status?" → pending count, last cycle
  outcome, autonomous on/off.
- "Show stats on the store." → record counts, edge counts, store
  size, top knowledge_types.
- "Find orphans." → records with no edges; usually a sign of
  classification gaps.
- "Run curation now." → forces an immediate cycle if you don't
  want to wait the full minute.

If problems persist, the server log lives at
`~/.gramaton/data/server.log` (or wherever your `data_dir` points).
Curation errors and LLM-call failures are logged at WARN with the
record ID; that's enough to triage most issues.

## What you don't need to think about

- **Embedding model upgrades.** When the embedding model changes,
  records get re-embedded automatically (or via
  `gramaton reembed`).
- **Backups.** Daily backups happen on a schedule (see
  `backup.schedule` in config).
- **Index rebuilds.** Search indexes rebuild on startup if they
  drift. No manual intervention needed.
- **Garbage collection.** GC is off by default; commit history is
  cheap to keep, and you'll want it for the audit trail.

You only think about Gramaton when you have something to capture,
something to find, or something to track. Everything else is the
agent's problem.

## Related docs

- [README.md](README.md) — install + first-run flow.
- [docs/configuration.md](docs/configuration.md) — every knob, with
  defaults and tradeoffs.
- [docs/providers.md](docs/providers.md) — picking an LLM provider
  (Anthropic, OpenAI, Bedrock).
- [docs/integrator-guide.md](docs/integrator-guide.md) — for people
  building agents or tools on top of Gramaton.
- [docs/architecture.md](docs/architecture.md) — how the internals
  fit together.
