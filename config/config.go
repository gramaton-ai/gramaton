package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all Gramaton configuration.
//
// Fields are grouped into two tiers:
//
//   - User-facing: operational settings and cost/quality dials. Safe for
//     a normal operator to change.
//   - Internal tuning: algorithmic parameters calibrated for correctness
//     and performance. Do not adjust casually. See the warning header
//     further down in this file.
type Config struct {
	// DataDir is the root directory for all Gramaton on-disk state
	// (graph, indexes, backups). Change this only for a fresh install;
	// moving an existing data_dir requires copying the contents.
	DataDir string `yaml:"data_dir"`

	// --- User-facing configuration ---

	Server      ServerConfig      `yaml:"server"`
	Embedding   EmbeddingConfig   `yaml:"embedding"`
	Logging     LoggingConfig     `yaml:"logging"`
	Backup      BackupConfig      `yaml:"backup"`
	GC          GCConfig          `yaml:"gc"`
	Curation    CurationConfig    `yaml:"curation"`
	LLM         LLMConfig         `yaml:"llm"`
	LLMCuration LLMCurationConfig `yaml:"llm_curation"`
	Observe     ObserveConfig     `yaml:"observe"`
	Limits      LimitsConfig      `yaml:"limits"`
	Search      SearchConfig      `yaml:"search"`

	// --- Internal tuning (do not adjust casually) ---

	Scoring    ScoringConfig    `yaml:"scoring"`
	Decay      DecayConfig      `yaml:"decay"`
	Freshness  FreshnessConfig  `yaml:"freshness"`
	Activation ActivationConfig `yaml:"activation"`
	Chunking   ChunkingConfig   `yaml:"chunking"`
	Concepts   ConceptsConfig   `yaml:"concepts"`
	Dedup      DedupConfig      `yaml:"dedup"`
	Graph      GraphConfig      `yaml:"graph"`
	Storage    StorageConfig    `yaml:"storage"`
	Merge      MergeConfig      `yaml:"merge"`
}

// =============================================================================
// USER-FACING CONFIGURATION
// =============================================================================
//
// Everything in this section is intended to be changed by operators to fit
// their environment, cost envelope, or quality expectations.

