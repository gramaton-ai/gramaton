# Knowledge Extraction Instructions

You are extracting knowledge from the current conversation so a *future*
agent — possibly you tomorrow, possibly someone else — can answer
questions from it. Treat that future agent as your reader. They will
not have access to this conversation. They will have search.

## The Question Types Your Segments Need to Answer

Future searches will ask things like:

- **Single-fact recall.** "What did we decide about X?"
- **Current position.** "What's our position on X *now*?" (relative to
  earlier, possibly contradictory positions)
- **Decision rationale.** "Why did we decide Y?"
- **What we tried.** "Did we already consider X? What happened?"
- **What's open.** "What's still unresolved?"
- **What changed.** "What's new since I was last here?"
- **Implicit preferences.** "Based on past behavior, what does the user
  prefer?"

Each of these benefits from *different* metadata classifications. The
classification fields aren't paperwork — each one is a dimension of
"what kind of question does this segment answer?"

## Field Roles (Critical — Read This Before Writing)

The three content fields serve **three different parts of retrieval**.
They are not nested compressions of the same text.

- **`content`** — Unbounded. Full self-contained reading. **MUST
  include the rationale, alternatives considered, why-not's, concrete
  details (file paths, numbers, names, IDs), and constraints that
  shaped the decision.** Read by humans and agents *after* a search
  match. Aim for "self-contained paragraph" as a *floor*, not a
  ceiling. Anti-patterns to avoid:
  - "We discussed X" (substance lost)
  - "User decided Y" (the *why* lost)
  - "The team chose Z" (everything lost)
  Better: "We chose bbolt over Badger for storage because bbolt's
  single-writer model matches our current throughput needs and
  simplifies recovery; Badger was rejected due to its operational
  complexity at our scale (current target is <10K writes/sec)."

- **`summary_short`** — Up to ~750 chars. **This is the embedding-ready
  semantic anchor of the segment.** When present, it is what gets
  vector-embedded for similarity search. Treat it as the canonical
  representation of what the segment is *about* — not a tagline.
  Include the topic, the core claim, the distinguishing features that
  make it findable by meaning. The 750-char budget is intentional:
  longer than a headline, short enough to embed cleanly.

- **`keywords`** — 3–8 search terms a *future agent would type*. Not
  literal phrases from this conversation. If the segment is about
  switching from prolly trees to bbolt, keywords might be
  `["storage", "bbolt", "prolly-trees", "migration", "performance"]` —
  even if "migration" was never said. BM25 weighting boost on these.

The `content` is for comprehension; `summary_short` is for semantic
recall; `keywords` are for keyword recall. Different searches will
hit different fields. Fill all three thoughtfully.

## Synthesis, Not Summarization

Rewrite the knowledge in the form most useful to a future reader who
wasn't in the room. Do *not* pick the most distinctive sentence (that's
TF-IDF behavior — bad). Do *not* compress by shortening (that loses
the why). Reorganize, restate, and synthesize so the segment stands
alone.

The findability metadata should be what someone would **search for
later**, not what was literally said. Names a phrase no one used during
the conversation if that phrase is what the question would be asked
with.

## Classification Heuristics (How to Choose)

Each field maps to question types. Don't pick defaults; pick the value
that signals what your segment is good for.

### `temporality` — "Is this still true?"
- `immutable`: Always true. Definitions, axioms, fundamental constraints.
- `durable`: Stable until contradicted. Most decisions. Architectural choices.
- `temporal`: Time-bound; may go stale. Roadmap items, version-specific
  facts, things tied to a release.
- `ephemeral`: Very short lifespan. "User is at the airport." Open
  questions in flight.

### `confidence` — How likely is this correct?
- 0.9+: Settled, reviewed, demonstrated. Use for landed decisions.
- 0.7–0.9: Strong but not bulletproof. Most well-reasoned conclusions.
- 0.4–0.7: Tentative. Things still in active debate or partial.
- 0.0–0.4: Highly uncertain. Speculation, unverified claims, dead ends
  recorded for posterity.

### `knowledge_type` — What kind of question does this answer?
- `episodic`: "What happened?" An event with time context.
- `semantic`: "What's true?" A general fact or established knowledge.
- `procedural`: "How do I X?" A how-to or process.
- `conceptual`: "What does X mean / why does X exist?" A definition or
  principle.
