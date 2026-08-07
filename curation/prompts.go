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
  "summary_short": "~750 char summary (semantic anchor for embedding)"
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

  Record: "To deploy gramaton: run 'go build -o ./gramaton .'
  then './gramaton serve' to start the server."
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

summary_short: Up to ~750 characters (hard cap 1000). This is the
embedding-ready semantic anchor of the record -- it is what gets
vector-embedded for similarity search. Make it semantically
representative, not a tagline. Start with the key fact, decision,
or topic. Do not start with "This record..." -- just state the
content.`

// ClassifySystemPromptShort is a condensed variant of
// ClassifySystemPrompt for records that fall below
// LongClassificationThreshold. Keeps the JSON schema, the meta-vs-object
// caveat, enum definitions, and keyword/summary guidance; drops the 9
// worked examples and the per-enum bullet lists of example inclusions.
// Used when the short-tier (default: Haiku) handles the record; short
// content rarely hits the borderline cases that the examples were added
// to disambiguate. About 60% smaller than the full prompt.
const ClassifySystemPromptShort = `You are a knowledge record classifier. Respond with JSON only, no other text.

Respond with this exact JSON structure:
{
  "temporality": "immutable|durable|temporal|ephemeral",
  "confidence": 0.0-1.0,
  "knowledge_type": "episodic|semantic|procedural|conceptual|reference",
  "epistemic_status": "well_established|probable|speculative|contested|refuted",
  "keywords": ["keyword1", "keyword2", ...],
  "summary_short": "~750 char summary (semantic anchor for embedding)"
}

Classify the RECORD ITSELF, not its topic. epistemic_status, confidence, and temporality describe how reliable, how certain, and how long THIS RECORD remains valid -- not those properties of the record's subject. Example: an authoritative article about a contested topic is well_established (authoritative article), not contested (topic is contested).

temporality -- how long will THIS RECORD remain valid?
  immutable: cannot change (proofs, historical events, specs, financial records)
  durable: stable until contradicted (decisions, preferences, architecture, practices)
  temporal: time-bound (plans, schedules, current-state, comparisons, version-specific info)
  ephemeral: very short-lived (session context, debugging notes, temporary workarounds)

confidence (0.0-1.0) -- how reliable is THIS RECORD?
  0.9+: authoritative, well-sourced
  0.7-0.9: reliable, well-supported
  0.4-0.7: uncertain, moderate support
  <0.4: speculative, low support

knowledge_type -- what kind of knowledge?
  episodic: specific event/decision/experience that happened
  semantic: verifiable factual claim about the world
  procedural: how-to instructions, workflows
  conceptual: principle, theory, definition, framework
  reference: lookup data, source material, imported documents

epistemic_status -- qualitative reliability of THIS RECORD:
  well_established: authoritative, broadly accepted, well-sourced
  probable: likely true, good support, not definitive
  speculative: uncertain, limited evidence, tentative
  contested: THIS RECORD's claims have conflicting evidence (not its topic)
  refuted: THIS RECORD has been shown to be false

keywords: 3-8 specific, searchable terms. Concrete nouns and domain-specific terms (e.g., "kafka event pipeline" not "technology decision"). Include names of tools, people, projects, and technical concepts.

summary_short: up to ~750 characters. The embedding-ready semantic anchor of the record. Start with the key fact/decision/topic. Don't start with "This record..." -- just state the content.`

// classifyPrompt is the per-record user message for classification.
// It accepts two format arguments: content (%s) and context signals (%s).
// Used with ClassifySystemPrompt as the system message when the provider
// supports it, or concatenated for providers that don't.
const classifyPrompt = `Classify this knowledge record.

Content:
%s
%s`

// SummarizeSystemPrompt is the stable invariant portion of the
// summarization prompt. Marked for Anthropic prompt caching so the
// same instructions are reused across the batch within the cache TTL.
const SummarizeSystemPrompt = `Write concise summaries of knowledge content. Each summary is ~750 characters (semantic anchor for embedding). Start with the key fact, decision, or concept. No quotes, no preamble.`

// summarizePrompt is the per-record user message. Variable content only.
// When the provider does not support SystemPromptSetter, callers
// concatenate SummarizeSystemPrompt in front of this template.
const summarizePrompt = `Content:
%s

Summary:`

// ManifestSystemPrompt is the stable instructions for the store manifest
// rollup. Invariant across cycles; cached by providers that support it.
const ManifestSystemPrompt = `Summarize the strengths and gaps of a knowledge store in 2-3 sentences. Be specific about what domains and topics are well-covered and what is missing or weak. Describe shape qualitatively; do NOT include record counts, percentages, or any numeric values in the summary -- caller surfaces numeric stats separately. No preamble, no quotes.`