// ServerConfig controls the gramaton server process.
type ServerConfig struct {
	// Port is the TCP port for the HTTP API. 0 selects an ephemeral
	// port; the chosen port is written to the data directory so
	// clients can discover it.
	Port int `yaml:"port"`

	// AutoStart causes `gramaton` commands to lazily start a server
	// when one isn't running. Disable for setups that run the server
	// as a managed service (systemd, launchd, supervisor).
	AutoStart bool `yaml:"auto_start"`

	// IdleTimeout is how long the server waits with no client requests
	// before self-shutdown. Long timeouts match async usage patterns
	// where the agent returns hours later.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

// EmbeddingConfig controls how record text is turned into vectors.
// Changing provider or dimension after records exist requires
// re-embedding the whole store (see `gramaton reembed`).
type EmbeddingConfig struct {
	// Provider: "bert" (pure-Go, default), "ollama", "openai", "bedrock".
	Provider string `yaml:"provider"`

	// Endpoint is the base URL for providers that need one (mainly
	// ollama). Ignored for the pure-Go bert provider.
	Endpoint string `yaml:"endpoint"`

	// Model is the embedding model name. Must match the provider's
	// catalog. Default is bge-small-en-v1.5 (384-dim, pure-Go).
	Model string `yaml:"model"`

	// MaxTokens overrides the model's context window (in tokens).
	// Auto-detected for Ollama models. Set manually for OpenAI or
	// Bedrock if the default (512) produces suboptimal chunk sizes.
	MaxTokens int `yaml:"max_tokens,omitempty"`

	// Dimension is the embedding vector dimension. Must match the
	// model's output dimension. The flat vector index file records its
	// dimension; changing this after initial setup requires
	// re-embedding all records.
	Dimension int `yaml:"dimension,omitempty"`

	// BaseURL overrides the provider's default API endpoint
	// (openai-compatible servers).
	BaseURL string `yaml:"base_url,omitempty"`

	// APIKeyFile is the path to a file containing the provider API key.
	APIKeyFile string `yaml:"api_key_file,omitempty"`

	// APIKeyEnv is the name of an env var that holds the provider API key.
	// Checked if APIKeyFile is empty or missing.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`

	// APIKey is the literal API key value. Prefer APIKeyFile or
	// APIKeyEnv -- inline keys end up in backups, version control,
	// and process listings. Provided here as the explicit "direct"
	// path for callers that have a real reason (e.g. tests).
	APIKey string `yaml:"api_key,omitempty"`

	// Bedrock-specific: AWS region for the embedding endpoint.
	Region string `yaml:"region,omitempty"`

	// Bedrock-specific: named AWS profile to use for credentials.
	AWSProfile string `yaml:"aws_profile,omitempty"`

	// Bedrock-specific: env var name holding the access key ID
	// (alternative to aws_profile).
	AWSAccessKeyIDEnv string `yaml:"aws_access_key_id_env,omitempty"`

	// Bedrock-specific: env var name holding the secret access key.
	AWSSecretAccessKeyEnv string `yaml:"aws_secret_access_key_env,omitempty"`
}

// LoggingConfig controls log verbosity and on-disk log rotation.
type LoggingConfig struct {
	// Level: "debug", "info", "warn", or "error".
	Level string `yaml:"level"`

	// MaxSizeMB is the total disk budget for all log files combined.
	// Older rotations are deleted once this is exceeded.
	MaxSizeMB int `yaml:"max_size_mb"`

	// RotateSizeMB triggers a rotation to a new file when the current
	// log reaches this size.
	RotateSizeMB int `yaml:"rotate_size_mb"`
}

// BackupConfig controls automatic data_dir snapshots.
type BackupConfig struct {
	// Enabled toggles the background backup goroutine.
	Enabled bool `yaml:"enabled"`

	// Dir is the destination directory for backups. Empty = default
	// location inside data_dir.
	Dir string `yaml:"dir"`

	// Retain is the number of most-recent backups to keep. Older
	// backups are pruned.
	Retain int `yaml:"retain"`

	// Schedule is the interval between automatic backups.
	Schedule time.Duration `yaml:"schedule"`
}

// GCConfig controls garbage collection of old content blob revisions.
// GC is off by default because it's a destructive operation.
type GCConfig struct {
	// Enabled turns the GC loop on. Safe to leave off for most stores.
	Enabled bool `yaml:"enabled"`

	// DryRun logs candidates without deleting. Recommended first step
	// when turning GC on.
	DryRun bool `yaml:"dry_run"`

	// MinAgeDays is the minimum age (in days) before a content blob
	// revision can be collected. Guards against collecting recent
	// revisions that may still be referenced.
	MinAgeDays int `yaml:"min_age_days"`
}

// CurationConfig controls the background curation loop. Curation handles
// deterministic maintenance (auto-supersession, orphan linking, lifecycle
// expiry, concept emergence) and, when LLM is configured, autonomous
// classification / summarization / contradiction detection / concept
// synthesis / manifest rollup.
type CurationConfig struct {
	// Enabled turns the curation loop on. When false, none of the
	// cost-reduction knobs in LLMCurationConfig have any effect and
	// the store loses free deterministic maintenance too.
	Enabled bool `yaml:"enabled"`

	// Interval is the cadence of the curation tick. Shorter intervals
	// keep the Anthropic prompt cache (5-min TTL) warm across cycles.
	Interval time.Duration `yaml:"interval"`

	// OrphanSimilarityMin is the minimum cosine similarity for an
	// orphan record to be auto-linked to an existing cluster.
	OrphanSimilarityMin float64 `yaml:"orphan_similarity_min"`

	// StaleEphemeralScore is the staleness score above which ephemeral
	// records are expired.
	StaleEphemeralScore float64 `yaml:"stale_ephemeral_score"`

	// StaleTemporalScore is the staleness score above which temporal
	// records are expired. Higher than ephemeral to reflect longer
	// natural lifespan.
	StaleTemporalScore float64 `yaml:"stale_temporal_score"`

	// MaxOrphansPerRun caps how many orphan-linking operations run
	// per cycle. Prevents a massive backlog from blocking a single
	// cycle.
	MaxOrphansPerRun int `yaml:"max_orphans_per_run"`

	// MaxDedupPerRun caps how many duplicate-consolidation operations
	// run per cycle.
	MaxDedupPerRun int `yaml:"max_dedup_per_run"`

	// SectionLinkMin is the minimum similarity for cross-section
	// linking. Section nodes below this similarity are not linked
	// to sibling sections.
	SectionLinkMin float64 `yaml:"section_link_min"`

	// MaxSectionLinksPerRun caps cross-section linking per cycle.
	MaxSectionLinksPerRun int `yaml:"max_section_links_per_run"`

	// ObservationBatchSize is the number of parent records processed
	// per observation extraction cycle. 0 = auto (500 for local
	// providers, 20 for external). Observation is soft-deprecated;
	// see ObserveConfig.Enabled.
	ObservationBatchSize int `yaml:"observation_batch_size"`

	// ObservationMinContentLength is the minimum content_full length
	// (chars) for a record to be eligible for observation extraction.
	// Lower values create observations on short records that are
	// essentially duplicates of their parent. Default 1500.
	ObservationMinContentLength int `yaml:"observation_min_content_length"`
}

// LLMConfig configures the LLM provider and cost/rate caps. Model
// selection for curation tasks flows through Models (effort tiers);
// the top-level Model field is only used by a few non-curation code
// paths (see below).
type LLMConfig struct {
	// Provider: "anthropic", "openai", "bedrock", "claudecli", "kirocli".
	Provider string `yaml:"provider"`

	// Model is the default used by code paths that call the provider's
	// Complete() without specifying a model. Current call sites:
	//   - search/rerank.go          (active when search.rerank_enabled)
	//   - search/decompose.go       (complex query decomposition)
	//   - server/handler_observe.go (active when observe.enabled; soft-deprecated)
	//   - curation/parallel.go      (fallback when a task's effort tier
	//                                resolves to an empty model; should
	//                                never fire with a properly configured
	//                                LLM.Models)
	// Does NOT affect session extraction (done agent-side) or capture-time
	// classification (no server-side LLM call; deferred to curation).
	Model string `yaml:"model"`

	// Models maps effort tiers to concrete model names. Curation tasks
	// declare an effort level (low/medium/high) via LLMCuration.*Effort;
	// this struct turns that into the actual model to pass to the
	// provider. Swap versions or providers here without touching
	// task-level code.
	Models LLMModels `yaml:"models"`

	// APIKeyFile is the path to a file holding the provider API key.
	APIKeyFile string `yaml:"api_key_file,omitempty"`

	// APIKeyEnv is the name of an env var holding the provider API key.
	// Used if APIKeyFile is empty.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`

	// APIKey is the literal API key value. Prefer APIKeyFile or
	// APIKeyEnv -- inline keys end up in backups, version control,
	// and process listings. Provided here as the explicit "direct"
	// path for callers that have a real reason (e.g. tests).
	APIKey string `yaml:"api_key,omitempty"`

	// BaseURL overrides the provider's default endpoint
	// (openai-compatible servers, local proxies).
	BaseURL string `yaml:"base_url,omitempty"`

	// Bedrock-specific: AWS region for the model endpoint.
	Region string `yaml:"region,omitempty"`

	// Bedrock-specific: named AWS profile to use for credentials.
	AWSProfile string `yaml:"aws_profile,omitempty"`

	// Bedrock-specific: env var name for the access key ID.
	AWSAccessKeyIDEnv string `yaml:"aws_access_key_id_env,omitempty"`

	// Bedrock-specific: env var name for the secret access key.
	AWSSecretAccessKeyEnv string `yaml:"aws_secret_access_key_env,omitempty"`

	// MaxCallsPerDay caps total LLM calls per calendar day. 0 = no cap.
	// Safety net against runaway cost. Count-based; acts as backstop
	// when MaxCostUSDPerDay can't compute a cost (unknown model in the
	// pricing table) and cost therefore reads as 0.
	MaxCallsPerDay int `yaml:"max_calls_per_day,omitempty"`

	// MaxCallsPerSession caps LLM calls per server session (between
	// starts). 0 = no cap.
	MaxCallsPerSession int `yaml:"max_calls_per_session,omitempty"`

	// MaxCostUSDPerDay caps total estimated LLM cost per calendar day
	// in USD. 0 = no cap. Cost is estimated via llm.EstimateCost using
	// the pricing table; providers or models missing from the table
	// contribute 0 to this total (MaxCallsPerDay is the backstop for
	// those). Complements MaxCallsPerDay rather than replacing it: a
	// count cap is cheap insurance for any model the pricing table
	// hasn't learned yet.
	MaxCostUSDPerDay float64 `yaml:"max_cost_usd_per_day,omitempty"`

	// RateLimitInterval is the minimum gap between successive LLM
	// calls. 0 = no rate limit.
	RateLimitInterval time.Duration `yaml:"rate_limit_interval,omitempty"`
}

// LLMModels maps effort tiers to model names. Every curation task picks
// its effort level (low/medium/high), and this struct turns that into a
// concrete model to pass to the provider. Keeping model names off the
// task-level code means a provider swap or model revision only edits
// this struct.
type LLMModels struct {
	// Low: cheap/fast tier (default: claude-haiku-4-5).
	Low string `yaml:"low"`

	// Medium: balanced tier (default: claude-sonnet-4-6).
	Medium string `yaml:"medium"`

	// High: best-quality tier (default: claude-opus-4-7).
	High string `yaml:"high"`
}

// LLMCurationConfig controls the autonomous (LLM-driven) part of
// curation: classification, summarization, contradiction detection,
// concept synthesis, manifest rollup. Every field is cost- or
// quality-related and intended for operator tuning.
type LLMCurationConfig struct {
	// BatchSize is the number of records classified per cycle.
	BatchSize int `yaml:"batch_size"`

	// MaxCallsPerRun is the hard cap on total LLM calls in one
	// curation cycle. Protects against a runaway cycle burning budget.
	// Acts as a backstop when MaxCostUSDPerRun can't compute a cost
	// (unknown model in the pricing table) -- keep it set even when
	// cost cap is enabled.
	MaxCallsPerRun int `yaml:"max_calls_per_run"`

	// MaxCostUSDPerRun caps estimated LLM cost per curation cycle in
	// USD. 0 = no cost cap; MaxCallsPerRun still applies. Cost is
	// estimated via llm.EstimateCost from per-task token counts and
	// the pricing table; unknown models contribute 0. A cost cap
	// trips when at least one call has landed for a cycle -- the
	// check is post-call, so the cycle may exceed the cap by one
	// call's worth of cost before breaking.
	MaxCostUSDPerRun float64 `yaml:"max_cost_usd_per_run,omitempty"`

	// MaxContradictionChecks is the maximum number of candidate pairs
	// examined for contradictions per cycle. With
	// ContradictionBatchSize > 1, multiple pairs share one LLM call.
	MaxContradictionChecks int `yaml:"max_contradiction_checks"`

	// ContradictionMinSim is the lower bound of the cosine-similarity
	// band for contradiction candidates. Pairs below this are too
	// dissimilar to meaningfully contradict.
	ContradictionMinSim float64 `yaml:"contradiction_min_similarity"`

	// ContradictionMaxSim is the upper bound. Pairs above this are
	// near-duplicates handled by auto-supersession, not contradiction
	// detection.
	ContradictionMaxSim float64 `yaml:"contradiction_max_similarity"`

	// ContradictionBatchSize is the number of pairs packed into a
	// single LLM call. 1 = single-pair (legacy). 5-10 = batched
	// (roughly N-fold call reduction at saturation).
	ContradictionBatchSize int `yaml:"contradiction_batch_size"`

	// MaxConceptsPerRun caps concept syntheses per cycle.
	MaxConceptsPerRun int `yaml:"max_concepts_per_run"`

	// SynthesisBatchSize is the number of concepts packed into a single
	// synthesis LLM call.
	SynthesisBatchSize int `yaml:"synthesis_batch_size"`

	// SynthesisMaxInputTokens is a soft cap on input tokens per
	// synthesis batch. When exceeded, a new batch starts.
	SynthesisMaxInputTokens int `yaml:"synthesis_max_input_tokens"`

	// ConceptCoherenceMin is the minimum mean cosine similarity of
	// member records to their cluster centroid. Concepts below this
	// are skipped (their members don't coherently represent a single
	// idea). 0 = no filter.
	ConceptCoherenceMin float64 `yaml:"concept_coherence_min"`

	// Cost-reduction toggles. All default to true (activated). Flip
	// to false to revert that individual optimization.

	// PromptCachingEnabled caches invariant system prompts on providers
	// that support it (e.g., Anthropic). Falls back to inline
	// concatenation when off or unsupported.
	PromptCachingEnabled bool `yaml:"prompt_caching_enabled"`

	// ManifestCacheEnabled skips the manifest LLM call when the store's
	// state fingerprint (record counts, top keywords, temporal span)
	// is unchanged from the previous cycle.
	ManifestCacheEnabled bool `yaml:"manifest_cache_enabled"`

	// ContradictionCheckReverseEdges skips candidate pairs that already
	// have a B->A edge (not just A->B). Reduces redundant work on
	// previously-linked pairs.
	ContradictionCheckReverseEdges bool `yaml:"contradiction_check_reverse_edges"`

	// ClassifyShortPromptCompressed routes short-tier records to the
	// condensed ClassifySystemPromptShort (~60% smaller). Long-tier
	// records always use the full prompt.
	ClassifyShortPromptCompressed bool `yaml:"classify_short_prompt_compressed"`

	// Per-task effort assignments. Empty = baked-in default (see
	// defaultEffortForTask). Set to "low", "medium", or "high" to
	// override. Resolves to a concrete model via LLM.Models.

	// ClassificationShortEffort: tier for records below
	// LongClassificationThreshold. Default: low (Haiku).
	ClassificationShortEffort string `yaml:"classification_short_effort"`

	// ClassificationLongEffort: tier for records at or above
	// LongClassificationThreshold. Default: medium (Sonnet).
	ClassificationLongEffort string `yaml:"classification_long_effort"`

	// SummarizationEffort: tier for content_short generation.
	// Default: low (Haiku).
	SummarizationEffort string `yaml:"summarization_effort"`

	// ContradictionEffort: tier for contradiction / supersession
	// detection. Default: medium (Sonnet) because subtle semantic
	// distinctions benefit from calibrated reasoning.
	ContradictionEffort string `yaml:"contradiction_effort"`

	// ConceptEffort: tier for concept synthesis. Default: medium
	// (Sonnet). Bump to high for critical stores where concept
	// abstraction quality matters.
	ConceptEffort string `yaml:"concept_effort"`

	// ManifestEffort: tier for the periodic store manifest rollup.
	// Default: low (Haiku). Low-nuance summarization.
	ManifestEffort string `yaml:"manifest_effort"`

	// LongClassificationThreshold is the character cutoff between
	// short-tier and long-tier classification. Records below this
	// length route to the short tier.
	LongClassificationThreshold int `yaml:"long_classification_threshold"`
}

// EffortLevel names the cost/quality tiers. Each tier maps to a concrete
// model via LLM.Models; tasks declare effort, not model names.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"    // cheap, fast (default: haiku)
	EffortMedium EffortLevel = "medium" // balanced (default: sonnet)
	EffortHigh   EffortLevel = "high"   // best quality (default: opus)
)