- `reference`: "What's the value of X?" Lookup data.

### `epistemic_status` — How strongly should the reader rely on this?
- `well_established`: Broadly accepted; demonstrated; landed.
- `probable`: Likely true; well-reasoned but not yet validated.
- `speculative`: Uncertain; explore-but-don't-trust. **Use this for
  open questions, exploration, and dead ends — they belong in the
  store, just flagged accurately.**
- `contested`: Conflicting evidence. Rare in conversation; common in
  research synthesis.
- `refuted`: Shown to be false. Use deliberately for "we tried X and
  it didn't work" so future searches see the dead end and don't
  re-walk it.

## `promote_to_memory` — The Two-Tier Decision

Every segment becomes a Session segment (BM25-searchable, captures
the conversation thread). When `promote_to_memory: true` (the default
when omitted), it *also* becomes a Memory record (vector-embedded,
auto-supersession, full lifecycle).

**Promote (true / omit)** when the segment is decision-grade knowledge
worth surfacing in semantic search:
- Decisions and their rationale
- User preferences
- Established facts, constraints, architectural choices
- Procedures and how-to's
- Research findings

**Session-only (false)** when the segment is *valuable context* but
shouldn't pollute the Memory store's vector space:
- Open questions that haven't resolved (capture them so a future
  agent knows the question is still on the table — but they aren't
  Memory-grade until answered)
- Pure exploration / "we considered X but moved on" (record that the
  path was explored, but don't put it in vector recall where it
  competes with actual decisions)
- Conversational context that helps reading the session but isn't
  standalone knowledge ("user mentioned being on a flaky wifi during
  this session")
- Throwaway-but-might-matter facts where ranking the wrong way would
  be worse than not embedding

A heuristic: **if a future agent searches semantically for this topic,
would this segment be a useful answer or noise?** If useful → promote.
If noise but worth searching for explicitly by keyword → Session-only.

## Dedup vs. Supersession (They Are Different)

Look at the session state returned with these instructions before
extracting.

- **Skip restatements.** If a segment covers the same ground as an
  already-captured segment with no new information, don't recapture.
- **Capture updates and contradictions.** If a new turn refines,
  changes, or reverses an earlier captured segment, *do* capture the
  new version. Auto-supersession (cosine ≥ 0.92 against existing
  Memory) will mark the older record historical and create a
  `supersedes` edge automatically. This is the system's primary
  mechanism for "current position" question types — don't disable it
  by aggressive deduping.

## What to Skip

Be narrow. The metadata system handles uncertainty; only skip content
with **no future value**:

- Greetings, small talk, pleasantries.
- Restatements that add nothing to a prior captured segment (see
  Dedup above — but capture *updates*).
- Confused exchanges that produced no question and no answer.
- Mechanical tool-call narration ("I ran ls and saw 3 files") unless
  the result itself is the knowledge.

**Do NOT skip:**
- In-progress reasoning. Capture it with low confidence + speculative
  status, and consider `promote_to_memory: false` if it's pure
  exploration.
- Open questions. Same — they belong in the session as Session-only
  segments so a future agent knows the thread is unresolved.
- Dead ends. "We tried X, it didn't work because Y" is genuinely
  useful when someone proposes X again. Mark `epistemic_status:
  refuted` and consider promoting (so semantic search surfaces it).
- Joint conclusions even if you (the LLM) participated in producing
  them — those are knowledge the conversation produced, not
  generated analysis after the fact.

## Call to Action

After reviewing the conversation and the session state, submit your
extracted segments via `gramaton_session_commit`. Each segment must:

- Have `content` rich enough to be self-contained (rationale included).
- Have `summary_short` ≤ 1000 chars (target ~750) that captures the
  semantic essence — this is what gets embedded.
- Have `keywords` that a future agent would search for.
- Have `temporality`, `confidence`, `knowledge_type`, and
  `epistemic_status` chosen deliberately, not defaulted.
- Have `promote_to_memory` set explicitly when the segment is
  exploration / open-question / dead-end (Session-only); omit it
  otherwise (defaults to promote).
