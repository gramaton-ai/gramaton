package config

// commentRegistry maps yaml-path -> HeadComment text used by the
// Save() renderer (see render.go) to attach human-readable comments
// to the rendered config.yaml.
//
// Scope: this registry covers the llm: block in detail. The rest of
// the file ships without comments today; future passes can extend
// the registry. Coverage policy: every key under llm: should have an
// entry; keys outside llm: are optional.
//
// Authoring conventions:
//   - Section banners use a 60-char `=` ruler to set off a logical
//     block visually in editor.
//   - Per-field comments lead with the field name, a colon, then a
//     terse description. Multi-line wraps are fine; yaml.v3 emits
//     each line as `# ...`.
//   - Reference URLs go in the section banner so they're easy to
//     spot without scrolling.
var commentRegistry = map[string]string{

	// =============================================================
	// LLM block: umbrella banner with model-staleness pointer
	// =============================================================
	"llm": `=============================================================
LLM CONFIGURATION
=============================================================
Everything that controls how Gramaton calls a Large Language
Model lives in this block.

Model names age. If a curation cycle starts failing with
"model not found" or 404s, the name probably deprecated.
Find current names at:
  anthropic: https://docs.anthropic.com/en/docs/about-claude/models
  openai:    https://platform.openai.com/docs/models
  bedrock:   https://docs.aws.amazon.com/bedrock/latest/userguide/models-supported.html`,

	// -------------------------------------------------------------
	// Provider + auth
	// -------------------------------------------------------------
	"llm.provider": `provider: which LLM service to call.
  anthropic | openai | bedrock - first-party APIs (recommended).
  claude-cli | kiro-cli - UNSUPPORTED. These shell out to the
    vendor's interactive CLI, outside its intended use; may
    violate the vendor's terms of service and could result in
    account suspension. Retained for local experimentation only.`,

	"llm.api_key_file": `api_key_file: path to a file holding the API key (one line).
  Preferred over inline keys -- they leak into backups and
  process listings.`,

	"llm.api_key_env": `api_key_env: env var name to read the key from. Used if
  api_key_file is empty.`,

	"llm.api_key": `api_key: literal API key value. Avoid -- use api_key_file
  or api_key_env. Inline keys end up in backups + process
  listings.`,

	"llm.base_url": `base_url: override provider endpoint (OpenAI-compatible
  servers, local proxies). Empty = provider default.`,

	"llm.region": `region: AWS region for Bedrock model endpoint.`,

	"llm.aws_profile": `aws_profile: named AWS profile for Bedrock credentials.
  Empty = default credential chain.`,

	"llm.aws_access_key_id_env": `aws_access_key_id_env: env var holding the AWS access
  key ID for Bedrock.`,

	"llm.aws_secret_access_key_env": `aws_secret_access_key_env: env var holding the AWS
  secret access key for Bedrock.`,

	// =============================================================
	// RERANK
	// =============================================================
	"llm.rerank": `=============================================================
RERANK -- search-time LLM reranking of retrieval candidates.
=============================================================
Adds ~1 LLM call per search. Off by default. Quality lift is
real on conceptual queries but the latency cost is meaningful
for interactive search. Flip on if you have an LLM provider
configured and conceptual queries dominate your workload.`,

	"llm.rerank.enabled": `enabled: send retrieval candidates back through the LLM for
  relevance reordering.`,

	"llm.rerank.candidates": `candidates: number of initial results fed to the reranker.
  More = better quality, more cost + latency.`,

	// =============================================================
	// MODELS (tiers + per-task assignments)
	// =============================================================
	"llm.models": `=============================================================
MODELS -- three tiers + which tier each task uses.
=============================================================
All model-selection lives here. Each task picks a tier; each
tier maps to a concrete model. Lower the tier to save cost;
raise it for quality.`,

	"llm.models.low": `low: cheap, fast tier. Use for high-volume / easy tasks.`,

	"llm.models.medium": `medium: balanced tier. Use for tasks that need real reasoning.`,

	"llm.models.high": `high: best-quality tier. Use sparingly -- premium pricing.`,

	"llm.models.tasks": `-------------------------------------------------------------
tasks: which tier each LLM call site uses. Unset entries fall
  back to the baked-in default for that task.
-------------------------------------------------------------`,

	"llm.models.tasks.classification_short": `classification_short: classify records below long_threshold.
  Sets temporality, knowledge_type, etc. High volume, easy.`,

	"llm.models.tasks.classification_long": `classification_long: classify records >= long_threshold chars.
  Same task, longer prompt; harder reasoning over more text.`,

	"llm.models.tasks.summarization": `summarization: generate content_short (the embedded semantic
  anchor) for each new record. High volume, easy.`,

	"llm.models.tasks.contradiction": `contradiction: detect supersession + conflicts between similar
  records. Subtle reasoning; matters for store correctness.`,

	"llm.models.tasks.concept": `concept: synthesize concept nodes from clusters of related
  records. Generative + structural reasoning.`,

	"llm.models.tasks.manifest": `manifest: periodic rollup of store state. Cheap, mostly cached.`,

	"llm.models.tasks.rerank": `rerank: reorder search candidates by relevance to the query
  (active only when rerank.enabled is true above).`,

	"llm.models.tasks.decompose": `decompose: split complex search queries into sub-queries
  before retrieval. Off path for simple queries.`,

	// =============================================================
	// COST LIMITS
	// =============================================================
	"llm.cost_limits": `=============================================================
COST LIMITS -- caps that apply to ALL LLM calls.
=============================================================
0 disables. Use these to keep a runaway loop from emptying
your wallet.`,

	"llm.cost_limits.max_calls_per_day": `max_calls_per_day: total LLM calls per calendar day across
  ALL providers and tasks. Counter resets at UTC midnight.
  Default 500 (set by 'gramaton init') is conservative for
  new users; production curation against a meaningful
  backlog will hit this -- bump to 5000-10000+ or 0 to
  disable. When hit, all calls fail with ErrCapped until
  midnight UTC; curation logs a warning and skips the LLM
  phases. Backstop for cost caps when the model isn't in
  the pricing table.`,

	"llm.cost_limits.max_calls_per_session": `max_calls_per_session: total LLM calls per server session
  (between starts). Resets when the server restarts.
  Default 0 = disabled. Use when you want a single-session
  ceiling regardless of calendar day.`,

	"llm.cost_limits.max_cost_usd_per_day": `max_cost_usd_per_day: estimated USD per day, computed from
  the per-model pricing table (llm/pricing.go). Counter
  resets at UTC midnight. Default 5 (set by 'gramaton init')
  covers small-scale use; production runs need higher.
  Bump to fit your cost envelope or 0 to disable. When hit:
  same behavior as max_calls_per_day -- ErrCapped on all
  calls until midnight UTC. Use this when you care about
  absolute cost; max_calls_per_day is the fallback when a
  model isn't in the pricing table (CLI providers, custom
  endpoints).`,

	"llm.cost_limits.max_cost_usd_per_run": `max_cost_usd_per_run: estimated USD per curation cycle.
  Cycle-level twin of max_cost_usd_per_day; protects
  against a single runaway cycle without affecting the
  daily envelope. 0 to disable.`,

	"llm.cost_limits.rate_limit_interval": `rate_limit_interval: minimum gap between successive LLM
  calls.`,

	"llm.cost_limits.max_response_tokens": `max_response_tokens: cap on response length the provider
  may generate. 0 = provider default. Honored by the
  Anthropic provider only today.`,

	// =============================================================
	// CURATION KNOBS (with don't-edit warning)
	// =============================================================
	"llm.curation": `=============================================================
CURATION KNOBS -- autonomous cleanup-cycle tuning.
=============================================================
WARNING: these values control algorithmic behavior that has
been carefully tuned. Do not edit unless you have a specific
reason and have read the relevant code path. Wrong values
silently degrade store quality (missed contradictions,
generic concepts, runaway cost).`,

	"llm.curation.batch_size": `batch_size: records classified per LLM call.`,

	"llm.curation.max_calls_per_run": `max_calls_per_run: hard cap on LLM calls in one cycle.
  Runner stops dispatching new calls once reached;
  remaining work waits for the next cycle (default 5-min
  interval). Default 20 limits each cycle to ~20 *
  per-call cost. Production backlogs may want 100+ -- but
  every bump multiplies per-cycle worst-case spend.
  Backstop when cost cap can't compute (unknown model in
  pricing table).`,

	"llm.curation.long_classification_threshold": `long_classification_threshold: char cutoff between short
  and long classification tiers. Records below this route
  to the short-tier prompt.`,

	"llm.curation.task_timeout": `task_timeout: wall-clock cap on a single curation task.
  90s default. 0 disables.`,

	"llm.curation.prompt_caching_enabled": `prompt_caching_enabled: cache invariant system prompts on
  providers that support it (Anthropic). Major cost
  reduction.`,

	"llm.curation.manifest_cache_enabled": `manifest_cache_enabled: skip the manifest LLM call when
  the store fingerprint is unchanged from the previous
  cycle.`,

	"llm.curation.classify_short_prompt_compressed": `classify_short_prompt_compressed: route short-tier
  records through the condensed prompt (~60% smaller).`,

	"llm.curation.contradiction": `-------------------------------------------------------------
contradiction: tuning for the contradiction/supersession
detector.
-------------------------------------------------------------`,

	"llm.curation.contradiction.max_checks": `max_checks: candidate pairs examined per cycle.
  With batch_size > 1, multiple pairs share one call.`,

	"llm.curation.contradiction.min_similarity": `min_similarity: lower bound of the cosine band for
  contradiction candidates. Below = too dissimilar.`,

	"llm.curation.contradiction.max_similarity": `max_similarity: upper bound. Above = near-duplicate
  handled by auto-supersession instead.`,

	"llm.curation.contradiction.batch_size": `batch_size: pairs packed into one LLM call.
  1 = single-pair (legacy). 5-10 = ~Nx call reduction.`,

	"llm.curation.contradiction.check_reverse_edges": `check_reverse_edges: skip pairs that already have a B->A
  edge (not just A->B). Reduces redundant work.`,

	"llm.curation.concept": `-------------------------------------------------------------
concept: tuning for concept synthesis.
-------------------------------------------------------------`,

	"llm.curation.concept.max_per_run": `max_per_run: concept syntheses per cycle.`,

	"llm.curation.concept.synthesis_batch_size": `synthesis_batch_size: concepts packed into one synthesis
  call.`,

	"llm.curation.concept.synthesis_max_input_tokens": `synthesis_max_input_tokens: soft cap on input tokens per
  synthesis batch.`,

	"llm.curation.concept.coherence_min": `coherence_min: minimum mean cosine similarity of cluster
  members. Below this, the concept is skipped.`,

	"llm.curation.retries": `-------------------------------------------------------------
retries: per-record / per-pair retry caps. Without these, a
pathological record (oversized, content-policy refusal,
persistent parse failure) re-enters every cycle forever
and rebills the same input tokens. Default 3 across the
board. 0 disables (legacy infinite-retry behavior).
-------------------------------------------------------------`,

	"llm.curation.retries.max_classify_attempts": `max_classify_attempts: per-record before classification
  marks the record processing_status="stuck".`,

	"llm.curation.retries.max_summary_attempts": `max_summary_attempts: per-record before summarization
  gives up. Independent of classify (a record can
  classify cleanly but produce an unusable summary).`,

	"llm.curation.retries.max_synthesis_attempts": `max_synthesis_attempts: per-concept before flipping
  synthesis_status to "stuck".`,

	"llm.curation.retries.max_manifest_attempts": `max_manifest_attempts: consecutive failures on the SAME
  store-state fingerprint before the manifest call is
  skipped until the hash changes.`,

	"llm.curation.retries.max_contradiction_attempts": `max_contradiction_attempts: per-pair before the pair is
  permanently excluded.`,

	"llm.curation.retries.max_embed_attempts": `max_embed_attempts: per-record gramaton_reembed retries
  before the record is excluded from the candidate set.`,
}