// CurationTask names the curation LLM tasks. Each task has a default
// effort level; users override via LLMCurationConfig.*Effort fields.
type CurationTask string

const (
	TaskClassificationShort CurationTask = "classification_short"
	TaskClassificationLong  CurationTask = "classification_long"
	TaskSummarization       CurationTask = "summarization"
	TaskContradiction       CurationTask = "contradiction"
	TaskConcept             CurationTask = "concept"
	TaskManifest            CurationTask = "manifest"
)

// defaultEffortForTask returns the out-of-the-box effort level for each
// curation task. Assignment reflects which tasks benefit from better
// reasoning: summarization and short classification are Haiku-grade;
// long-content classification, contradiction detection, and concept
// synthesis need calibrated reasoning; the manifest rollup is cheap.
func defaultEffortForTask(task CurationTask) EffortLevel {
	switch task {
	case TaskClassificationShort, TaskSummarization, TaskManifest:
		return EffortLow
	case TaskClassificationLong, TaskContradiction, TaskConcept:
		return EffortMedium
	}
	return EffortMedium
}

// EffortForTask resolves a task to its effort level, consulting the
// user's per-task overrides before falling back to the baked-in default.
// Unknown effort strings are treated as empty (falls through to default).
func (c Config) EffortForTask(task CurationTask) EffortLevel {
	var override string
	switch task {
	case TaskClassificationShort:
		override = c.LLMCuration.ClassificationShortEffort
	case TaskClassificationLong:
		override = c.LLMCuration.ClassificationLongEffort
	case TaskSummarization:
		override = c.LLMCuration.SummarizationEffort
	case TaskContradiction:
		override = c.LLMCuration.ContradictionEffort
	case TaskConcept:
		override = c.LLMCuration.ConceptEffort
	case TaskManifest:
		override = c.LLMCuration.ManifestEffort
	}
	switch EffortLevel(override) {
	case EffortLow, EffortMedium, EffortHigh:
		return EffortLevel(override)
	}
	return defaultEffortForTask(task)
}

