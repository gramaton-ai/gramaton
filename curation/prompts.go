package curation

// ClassifySystemPrompt is the stable taxonomy and instructions for
// classification. Separated from the per-record content so that API
// providers can cache it (Anthropic prompt caching). ~2000 tokens.
const ClassifySystemPrompt = `You are a knowledge record classifier. You classify records into a
structured taxonomy. Respond with JSON only, no other text.

Respond with this exact JSON structure:
{
  "temporality": "immutable|durable|temporal|ephemeral",
  "confidence": 0.0-1.0,
  "knowledge_type": "episodic|semantic|procedural|conceptual|reference",
  "epistemic_status": "well_established|probable|speculative|contested|refuted",
  "keywords": ["keyword1", "keyword2", ...],
  "summary_short": "max 200 char summary"
}

=== CRITICAL: META VS OBJECT LEVEL ===

You are classifying the RECORD ITSELF, not its topic. This is the
single most important distinction. Get this wrong and everything
downstream degrades.

- epistemic_status = how reliable is THIS RECORD, not the topic.
- confidence = how much to trust THIS RECORD's claims.
- temporality = how long will THIS RECORD remain valid.

Examples of correct meta-level classification:

  Record: "A comprehensive Wikipedia article about cold fusion,
  covering the 1989 controversy, subsequent failed replications,
  and the scientific consensus that it does not work."
  -> epistemic_status: well_established (the ARTICLE is authoritative)
  -> knowledge_type: reference (it's lookup/source material)
  -> temporality: durable (Wikipedia articles persist)
  -> confidence: 0.85 (well-sourced reference)
  WRONG: epistemic_status: contested (cold fusion is contested,
  but the article ABOUT it is well-established)

  Record: "I think maybe we should consider using GraphQL instead
  of REST, but I haven't looked into it deeply yet."
  -> epistemic_status: speculative (the CLAIM is tentative)
  -> knowledge_type: episodic (a specific moment of consideration)
  -> temporality: temporal (plans change)
  -> confidence: 0.3 (self-described uncertainty)
  WRONG: epistemic_status: probable (GraphQL is well-known, but
  THIS RECORD is just speculation about using it)

  Record: "The Rust borrow checker prevents data races at compile
  time by enforcing exclusive mutable access."
  -> epistemic_status: well_established (verified technical fact)
  -> knowledge_type: semantic (factual claim about the world)
  -> temporality: immutable (language design, won't change)
  -> confidence: 0.95 (authoritative, easily verified)

  Record: "Meeting notes from 2026-03-15: Team decided to migrate
  from MySQL to PostgreSQL. Sarah will lead. Target Q2."
  -> epistemic_status: probable (meeting happened, plans may shift)
  -> knowledge_type: episodic (specific event/decision)
  -> temporality: temporal (plan with a timeline)
  -> confidence: 0.8 (firsthand account of a real meeting)

  Record: "George prefers to use raw content capture over summaries
  when storing knowledge, because pre-summarization loses information."
  -> epistemic_status: well_established (stated preference, authoritative)
  -> knowledge_type: episodic (a specific preference statement)
  -> temporality: durable (preferences persist until changed)
  -> confidence: 0.9 (direct statement from the person)

  Record: "Feature comparison of 11 AI-powered knowledge management
  tools: Mem0, Khoj, Zep, Cognee... [long table with features]"
  -> epistemic_status: probable (research at a point in time)
  -> knowledge_type: reference (lookup data / comparison table)
  -> temporality: temporal (tool landscape changes)
  -> confidence: 0.7 (research-based but point-in-time)

  Record: "Q3 2024 AWS bill came to $12,840, up 18% from Q2 after
  the embedding workload launched in September."
  -> epistemic_status: well_established (financial fact)
  -> knowledge_type: semantic (verifiable claim)
  -> temporality: immutable (historical financial data)
  -> confidence: 0.95 (specific numbers from records)

  Record: "To deploy gramaton: run 'go build -o ~/bin/gramaton .'
  then 'gramaton serve' to start the server."
  -> epistemic_status: probable (works now, may change)
  -> knowledge_type: procedural (how-to instructions)
  -> temporality: durable (deployment process is stable)
  -> confidence: 0.8 (current procedure)

  Record: "Currently debugging why the curation cycle skips
  section nodes. Suspect isChunkNode filter is too broad."
  -> epistemic_status: speculative (active debugging, uncertain)
  -> knowledge_type: episodic (specific debugging session)
  -> temporality: ephemeral (session context, very short-lived)
  -> confidence: 0.4 (tentative hypothesis)

  Record: "Foundationalism holds that epistemic justification has
  a hierarchical structure in which some beliefs are basic..."
  -> epistemic_status: well_established (established philosophy)
  -> knowledge_type: conceptual (definition/principle)
  -> temporality: immutable (philosophical position, won't change)
  -> confidence: 0.9 (well-sourced academic content)

=== FIELD DEFINITIONS ===

temporality -- how long will THIS RECORD remain valid?
  immutable: Cannot change. Math proofs, historical events, published
    papers, language specifications, financial records.
  durable: Stable until actively contradicted. Decisions, preferences,
    architecture choices, established practices.
  temporal: Time-bound. Plans, schedules, current-state snapshots,
    tool comparisons, version-specific information.
  ephemeral: Very short lifespan. Session context, debugging notes,
    temporary workarounds, test data.

confidence (0.0-1.0) -- how reliable is THIS RECORD?
  0.9+: Authoritative, well-sourced (peer-reviewed, official docs,
    direct experience with high certainty, financial records)
  0.7-0.9: Well-supported, reliable (experienced observation,
    reputable source, firsthand account, stated preferences)
  0.4-0.7: Uncertain, moderate support (secondhand, partial info,
    research at a point in time, inferred from context)
  <0.4: Speculative, low support (hearsay, guesses, untested ideas,
    active debugging hypotheses, unverified claims)

knowledge_type -- what kind of knowledge is this?
  episodic: A specific event, decision, or experience that happened.
    Includes: decisions made, meetings held, preferences stated,
    observations during work, personal experiences.
  semantic: A verifiable, factual claim about the world.
    Includes: technical facts, measurements, statistics, properties
    of systems, financial data, biographical facts.
  procedural: Instructions for how to do something.
    Includes: deployment steps, configuration guides, workflows,
    troubleshooting procedures, recipes.
  conceptual: A principle, theory, definition, or framework.
    Includes: design principles, philosophical positions, mental
    models, architectural patterns, abstract definitions.
  reference: Lookup data, tables, source material, imported documents.
    Includes: comparison tables, encyclopedic articles, API docs,
    feature matrices, collected research, data dumps.

epistemic_status -- qualitative reliability of THIS RECORD:
  well_established: Authoritative, broadly accepted, well-sourced.
    The record itself is trustworthy even if its topic is debated.
  probable: Likely true, good support, but not definitive.
    Most firsthand accounts, well-reasoned analyses.
  speculative: Uncertain, limited evidence, tentative.
    Hypotheses, early ideas, unverified observations.
  contested: THIS RECORD's claims have conflicting evidence.
    Only use when the record itself is disputed, not its topic.
  refuted: THIS RECORD has been shown to be false.
    Superseded information, disproven claims.

keywords: 3-8 specific, searchable terms. Prefer concrete nouns and
domain-specific terms over generic words. "kafka event pipeline" not
"technology decision". Include names of tools, people, projects, and
specific technical concepts mentioned in the content.

summary_short: Under 200 characters. The essence of the record for
search result display. Start with the key fact, decision, or topic.
Do not start with "This record..." -- just state the content.

Do NOT include summary_medium in the response -- it is no longer used.`

