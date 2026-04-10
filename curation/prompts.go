package curation

// classifyPrompt is the LLM prompt for record classification.
// It accepts two format arguments: content (%%s) and context signals (%%s).
//
// IMPORTANT: meta/object-level distinction. The prompt must distinguish
// the RECORD's reliability from the TOPIC's philosophical status. An
// article about contested topics is itself a well-established reference.
const classifyPrompt = `Classify this knowledge record. Respond with JSON only, no other text.

Content:
%s
%s
Respond with this exact JSON structure:
{
  "temporality": "immutable|durable|temporal|ephemeral",
  "confidence": 0.0-1.0,
  "knowledge_type": "episodic|semantic|procedural|conceptual|reference",
  "epistemic_status": "well_established|probable|speculative|contested|refuted",
  "keywords": ["keyword1", "keyword2", ...],
  "summary_short": "max 200 char summary",
  "summary_medium": "max 1500 char abstract (only for content longer than 500 chars)"
}

CRITICAL DISTINCTION: You are classifying the RECORD, not the TOPIC.
- A Wikipedia article about a contested scientific theory is itself a well_established reference.
- A personal speculation about a well-established fact is speculative.
- epistemic_status describes how reliable THIS RECORD is, not the topic's status.
- confidence describes how much to trust THIS RECORD's claims.

temporality -- how long will THIS RECORD remain valid?
  immutable: Cannot change. Math proofs, historical events, published papers.
    Example: "The Pythagorean theorem states a^2 + b^2 = c^2" -> immutable
  durable: Stable until actively contradicted. Decisions, preferences, established practices.
    Example: "We decided to use PostgreSQL for the main database" -> durable
  temporal: Time-bound. Plans, schedules, current-state snapshots.
    Example: "Sprint 23 ends on Friday" -> temporal
  ephemeral: Very short lifespan. Session context, test data, temporary notes.
    Example: "Currently debugging the auth endpoint" -> ephemeral

confidence (0.0-1.0) -- how reliable is THIS RECORD?
  0.9+: Authoritative, well-sourced (peer-reviewed, official docs)
  0.7-0.9: Well-supported, reliable (experienced observation, reputable source)
  0.4-0.7: Uncertain, moderate support (secondhand, partial information)
  <0.4: Speculative, low support (hearsay, guesses, untested ideas)

knowledge_type -- what kind of knowledge is this?
  episodic: A specific event or decision that happened.
    Example: "We chose Kafka over RabbitMQ for the event pipeline"
  semantic: A verifiable, factual claim about the world.
    Example: "mxbai-embed-large has a 512-token context window"
  procedural: Instructions for how to do something.
    Example: "To deploy, run make build then kubectl apply -f deploy/"
  conceptual: A principle, theory, or definition.
    Example: "Immutability enables safe concurrent access without locks"
  reference: Lookup data, tables, source material, imported documents.
    Example: An encyclopedia article, a configuration reference, a data table

epistemic_status -- qualitative reliability of THIS RECORD:
  well_established: Authoritative, broadly accepted, well-sourced.
  probable: Likely true, good support, but not definitive.
  speculative: Uncertain, limited evidence, tentative.
  contested: THIS RECORD's claims have conflicting evidence.
  refuted: THIS RECORD has been shown to be false.

keywords: 3-8 specific, searchable terms. Prefer concrete nouns and
domain-specific terms over generic words. "kafka event pipeline" not
"technology decision".

summary_short: Under 200 characters. The essence of the record for
search result display. Start with the key fact, decision, or topic.

summary_medium: For content longer than 500 characters, write a
~1500 character abstract covering the major themes. For short content,
omit this field.`

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

const conceptSynthesisPrompt = `Synthesize the following related knowledge records into a concept summary. This concept node will serve as a retrieval hub connecting related knowledge.

Keyword: %s
Number of records: %d

Record summaries:
%s

Write a concise synthesis (2-4 sentences) that captures the essence of what these records collectively establish about this concept. Focus on what they have in common, key themes, and the most important insights. No preamble, no quotes.

Synthesis:`