// ModelAtEffort returns the concrete model name for an effort tier.
// Returns empty string when the tier isn't configured; callers must
// handle that (log + skip, or error) rather than silently calling the
// LLM with an empty model name.
func (c Config) ModelAtEffort(effort EffortLevel) string {
	switch effort {
	case EffortLow:
		return c.LLM.Models.Low
	case EffortMedium:
		return c.LLM.Models.Medium
	case EffortHigh:
		return c.LLM.Models.High
	}
	return ""
}

// ModelForTask is the primary entry point for curation model selection:
// resolves task -> effort -> model name.
func (c Config) ModelForTask(task CurationTask) string {
	return c.ModelAtEffort(c.EffortForTask(task))
}

// ObserveConfig controls the soft-deprecated observe pipeline. Prefer
// the session flow (gramaton_session_prepare / gramaton_session_commit)
// for automatic knowledge capture. Kept here for backwards compatibility;
// set Enabled=false in new setups.
type ObserveConfig struct {
	// Enabled toggles the observe pipeline. Defaults to true for
	// backwards compatibility; recommend false for new setups.
	Enabled bool `yaml:"enabled"`

	// MaxFactsPerCall caps facts extracted per observation LLM call.
	MaxFactsPerCall int `yaml:"max_facts_per_call"`

	// DefaultConfidence is the confidence value assigned to observed
	// facts that don't include one.
	DefaultConfidence float64 `yaml:"default_confidence"`

	// DefaultTemporality is the temporality assigned to observed facts
	// that don't include one. "ephemeral" suits short-lived session
	// observations.
	DefaultTemporality string `yaml:"default_temporality"`

	// SubstanceMinLength is the minimum fact length (chars) to keep.
	// Below this, facts are too trivial to store.
	SubstanceMinLength int `yaml:"substance_min_length"`

	// FeedbackLoopHours is the look-back window for suppressing
	// re-observation of recently observed facts.
	FeedbackLoopHours int `yaml:"feedback_loop_hours"`

	// FeedbackLoopSimilarity is the cosine threshold for "same fact"
	// dedup during the feedback window.
	FeedbackLoopSimilarity float64 `yaml:"feedback_loop_similarity"`

	// RetrievalTracking records which records surfaced in recent
	// retrievals so the observe pipeline can focus on new ground.
	RetrievalTracking bool `yaml:"retrieval_tracking"`

	// RetrievalSimilarity is the cosine threshold for "previously
	// retrieved" membership.
	RetrievalSimilarity float64 `yaml:"retrieval_similarity"`
}