// manifestSummaryPrompt is the per-cycle user message. Contains only the
// variable store statistics.
const manifestSummaryPrompt = `Store stats:
- Total records: %d
- Knowledge types: %s
- Top keywords: %s
- Temporal range: %s to %s

Summary:`

// ContradictionSystemPrompt is the stable relationship-analysis
// instructions. Used by both single-pair and batched contradiction
// detection. Cached by providers that support it.
const ContradictionSystemPrompt = `You analyze the relationship between two knowledge records. Respond with JSON only, no other text.

Respond with this exact JSON structure:
{
  "relationship": "contradicts|related|none",
  "confidence": 0.0-1.0,
  "explanation": "brief explanation of why"
}

Guide:
- contradicts: The records make incompatible claims about the same topic. Both cannot be true simultaneously.
- related: The records discuss similar topics but do not conflict. This includes one record reading like a newer or expanded version of the other -- classify that as related; never decide one replaces the other.
- none: The records are not meaningfully related despite surface similarity. No action needed.

Only use "contradicts" when you are confident. When in doubt, use "related" or "none".`

// contradictionPrompt is the per-pair user message. Variable content only.
const contradictionPrompt = `Record A:
%s

Record B:
%s`

// ContradictionBatchSystemPrompt is the stable instructions for batched
// contradiction analysis. The batched mode asks the LLM to classify
// N independent pairs in a single call, returning a JSON array with one
// object per pair in the input order. Cached by providers that support it.
const ContradictionBatchSystemPrompt = `You analyze relationships between pairs of knowledge records. You will receive N independent pairs and must classify each.

Respond with a JSON array of objects, one per pair, in the same order as the input. Each object must match:
{
  "pair_id": <integer matching the input pair_id>,
  "relationship": "contradicts|related|none",
  "confidence": 0.0-1.0,
  "explanation": "brief explanation of why"
}

Relationships:
- contradicts: The records make incompatible claims about the same topic. Both cannot be true simultaneously.
- related: The records discuss similar topics but do not conflict. This includes one record reading like a newer or expanded version of the other -- classify that as related; never decide one replaces the other.
- none: The records are not meaningfully related despite surface similarity. No action needed.

Only use "contradicts" when confident. When in doubt, use "related" or "none". Return JSON only, no prose, no code fences.`

// classificationSchema is the JSON Schema passed to CompleteStructured
// for the classification call. Providers that support wire-layer
// schema enforcement (anthropic tool-use, openai response_format
// strict=true, bedrock Converse tool-use for Claude) guarantee the
// response matches this shape before we see it, which eliminates
// the "chatty preamble around JSON" and "tool-use tag leakage"
// failure modes that parseClassification had to defend against.
// Mirrors classificationResult (curation/autonomous.go:1763) — keep
// them in sync.
//
// Schema constraints tuned for OpenAI strict mode compatibility:
//   - `additionalProperties: false` is REQUIRED by strict mode.
//   - `required` MUST list every property.
//   - `minimum`/`maximum` on numerics are REJECTED by strict mode
//     (not in its supported subset). Confidence bound-checking is
//     done post-parse in parseClassification (clamps to 0.5 on
//     out-of-range) instead. Anthropic and Bedrock-via-Claude both
//     accept this more permissive schema without complaint.
var classificationSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"temporality": map[string]any{
			"type": "string",
			"enum": []string{"immutable", "durable", "temporal", "ephemeral"},
		},
		"confidence": map[string]any{
			"type": "number",
		},
		"knowledge_type": map[string]any{
			"type": "string",
			"enum": []string{"episodic", "semantic", "procedural", "conceptual", "reference"},
		},
		"epistemic_status": map[string]any{
			"type": "string",
			"enum": []string{"well_established", "probable", "speculative", "contested", "refuted"},
		},
		"keywords": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
		"summary_short": map[string]any{
			"type": "string",
		},
	},
	"required":             []string{"temporality", "confidence", "knowledge_type", "epistemic_status", "keywords", "summary_short"},
	"additionalProperties": false,
}

// ConceptSynthesisSystemPrompt is the stable instructions for concept
// synthesis. Variable content is one or more concept sections with
// members. Cached by providers that support it.
const ConceptSynthesisSystemPrompt = `Synthesize each concept below from its member record summaries. Respond with a JSON array of objects, one per concept, in order. Each object: {"keyword": "...", "synthesis": "2-4 sentence summary"}

Return JSON only, no prose, no code fences.`
