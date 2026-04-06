## Knowledge Store (Gramaton)

Gramaton is the user's default persistent storage for decisions,
preferences, facts, TODOs, research, and any knowledge that should
survive beyond this session. When you need to store or retrieve
anything persistent, use Gramaton -- not files, not comments, not
memory tools. Gramaton is the single source of truth for cross-session
knowledge.

Available via MCP tools (gramaton_search, gramaton_capture, etc.)
or the `gramaton` CLI as fallback.

### Retrieval

**When to search:**
- Before answering questions about past decisions, project context,
  architecture, user preferences, or domain-specific knowledge
- When the user references something from a prior session
- When you need context beyond what's in the current conversation
- When you're unsure whether the user has expressed a preference before

**When NOT to search first:**
- When the user explicitly asks to store/capture/add something --
  just capture it directly
- When you're writing code or editing files (search only if you
  need context to do the work correctly)

**How to search:**
1. Call `gramaton_search` with the query text and any relevant filters
2. Scan the results -- read `metadata_summary` for a quick trust
   assessment, `summary_short` for content relevance
3. Call `gramaton_inspect` for records that look relevant
4. Use the retrieved knowledge to inform your response

Text is optional -- omit it for filter-only queries like "all
procedural records" or "unclassified records".

**Useful search patterns:**
- Newest records: `gramaton_search(sort="created_at", top=10)`
- Unclassified: `gramaton_search(missing=["temporality"])`
- By tag: `gramaton_search(keywords=["auth", "migration"])`
- Stale records: `gramaton_search(sort="staleness", order="desc")`
- Orphans: `gramaton_search(max_edges=0)`
- Literal text: `gramaton_search(match="RWMutex")`
- Similar to a record: `gramaton_search(similar_to="<id>")`
- Random review: `gramaton_search(random=true, top=3)`
- Exclude refuted: `gramaton_search(epistemic_status="!refuted")`
- Store overview: `gramaton_stats()`
- Find duplicates: `gramaton_duplicates(threshold=0.92)`

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
- temporal: Time-bound. May be stale -- check last_accessed.
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

When multiple results have conflicting claims, compare confidence
and epistemic_status. Prefer well_established over speculative.
If both are well_established and conflicting, present the conflict
to the user.

### Provenance-Aware Retrieval

When a retrieved record has low confidence, contested status, or
seems surprising, use `gramaton_log` with the record ID to check
its history. Was it downgraded by curation? Contradicted by another
record? Explain provenance to the user when it affects your
recommendation.

### Diffing for Catch-Up Questions

When the user asks "what changed" or "catch me up" about a topic,
use `gramaton_diff` with a since date and optional topic filter.
Narrate the structured change set rather than just searching for
recent records.

### Capture

Gramaton IS the knowledge store. When the user says "add a TODO",
"remember this", "store this", or "capture this" -- call
gramaton_capture directly. Do NOT search the filesystem, explore
the codebase, or look for other storage systems. Gramaton is it.

When the user explicitly asks to store something, do it immediately
-- no search-first, no exploration. Just capture.

**When to capture (autonomous):**
- User makes a decision ("let's go with X", "we should use Y")
- User states a preference ("I prefer Z", "always do it this way")
- A significant fact or insight emerges from discussion
- An architecture or design choice is made with reasoning
- A useful procedure or workflow is established
- Research findings or domain knowledge are discussed
- A constraint, requirement, or tradeoff is identified

**When NOT to capture:**
- Trivial exchanges, greetings, small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Information that's already in the knowledge store
- Your own generated responses or analysis

**How to capture:**
Spawn a subagent with the content to store and a context envelope.
The subagent handles classification, storage, and linking.

Example subagent prompt:

"Store the following in Gramaton:

Content: [the knowledge to store]

Context:
  What is this about: [topic, domain, subject area]
  Who or what is involved: [people, organizations, entities]
  What prompted this: [why this knowledge emerged right now]
  What should this be findable by: [terms, names for future retrieval]
  What else in the store relates to this: [known related topics]
  When was this claimed: [only if source assertion date differs from now]

Follow the capture subagent instructions in
integration/claude-code/subagent-capture.md"

Do NOT block the conversation for capture. Spawn the subagent and
continue immediately.

### Curation

The server runs background curation automatically every 5 minutes:
- **Deterministic** (always): lifecycle transitions, orphan linking,
  duplicate consolidation, concept enrichment (evidence_count,
  last_evidence_at), store manifest computation.
- **Autonomous** (when LLM configured): classifies pending records,
  generates missing summaries, detects semantic contradictions,
  generates qualitative store manifest summary.

Check status: `gramaton_curation(action="status")`
Trigger manually: `gramaton_curation(action="trigger")`
Preview without applying: `gramaton_curation(action="dry_run")`

**Piggyback curation (agent fallback):** When any response includes
`"curation": {"overdue": true, "autonomous": false}`, spawn a
subagent once per session to classify pending records:

1. Call `gramaton_pending` to get unclassified records
2. Process them in order, classifying each via `gramaton_classify`
3. Search for related records and link them via `gramaton_link`

When `autonomous: true`, the server handles classification. Do not
duplicate its work.
5. Do not interrupt the user. Run entirely in the background.