// LimitsConfig holds request-level safety caps. These are not tuning
// knobs in the algorithmic sense, but rather environmental bounds to
// prevent unreasonably large inputs. Operators may raise these for
// unusual workloads (e.g., importing large documents).
type LimitsConfig struct {
	// MaxJSONSize is the largest JSON request body accepted by the
	// HTTP API, in bytes. Guards against memory exhaustion.
	MaxJSONSize int `yaml:"max_json_size"`

	// MaxNestingDepth is the maximum JSON nesting depth. Prevents
	// pathological inputs that would blow the stack.
	MaxNestingDepth int `yaml:"max_nesting_depth"`

	// MaxContentLength caps the size of record content_full, in bytes.
	MaxContentLength int `yaml:"max_content_length"`

	// MaxKeywords caps the number of keywords per record.
	MaxKeywords int `yaml:"max_keywords"`

	// MaxSummaryShort caps the content_short length. Hard cap; soft
	// target is ~750 chars (the embedding-ready semantic anchor size).
	MaxSummaryShort int `yaml:"max_summary_short"`

	// StdinTimeout caps how long CLI commands wait for stdin input.
	StdinTimeout time.Duration `yaml:"stdin_timeout"`

	// MaxWritesPerSecond rate-limits mutating HTTP requests per client.
	MaxWritesPerSecond int `yaml:"max_writes_per_second"`
}

// SearchConfig holds search parameters. The first four fields are
// user-facing cost/quality dials; the remainder are internal scoring
// and index parameters that operators rarely need to adjust.
type SearchConfig struct {
	// --- User-facing dials ---

	// RetrievalCandidates is the number of candidates pulled from
	// vector + BM25 before reranking. More = better recall, slower.
	RetrievalCandidates int `yaml:"retrieval_candidates"`

	// RerankEnabled toggles LLM reranking of retrieval candidates.
	// When true, the top N candidates are passed to the LLM
	// (llm.model) for relevance scoring.
	RerankEnabled bool `yaml:"rerank_enabled"`

	// RerankCandidates is the number of candidates sent to the LLM
	// reranker when RerankEnabled is true. More = better quality,
	// slower and more expensive.
	RerankCandidates int `yaml:"rerank_candidates"`

	// SessionDedupEnabled suppresses Session segments in search results
	// when the Memory record they were extracted into is also in the
	// result set. Reduces duplication across the two stores.
	SessionDedupEnabled bool `yaml:"session_dedup_enabled"`

	// --- Internal scoring / index parameters (rarely adjust) ---

	// BM25K1: term frequency saturation parameter.
	BM25K1 float64 `yaml:"bm25_k1"`

	// BM25B: document-length normalization.
	BM25B float64 `yaml:"bm25_b"`

	// BM25WeightFull: RRF weight for the content_full BM25 lane.
	BM25WeightFull float64 `yaml:"bm25_weight_full"`

	// BM25WeightMedium: RRF weight for the content_medium BM25 lane
	// (retained for legacy stores; content_medium is not written in
	// current pipelines).
	BM25WeightMedium float64 `yaml:"bm25_weight_medium"`

	// BM25WeightShort: RRF weight for the content_short BM25 lane.
	// Weighted higher than full because summaries are more
	// discriminative for retrieval.
	BM25WeightShort float64 `yaml:"bm25_weight_short"`

	// RRFK: Reciprocal Rank Fusion constant. Lower = more weight to
	// top ranks; higher = flatter fusion.
	RRFK int `yaml:"rrf_k"`

	// SuggestionThreshold: top-result score below which "did you mean"
	// suggestions are returned alongside the results.
	SuggestionThreshold float64 `yaml:"suggestion_threshold"`

	// HNSWThreshold: vector count above which HNSW is used instead of
	// flat scan. Smaller stores don't benefit from HNSW overhead.
	HNSWThreshold int `yaml:"hnsw_threshold"`

	// HNSWM: HNSW maximum connections per layer. Higher = better
	// recall, more memory.
	HNSWM int `yaml:"hnsw_m"`

	// HNSWEfConstruction: HNSW build-time quality parameter.
	HNSWEfConstruction int `yaml:"hnsw_ef_construction"`

	// HNSWEfSearch: HNSW query-time search width. Higher = better
	// recall, slower.
	HNSWEfSearch int `yaml:"hnsw_ef_search"`

	// VectorOnlyPenalty: score multiplier for results that matched via
	// vector similarity but have no BM25 match. Penalizes
	// semantic-only matches to favor lexical+semantic hits.
	VectorOnlyPenalty float64 `yaml:"vector_only_penalty"`
}

// =============================================================================
// INTERNAL TUNING PARAMETERS
// =============================================================================
//
// WARNING: Everything below this line controls algorithmic behavior that
// is calibrated for correctness and performance. Most operators should
// NOT adjust these values. Changing them may:
//
//   - Degrade retrieval quality without any visible error
//   - Break index invariants (e.g., embedding dimension mismatch
//     requires a full re-embed)
//   - Cause silent scoring or lifecycle bugs
//   - Create corrupt store state that is hard to recover from
//
// If you think you need to change a value here, first read the doc block
// above the specific struct to understand what it controls, and confirm
// the change against the gramaton source code before running in production.
//
// Retained in config because integration tests and advanced deployments
// occasionally need them, not because they're tuned per deployment.
// =============================================================================

