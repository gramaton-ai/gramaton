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