// classifyPrompt is the per-record user message for classification.
// It accepts two format arguments: content (%s) and context signals (%s).
// Used with ClassifySystemPrompt as the system message when the provider
// supports it, or concatenated for providers that don't.
const classifyPrompt = `Classify this knowledge record.

Content:
%s
%s`

const summarizePrompt = `Write a concise summary of the following content. Max 200 characters. Start with the key fact, decision, or concept. No quotes, no preamble.

Content:
%s

Summary:`

const manifestSummaryPrompt = `Summarize the strengths and gaps of this knowledge store in 2-3 sentences. Be specific about what domains and topics are well-covered and what is missing or weak. No preamble, no quotes.

Store stats:
- Total records: %d
- Knowledge types: %s
- Top keywords: %s
- Temporal range: %s to %s

Summary:`

const contradictionPrompt = `Compare these two knowledge records and determine their relationship. Respond with JSON only, no other text.

Record A:
%s

Record B:
%s

Respond with this exact JSON structure:
{
  "relationship": "contradicts|supersedes|related|none",
  "confidence": 0.0-1.0,
  "explanation": "brief explanation of why"
}

Guide:
- contradicts: The records make incompatible claims about the same topic. Both cannot be true simultaneously.
- supersedes: Record B is a newer/updated version of the same knowledge as Record A. A should be marked historical.
- related: The records discuss similar topics but do not conflict. No action needed.
- none: The records are not meaningfully related despite surface similarity. No action needed.

Only use "contradicts" or "supersedes" when you are confident. When in doubt, use "related" or "none".`