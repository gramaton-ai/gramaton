package curation

const classifyPrompt = `Classify this knowledge record. Respond with JSON only, no other text.

Content:
%s

Respond with this exact JSON structure:
{
  "temporality": "immutable|durable|temporal|ephemeral",
  "confidence": 0.0-1.0,
  "knowledge_type": "episodic|semantic|procedural|conceptual|reference",
  "epistemic_status": "well_established|probable|speculative|contested|refuted",
  "keywords": ["keyword1", "keyword2"],
  "summary_short": "max 200 character summary"
}

Classification guide:
- temporality: How long will this remain valid? immutable=forever (definitions), durable=until contradicted, temporal=time-bound, ephemeral=hours
- confidence: How likely is this correct? 0.9+=authoritative, 0.7-0.9=well-supported, 0.4-0.7=uncertain, <0.4=speculative
- knowledge_type: episodic=event, semantic=fact, procedural=how-to, conceptual=principle, reference=lookup data
- epistemic_status: well_established=broadly accepted, probable=likely, speculative=uncertain, contested=disagreement, refuted=false
- keywords: 3-8 specific, searchable terms. Prefer concrete nouns over abstract words.
- summary_short: Capture the essence in under 200 characters. Start with the key fact or decision.`

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