// ScoringConfig controls the weighted sum that ranks search results.
// The four weights are normalized to sum to 1.0 at the default values;
// changing one weight should shift the others or be accompanied by a
// rerun of calibration benchmarks. HistoricalPenalty and importance
// thresholds further modulate scores for historical/important records.
type ScoringConfig struct {
	// WeightSimilarity: cosine similarity to the query. Dominant term.
	WeightSimilarity float64 `yaml:"weight_similarity"`

	// WeightFreshness: decay-modulated recency bonus.
	WeightFreshness float64 `yaml:"weight_freshness"`

	// WeightActivation: graph-activation spreading from query anchors.
	WeightActivation float64 `yaml:"weight_activation"`

	// WeightConfidence: record's declared confidence (0-1).
	WeightConfidence float64 `yaml:"weight_confidence"`

	// ImportanceThreshold: records with importance >= this get floor
	// protection (won't drop below ImportanceFloor * top_score).
	ImportanceThreshold float64 `yaml:"importance_threshold"`

	// ImportanceFloor: fraction of top-result score that "important"
	// records are guaranteed to reach.
	ImportanceFloor float64 `yaml:"importance_floor_ratio"`

	// HistoricalPenalty: score multiplier applied to records that have
	// been superseded or marked historical (valid_until in the past).
	HistoricalPenalty float64 `yaml:"historical_penalty"`
}

// DecayConfig holds per-temporality decay rates used when computing
// freshness. Rates are per-hour half-life coefficients. Changing these
// shifts record expiration cadence and historical-scoring weight.
type DecayConfig struct {
	Rates DecayRates `yaml:"rates"`
}

// DecayRates maps each temporality to a decay coefficient.
type DecayRates struct {
	// Ephemeral: fastest decay. Default tuned to decay to near-zero
	// over hours.
	Ephemeral float64 `yaml:"ephemeral"`

	// Temporal: decays over weeks-to-months.
	Temporal float64 `yaml:"temporal"`

	// Durable: decays over years.
	Durable float64 `yaml:"durable"`

	// Immutable: zero decay. Should always be 0.
	Immutable float64 `yaml:"immutable"`
}

// FreshnessConfig controls how the freshness score is computed from
// a record's age and temporality.
type FreshnessConfig struct {
	// Scale is the time scale (in hours) used by the decay function.
	// Default 8760 = one year.
	Scale float64 `yaml:"scale"`

	// Exponents modulates how quickly freshness drops for each
	// temporality class.
	Exponents FreshnessExponents `yaml:"exponents"`
}

// FreshnessExponents are per-temporality exponents applied in the
// freshness decay formula. Higher = steeper drop.
type FreshnessExponents struct {
	// Immutable: should be 0 (immutable records don't decay).
	Immutable float64 `yaml:"immutable"`

	// Durable: gentle decay (~0.5).
	Durable float64 `yaml:"durable"`

	// Temporal: linear-ish decay (~1.0).
	Temporal float64 `yaml:"temporal"`

	// Ephemeral: linear-ish decay, subject to fast decay rate in
	// DecayConfig.
	Ephemeral float64 `yaml:"ephemeral"`
}

// ActivationConfig controls graph-based spreading activation. When a
// query matches anchor records, activation spreads outward through
// edges, boosting related records' scores.
type ActivationConfig struct {
	// BaseAmount: initial activation delivered to anchor records.
	BaseAmount float64 `yaml:"base_amount"`

	// AttenuationFactor: multiplier applied as activation spreads one
	// hop further. 0.5 = each hop halves the contribution.
	AttenuationFactor float64 `yaml:"attenuation_factor"`
}

// ChunkingConfig controls structural text splitting. NOT used in the
// capture hot path -- observation extraction (D18/D23) handles content
// decomposition in the curation cycle. Retained for internal utilities.
type ChunkingConfig struct {
	// Threshold: content length above which chunking kicks in.
	Threshold int `yaml:"threshold"`

	// ChunkSize: target chunk length in chars.
	ChunkSize int `yaml:"chunk_size"`

	// Overlap: adjacent chunks share this many chars.
	Overlap int `yaml:"overlap"`

	// SectionMin: minimum section size in chars (default 500).
	SectionMin int `yaml:"section_min"`

	// SectionMax: maximum section size in chars (default 5000).
	SectionMax int `yaml:"section_max"`
}

// ConceptsConfig controls concept emergence thresholds. Concepts are
// synthesized from repeated keywords; these knobs decide when a keyword
// cluster is promoted to a concept node.
type ConceptsConfig struct {
	// EmergenceThreshold: minimum number of records sharing a keyword
	// before it becomes a candidate concept.
	EmergenceThreshold int `yaml:"emergence_threshold"`

	// MinContentLengthDirect: minimum content length for a record to
	// count toward the emergence threshold directly (short records
	// contribute less evidence).
	MinContentLengthDirect int `yaml:"min_content_length_direct"`

	// MaxKeywordPct: skip keywords that appear in more than this
	// fraction of records (0-1.0). Ubiquitous keywords don't make
	// meaningful concepts.
	MaxKeywordPct float64 `yaml:"max_keyword_pct"`
}

// DedupConfig controls auto-supersession of near-duplicate records.
// The similarity threshold is carefully calibrated; changing it can
// either miss true duplicates or incorrectly supersede distinct records.
type DedupConfig struct {
	// SimilarityThreshold: cosine similarity above which a new capture
	// supersedes an older record. Default 0.92 is calibrated for
	// bge-small-en-v1.5 embeddings.
	SimilarityThreshold float64 `yaml:"similarity_threshold"`

	// Action: "supersede" (default) marks the older record historical
	// (sets valid_until + resolution=superseded + adds a supersedes
	// edge). "reject" refuses the capture with ErrConflict and rolls
	// back the new node.
	//
	// The previous "flag" value was removed in 2026-04 (see
	// design-decisions.md D37). Load() silently coerces legacy
	// `action: flag` configs to "supersede" for one release cycle --
	// the two values never had distinct behavior.
	Action string `yaml:"action"`
}

// GraphConfig controls graph traversal behavior.
type GraphConfig struct {
	// EdgeWeightTraversalThreshold: minimum edge weight required for
	// traversal during spreading activation and explore queries.
	// Edges weaker than this are treated as absent.
	EdgeWeightTraversalThreshold float64 `yaml:"edge_weight_traversal_threshold"`
}

// StorageConfig controls the on-disk prolly-tree parameters. These
// directly affect the write-amplification / storage-overhead tradeoff
// and should not be changed on existing stores (would require rebuild).
type StorageConfig struct {
	// ProllyTargetChunkSize is the target number of entries per leaf
	// chunk in the prolly tree. Controls the tradeoff between chunk
	// sharing granularity and per-chunk overhead. Smaller values mean
	// finer sharing (less data rewritten per mutation) but more tree
	// nodes and disk I/O on traversal. Larger values mean coarser
	// sharing but fewer nodes. Default 64 works well for stores up
	// to ~100K nodes.
	ProllyTargetChunkSize int `yaml:"prolly_target_chunk_size"`

	// ProllySplitBits is the number of low bits of the FNV-1a hash
	// that must be zero to trigger a chunk boundary. Determines the
	// average chunk size: 2^bits entries. 6 bits = average 64 entries.
	// 5 bits = 32 entries (finer sharing, more overhead). 7 bits = 128
	// entries (coarser sharing, less overhead). Must be between 3 and 10.
	ProllySplitBits int `yaml:"prolly_split_bits"`
}

// MergeConfig controls how conflicts are resolved during branch merges.
type MergeConfig struct {
	// ConflictStrategy: "timestamp_wins" (newer mutation wins;
	// current default) or future alternatives.
	ConflictStrategy string `yaml:"conflict_strategy"`
}

// =============================================================================
// Defaults, Load, Save
// =============================================================================

// Defaults returns a Config with all values set to their documented defaults.
func Defaults() Config {
	return Config{
		DataDir: filepath.Join(defaultHomeDir(), ".gramaton", "data"),

		Server: ServerConfig{
			Port:        0,
			AutoStart:   true,
			IdleTimeout: 4 * time.Hour, // Long idle timeout for async usage patterns (D15)
		},

		Embedding: EmbeddingConfig{
			Provider:  "bert",
			Endpoint:  "http://localhost:11434", // for ollama fallback
			Model:     "bge-small-en-v1.5",
			Dimension: 384,
		},

		Logging: LoggingConfig{
			Level:        "info",
			MaxSizeMB:    512,
			RotateSizeMB: 50,
		},

		Backup: BackupConfig{
			Enabled:  true,
			Retain:   2,
			Schedule: 24 * time.Hour,
		},

		GC: GCConfig{
			Enabled:    false,
			DryRun:     true,
			MinAgeDays: 30,
		},

		Curation: CurationConfig{
			Enabled:               true,
			Interval:              1 * time.Minute,
			OrphanSimilarityMin:   0.6,
			StaleEphemeralScore:   0.95,
			StaleTemporalScore:    0.99,
			MaxOrphansPerRun:      20,
			MaxDedupPerRun:        20,
			SectionLinkMin:        0.75,
			MaxSectionLinksPerRun: 30,
			ObservationBatchSize:        0, // auto: 500 for local providers, 20 for external
			ObservationMinContentLength: 1500,
		},

		LLM: LLMConfig{
			Model: "claude-sonnet-4-6", // used by search reranking, query decomposition, observe (if enabled)
			Models: LLMModels{
				Low:    "claude-haiku-4-5",
				Medium: "claude-sonnet-4-6",
				High:   "claude-opus-4-7",
			},
		},

		LLMCuration: LLMCurationConfig{
			BatchSize:                   10,
			MaxCallsPerRun:              20,
			MaxContradictionChecks:      5,
			ContradictionMinSim:         0.5,
			ContradictionMaxSim:         0.85,
			ContradictionBatchSize:      5,   // batched: 5 pairs per LLM call (~5x call reduction at saturation)
			MaxConceptsPerRun:           5,
			SynthesisBatchSize:          5,
			SynthesisMaxInputTokens:     8000,
			ConceptCoherenceMin:         0.6, // skip concept synthesis when member cluster has mean cosine < 0.6
			LongClassificationThreshold: 2000,

			// All cost-reduction optimizations activated by default.
			PromptCachingEnabled:           true,
			ManifestCacheEnabled:           true,
			ContradictionCheckReverseEdges: true,
			ClassifyShortPromptCompressed:  true,

			// Explicit effort assignments per curation task. Users edit
			// these in config.yaml to retune cost vs quality. Summarization
			// and short classification are Haiku-grade (clear-signal work,
			// enum picks, distilled summaries). Contradiction detection,
			// concept synthesis, and long-content classification benefit
			// from Sonnet-grade reasoning (subtle semantic + temporal
			// distinctions, multi-record abstraction, calibrated
			// confidence/temporality choices). Manifest rollup is
			// infrequent and low-nuance -> Haiku.
			ClassificationShortEffort: string(EffortLow),
			ClassificationLongEffort:  string(EffortMedium),
			SummarizationEffort:       string(EffortLow),
			ContradictionEffort:       string(EffortMedium),
			ConceptEffort:             string(EffortMedium),
			ManifestEffort:            string(EffortLow),
		},

		Observe: ObserveConfig{
			Enabled:                true,
			MaxFactsPerCall:        20,
			DefaultConfidence:      0.3,
			DefaultTemporality:     "ephemeral",
			SubstanceMinLength:     20,
			FeedbackLoopHours:      4,
			FeedbackLoopSimilarity: 0.85,
			RetrievalTracking:      true,
			RetrievalSimilarity:    0.7,
		},

		Limits: LimitsConfig{
			MaxJSONSize:        2 * 1024 * 1024,
			MaxNestingDepth:    10,
			MaxContentLength:   1024 * 1024,
			MaxKeywords:        100,
			MaxSummaryShort:    1000,
			StdinTimeout:       30 * time.Second,
			MaxWritesPerSecond: 100,
		},

		Search: SearchConfig{
			BM25K1:              1.2,
			BM25B:               0.75,
			BM25WeightFull:      1.0,
			BM25WeightMedium:    2.0,
			BM25WeightShort:     3.0,
			RRFK:                60,
			SuggestionThreshold: 0.75,
			HNSWThreshold:       5000,
			HNSWM:               16,
			HNSWEfConstruction:  200,
			HNSWEfSearch:        100,
			VectorOnlyPenalty:   0.1,
			RetrievalCandidates: 200,
			RerankEnabled:       false,
			RerankCandidates:    50,
		},

		// --- Internal tuning defaults ---

		Scoring: ScoringConfig{
			WeightSimilarity:    0.55,
			WeightFreshness:     0.10,
			WeightActivation:    0.20,
			WeightConfidence:    0.15,
			ImportanceThreshold: 0.7,
			ImportanceFloor:     0.5,
			HistoricalPenalty:   0.5,
		},

		Decay: DecayConfig{
			Rates: DecayRates{
				Ephemeral: 0.173,
				Temporal:  0.0096,
				Durable:   0.000321,
				Immutable: 0.0,
			},
		},

		Freshness: FreshnessConfig{
			Scale: 8760,
			Exponents: FreshnessExponents{
				Immutable: 0,
				Durable:   0.5,
				Temporal:  1.0,
				Ephemeral: 1.0,
			},
		},

		Activation: ActivationConfig{
			BaseAmount:        1.0,
			AttenuationFactor: 0.5,
		},

		Chunking: ChunkingConfig{
			Threshold:  512,
			ChunkSize:  512,
			Overlap:    128,
			SectionMin: 500,
			SectionMax: 5000,
		},

		Concepts: ConceptsConfig{
			EmergenceThreshold:     3,
			MaxKeywordPct:          0.2,
			MinContentLengthDirect: 50,
		},

		Dedup: DedupConfig{
			SimilarityThreshold: 0.92,
			Action:              "supersede",
		},

		Graph: GraphConfig{
			EdgeWeightTraversalThreshold: 0.3,
		},

		Storage: StorageConfig{
			ProllyTargetChunkSize: 64,
			ProllySplitBits:       6,
		},

		Merge: MergeConfig{
			ConflictStrategy: "timestamp_wins",
		},
	}
}

// DefaultDir returns the default Gramaton configuration directory.
func DefaultDir() string {
	return filepath.Join(defaultHomeDir(), ".gramaton")
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultDir(), "config.yaml")
}

// LoadWithFallback loads config with deep-merge semantics: start from
// Defaults(), overlay the global config (if present), then overlay the
// per-store config (if present). Keys absent from a layer's YAML inherit
// from the layer beneath. Explicit empty values (e.g. `key: []`,
// `key: {}`) replace. Normalization/bounds run once on the merged result.
//
// Either path may be missing; if both are missing the function returns
// normalized defaults.
func LoadWithFallback(storeCfgPath, globalCfgPath string) (Config, error) {
	cfg := Defaults()
	if err := overlay(&cfg, globalCfgPath); err != nil {
		return cfg, err
	}
	if storeCfgPath != globalCfgPath {
		if err := overlay(&cfg, storeCfgPath); err != nil {
			return cfg, err
		}
	}
	if err := normalize(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Load reads a config from the given path. If the file does not exist,
// returns defaults. Fields not specified in the file retain their defaults.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if err := overlay(&cfg, path); err != nil {
		return cfg, err
	}
	if err := normalize(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// overlay unmarshals the YAML at path onto cfg in place. Missing file is
// not an error (the layer is simply absent). Fields absent from the YAML
// retain whatever value cfg already held.
func overlay(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}

// normalize coerces legacy aliases and clamps out-of-range values on a
// loaded config. Runs once per load after all overlays have been applied.
func normalize(cfg *Config) error {
	// Dedup action coercion. See DedupConfig docs + design-decisions.md D37.
	// "flag" is a legacy alias that never had behavior distinct from
	// "supersede"; silently coerce for one release cycle. Empty (omitted
	// in YAML) -> default. Anything else -> error so typos surface.
	switch cfg.Dedup.Action {
	case "":
		cfg.Dedup.Action = "supersede"
	case "flag":
		cfg.Dedup.Action = "supersede"
	case "supersede", "reject":
		// ok
	default:
		return fmt.Errorf("config: invalid dedup.action %q; expected \"supersede\" or \"reject\"", cfg.Dedup.Action)
	}

	if cfg.LLMCuration.MaxCallsPerRun > 10000 {
		cfg.LLMCuration.MaxCallsPerRun = 10000
	}
	if cfg.LLMCuration.BatchSize > 5000 {
		cfg.LLMCuration.BatchSize = 5000
	}
	if cfg.LLMCuration.ContradictionBatchSize < 0 {
		cfg.LLMCuration.ContradictionBatchSize = 0
	}
	if cfg.LLMCuration.ContradictionBatchSize > 20 {
		// Beyond ~20 pairs the prompt size and parser reliability degrade.
		cfg.LLMCuration.ContradictionBatchSize = 20
	}
	if cfg.LLMCuration.ConceptCoherenceMin < 0 {
		cfg.LLMCuration.ConceptCoherenceMin = 0
	}
	if cfg.LLMCuration.ConceptCoherenceMin > 1 {
		cfg.LLMCuration.ConceptCoherenceMin = 1
	}
	if cfg.Curation.MaxOrphansPerRun > 200 {
		cfg.Curation.MaxOrphansPerRun = 200
	}
	if cfg.Curation.MaxDedupPerRun > 200 {
		cfg.Curation.MaxDedupPerRun = 200
	}
	return nil
}

// Save writes the config to the given path, creating parent directories
// as needed.
func Save(cfg Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create dir %s: %w", dir, err)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}

	return nil
}

func defaultHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
