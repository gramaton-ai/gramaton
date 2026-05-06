package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	Server    ServerConfig    `yaml:"server"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Logging   LoggingConfig   `yaml:"logging"`
	Backup    BackupConfig    `yaml:"backup"`
	GC        GCConfig        `yaml:"gc"`
	Curation  CurationConfig  `yaml:"curation"`
	LLM       LLMConfig       `yaml:"llm"`
	Observe   ObserveConfig   `yaml:"observe"`
	Limits    LimitsConfig    `yaml:"limits"`
	Search    SearchConfig    `yaml:"search"`
	Jobs      JobsConfig      `yaml:"jobs"`

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
	Telemetry  TelemetryConfig  `yaml:"telemetry"`
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

	// MaxWorkers caps the per-Embed-call goroutine count for providers
	// that parallelize internally (currently only `bert`). Default 0
	// means use min(GOMAXPROCS, 8). Lower this on memory-constrained
	// devices; raise on high-memory hosts. Each in-flight worker holds
	// a Scratch buffer (~14MB for bge-small at maxSeq=512); total peak
	// is roughly MaxWorkers * 14MB.
	MaxWorkers int `yaml:"max_workers,omitempty"`
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
	// cost-reduction knobs in llm.curation have any effect and the
	// store loses free deterministic maintenance too.
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
	// see ObserveConfig for the surviving TF-IDF extractor.
	ObservationBatchSize int `yaml:"observation_batch_size"`

	// ObservationMinContentLength is the minimum content_full length
	// (chars) for a record to be eligible for observation extraction.
	// Lower values create observations on short records that are
	// essentially duplicates of their parent. Default 1500.
	ObservationMinContentLength int `yaml:"observation_min_content_length"`

	// MaxObservationAttempts caps how many cycles re-extract
	// observations on a parent whose embedding kept failing. Per-parent
	// state lives on `observation_extract_attempts` (Int64) +
	// `last_observation_extract_error` (String). At threshold, the
	// parent is skipped at the candidate-selection guard. Higher
	// default than the LLM-cost counters because embedding failures
	// are typically transient (timeout, OOM, rate limit) -- 5 gives
	// transient errors more headroom before locking out. Zero
	// disables (legacy infinite-retry behavior). Embedding cost is
	// CPU/GPU only on local providers (bert/ollama) but billed input
	// tokens on paid providers, so the counter prevents quiet token
	// burn even though no LLM is involved.
	MaxObservationAttempts int `yaml:"max_observation_attempts"`
}

// LLMConfig is the single home for every LLM-related dial. Sub-structs
// keep concerns separated:
//
//   - top-level fields: provider + auth (the only fields a typical
//     operator must touch).
//   - Rerank: search-time LLM reranking toggle + tuning.
//   - Models: tier definitions (low/medium/high) and per-task tier
//     assignments. All model-selection lives here; deleting Tasks
//     entries falls back to the baked-in defaults.
//   - CostLimits: caps that apply to ALL llm calls, regardless of
//     subsystem.
//   - Curation: knobs for the autonomous LLM-driven cleanup cycle.
//     Carries the tuning knobs operators are not expected to touch.
type LLMConfig struct {
	// Provider: "anthropic", "openai", "bedrock", "claude-cli", "kiro-cli".
	// claude-cli and kiro-cli shell out to the vendor's interactive CLI;
	// they are unsupported and may violate the vendor's terms of service
	// (see docs/providers.md).
	Provider string `yaml:"provider"`

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

	// Rerank: search-time LLM reranking of retrieval candidates.
	Rerank LLMRerankConfig `yaml:"rerank"`

	// Models: tier definitions + per-task tier assignments.
	Models LLMModels `yaml:"models"`

	// CostLimits: caps applied to ALL llm calls.
	CostLimits LLMCostLimitsConfig `yaml:"cost_limits"`

	// Curation: tuning knobs for the autonomous cleanup cycle.
	// Operators are not expected to edit these.
	Curation LLMCurationConfig `yaml:"curation"`
}

// LLMRerankConfig controls the search-time LLM reranker. When enabled,
// the top Candidates retrieval results are passed to the LLM (at the
// `rerank` task tier) for relevance reordering. Adds ~1 LLM call per
// search.
type LLMRerankConfig struct {
	// Enabled: send retrieval candidates back through the LLM for
	// relevance reordering.
	Enabled bool `yaml:"enabled"`

	// Candidates: number of initial results fed to the reranker.
	// More = better quality, more cost + latency.
	Candidates int `yaml:"candidates"`
}

// LLMCostLimitsConfig is the cost backstop. Caps apply to every llm
// call regardless of subsystem. Zero disables a given cap.
type LLMCostLimitsConfig struct {
	// MaxCallsPerDay caps total LLM calls per calendar day. 0 = no
	// cap. Backstop when MaxCostUSDPerDay can't compute a cost
	// (unknown model in the pricing table).
	MaxCallsPerDay int `yaml:"max_calls_per_day"`

	// MaxCallsPerSession caps LLM calls per server session
	// (between starts). 0 = no cap.
	MaxCallsPerSession int `yaml:"max_calls_per_session"`

	// MaxCostUSDPerDay caps total estimated LLM cost per calendar
	// day in USD. 0 = no cap. Cost is estimated via llm.EstimateCost
	// using the per-model pricing table; unknown models contribute 0
	// (MaxCallsPerDay backs that case).
	MaxCostUSDPerDay float64 `yaml:"max_cost_usd_per_day"`

	// MaxCostUSDPerRun caps estimated LLM cost per curation cycle.
	// 0 = no cap. Cycle-level twin of MaxCostUSDPerDay.
	MaxCostUSDPerRun float64 `yaml:"max_cost_usd_per_run"`

	// RateLimitInterval is the minimum gap between successive LLM
	// calls. 0 = no rate limit.
	RateLimitInterval time.Duration `yaml:"rate_limit_interval"`

	// MaxResponseTokens caps the response length the provider is
	// allowed to generate. Currently honored only by the Anthropic
	// provider. 0 falls back to the per-provider default
	// (4096 for Anthropic).
	MaxResponseTokens int `yaml:"max_response_tokens"`
}

// LLMModels carries the three tier-to-model assignments and the
// per-task tier overrides. Every code path that calls an LLM has a
// task name; Tasks[task] picks the tier; the tier picks the model.
// Keeping model names off the task-level code means a provider swap
// or model revision only edits this struct.
type LLMModels struct {
	// Low: cheap, fast tier. Use for high-volume / easy tasks.
	Low string `yaml:"low"`

	// Medium: balanced tier. Use for tasks that need real reasoning.
	Medium string `yaml:"medium"`

	// High: best-quality tier. Use sparingly -- premium pricing.
	High string `yaml:"high"`

	// Tasks maps task name -> tier ("low", "medium", "high"). Unset
	// keys fall back to defaultEffortForTask. Unknown tier strings
	// also fall back to the default. Recognized task names match the
	// LLMTask constants below.
	Tasks map[string]string `yaml:"tasks"`
}

// LLMCurationConfig is curation-cycle tuning. WARNING: these values
// control algorithmic behavior that has been carefully tuned. Do not
// edit unless you have a specific reason and have read the relevant
// code path. Wrong values silently degrade store quality (missed
// contradictions, generic concepts, runaway cost).
type LLMCurationConfig struct {
	// BatchSize is the number of records classified per LLM call.
	BatchSize int `yaml:"batch_size"`

	// MaxCallsPerRun is the hard cap on total LLM calls in one
	// curation cycle. Protects against a runaway cycle. Acts as
	// backstop when CostLimits.MaxCostUSDPerRun can't compute a cost
	// (unknown model in pricing table) -- keep set even when cost
	// cap is enabled.
	MaxCallsPerRun int `yaml:"max_calls_per_run"`

	// LongClassificationThreshold is the character cutoff between
	// short-tier and long-tier classification. Records below this
	// route to the short tier.
	LongClassificationThreshold int `yaml:"long_classification_threshold"`

	// TaskTimeout is the wall-clock cap on a single curation task
	// (classify, summarize, enrich, contradict, manifest). On
	// timeout, the in-flight LLM call is cancelled and the next task
	// starts with a fresh ctx. Default: 90s. Zero disables.
	TaskTimeout time.Duration `yaml:"task_timeout"`

	// PromptCachingEnabled caches invariant system prompts on
	// providers that support it (Anthropic). Falls back to inline
	// concatenation when off or unsupported.
	PromptCachingEnabled bool `yaml:"prompt_caching_enabled"`

	// ManifestCacheEnabled skips the manifest LLM call when the
	// store's state fingerprint is unchanged from the previous cycle.
	ManifestCacheEnabled bool `yaml:"manifest_cache_enabled"`

	// ClassifyShortPromptCompressed routes short-tier records to the
	// condensed ClassifySystemPromptShort (~60% smaller). Long-tier
	// records always use the full prompt.
	ClassifyShortPromptCompressed bool `yaml:"classify_short_prompt_compressed"`

	// Contradiction: tuning for contradiction/supersession detection.
	Contradiction LLMContradictionConfig `yaml:"contradiction"`

	// Concept: tuning for concept synthesis.
	Concept LLMConceptConfig `yaml:"concept"`

	// Retries: per-record/per-pair retry caps. Without these, a
	// pathological record (oversized content, content-policy refusal,
	// persistent parse failure) re-enters every cycle forever and
	// rebills the same input tokens.
	Retries LLMRetriesConfig `yaml:"retries"`
}

// LLMContradictionConfig tunes the contradiction/supersession detector.
type LLMContradictionConfig struct {
	// MaxChecks is the maximum number of candidate pairs examined
	// for contradictions per cycle. With BatchSize > 1, multiple
	// pairs share one LLM call.
	MaxChecks int `yaml:"max_checks"`

	// MinSimilarity is the lower bound of the cosine-similarity band
	// for contradiction candidates. Pairs below this are too
	// dissimilar to meaningfully contradict.
	MinSimilarity float64 `yaml:"min_similarity"`

	// MaxSimilarity is the upper bound. Pairs above this are
	// near-duplicates handled by auto-supersession, not contradiction
	// detection.
	MaxSimilarity float64 `yaml:"max_similarity"`

	// BatchSize is the number of pairs packed into a single LLM
	// call. 1 = single-pair (legacy). 5-10 = batched (~N-fold call
	// reduction at saturation).
	BatchSize int `yaml:"batch_size"`

	// CheckReverseEdges skips candidate pairs that already have a
	// B->A edge (not just A->B). Reduces redundant work on
	// previously-linked pairs.
	CheckReverseEdges bool `yaml:"check_reverse_edges"`
}

// LLMConceptConfig tunes concept synthesis.
type LLMConceptConfig struct {
	// MaxPerRun caps concept syntheses per cycle.
	MaxPerRun int `yaml:"max_per_run"`

	// SynthesisBatchSize is the number of concepts packed into a
	// single synthesis LLM call.
	SynthesisBatchSize int `yaml:"synthesis_batch_size"`

	// SynthesisMaxInputTokens is a soft cap on input tokens per
	// synthesis batch. When exceeded, a new batch starts.
	SynthesisMaxInputTokens int `yaml:"synthesis_max_input_tokens"`

	// CoherenceMin is the minimum mean cosine similarity of member
	// records to their cluster centroid. Concepts below this are
	// skipped. 0 = no filter.
	CoherenceMin float64 `yaml:"coherence_min"`
}

// LLMRetriesConfig caps per-record / per-pair retry attempts.
// Default 3 across the board. Zero disables (legacy infinite retry).
type LLMRetriesConfig struct {
	// MaxClassifyAttempts caps how many times a single record will
	// be retried by autonomous classification before being marked
	// processing_status="stuck".
	MaxClassifyAttempts int `yaml:"max_classify_attempts"`

	// MaxSummaryAttempts caps how many times generateSummaries will
	// retry a single record. Independent of MaxClassifyAttempts: a
	// record can classify cleanly but produce an unusable summary.
	MaxSummaryAttempts int `yaml:"max_summary_attempts"`

	// MaxSynthesisAttempts caps how many times enrichConceptSyntheses
	// will retry a single concept node before flipping its
	// synthesis_status to "stuck".
	MaxSynthesisAttempts int `yaml:"max_synthesis_attempts"`

	// MaxManifestAttempts caps consecutive failures of
	// generateManifestSummary on the SAME store-state fingerprint.
	MaxManifestAttempts int `yaml:"max_manifest_attempts"`

	// MaxContradictionAttempts caps how many times a single pair
	// will be retried by detectContradictions before being
	// permanently excluded.
	MaxContradictionAttempts int `yaml:"max_contradiction_attempts"`

	// MaxEmbedAttempts caps how many times gramaton_reembed will
	// retry a single record before excluding it from the candidate
	// set.
	MaxEmbedAttempts int `yaml:"max_embed_attempts"`
}

// EffortLevel names the cost/quality tiers. Each tier maps to a concrete
// model via LLM.Models; tasks declare effort, not model names.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"    // cheap, fast (default: haiku)
	EffortMedium EffortLevel = "medium" // balanced (default: sonnet)
	EffortHigh   EffortLevel = "high"   // best quality (default: opus)
)

// LLMTask names every code path that calls an LLM. Each task has a
// default effort level (defaultEffortForTask); users override via the
// llm.models.tasks map in config.yaml.
type LLMTask string

const (
	TaskClassificationShort LLMTask = "classification_short"
	TaskClassificationLong  LLMTask = "classification_long"
	TaskSummarization       LLMTask = "summarization"
	TaskContradiction       LLMTask = "contradiction"
	TaskConcept             LLMTask = "concept"
	TaskManifest            LLMTask = "manifest"
	TaskRerank              LLMTask = "rerank"
	TaskDecompose           LLMTask = "decompose"
)

// CurationTask is a deprecated alias for LLMTask retained until call
// sites migrate. New code should use LLMTask directly.
//
// Deprecated: use LLMTask.
type CurationTask = LLMTask

// defaultEffortForTask returns the out-of-the-box effort level for
// each task. Assignment reflects which tasks benefit from better
// reasoning: summarization, short classification, manifest rollup,
// and search-time tasks (rerank, decompose) are Haiku-grade.
// Long-content classification, contradiction detection, and concept
// synthesis need calibrated reasoning -> Sonnet-grade.
func defaultEffortForTask(task LLMTask) EffortLevel {
	switch task {
	case TaskClassificationShort, TaskSummarization, TaskManifest,
		TaskRerank, TaskDecompose:
		return EffortLow
	case TaskClassificationLong, TaskContradiction, TaskConcept:
		return EffortMedium
	}
	return EffortMedium
}

// EffortForTask resolves a task to its effort level, consulting the
// llm.models.tasks override map before falling back to the baked-in
// default. Unknown tier strings fall through to the default.
func (c Config) EffortForTask(task LLMTask) EffortLevel {
	if override, ok := c.LLM.Models.Tasks[string(task)]; ok {
		switch EffortLevel(override) {
		case EffortLow, EffortMedium, EffortHigh:
			return EffortLevel(override)
		}
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

// ModelForTask is the primary entry point for task model selection:
// resolves task -> effort -> model name.
func (c Config) ModelForTask(task LLMTask) string {
	return c.ModelAtEffort(c.EffortForTask(task))
}

// ObserveConfig controls the deterministic TF-IDF observation extractor
// that runs every curation cycle (curation/observe.go). It decomposes
// long records (>= CurationConfig.ObservationMinContentLength) into
// sub-fact nodes that inherit the parent's metadata, for narrow-target
// semantic recall. Not related to automatic conversation capture --
// that is handled by the sessions flow (gramaton_session_prepare /
// gramaton_session_commit).
//
// The original `/v1/observe` LLM-driven endpoint plus its gating flag
// and metadata-default knobs (DefaultConfidence, DefaultTemporality,
// SubstanceMinLength, FeedbackLoopHours, FeedbackLoopSimilarity,
// RetrievalTracking, RetrievalSimilarity, and Enabled itself) were
// removed when sessions replaced it; the struct is now a single-field
// holder.
type ObserveConfig struct {
	// MaxFactsPerCall caps sub-facts extracted per parent record.
	// Defaults to 20 (D23). Each TF-IDF sentence becomes one
	// observation child node.
	MaxFactsPerCall int `yaml:"max_facts_per_call"`
}

// LimitsConfig holds request-level safety caps. These are not tuning
// knobs in the algorithmic sense, but rather environmental bounds to
// prevent unreasonably large inputs. Operators may raise these for
// unusual workloads (e.g., importing large documents).
type LimitsConfig struct {
	// NOTE: LimitsConfig only declares the caps the code actually
	// enforces. MaxNestingDepth and MaxWritesPerSecond were declared
	// but never wired; they were removed in the 2026-04-21 config-
	// drift sweep. Add a field here only when introducing the code
	// path that reads it.

	// MaxJSONSize is the largest JSON request body accepted by the
	// HTTP API, in bytes. Guards against memory exhaustion.
	MaxJSONSize int `yaml:"max_json_size"`

	// MaxContentLength caps the size of record content_full, in bytes.
	MaxContentLength int `yaml:"max_content_length"`

	// MaxKeywords caps the number of keywords per record.
	MaxKeywords int `yaml:"max_keywords"`

	// MaxSummaryShort caps the content_short length. Hard cap; soft
	// target is ~750 chars (the embedding-ready semantic anchor size).
	MaxSummaryShort int `yaml:"max_summary_short"`

	// StdinTimeout caps how long CLI commands wait for stdin input.
	StdinTimeout time.Duration `yaml:"stdin_timeout"`
}

// SearchConfig holds search parameters. The first four fields are
// user-facing cost/quality dials; the remainder are internal scoring
// and index parameters that operators rarely need to adjust.
type SearchConfig struct {
	// --- User-facing dials ---

	// RetrievalCandidates is the number of candidates pulled from
	// vector + BM25 before reranking. More = better recall, slower.
	RetrievalCandidates int `yaml:"retrieval_candidates"`

	// SessionDedupEnabled suppresses Session segments in search results
	// when the Memory record they were extracted into is also in the
	// result set. Reduces duplication across the two stores.
	SessionDedupEnabled bool `yaml:"session_dedup_enabled"`

	// Pagination controls cursor-based pagination on gramaton_search:
	// snapshot lifetime, candidate-set materialization size, and
	// per-call page size. See SearchPaginationConfig for individual
	// fields.
	Pagination SearchPaginationConfig `yaml:"pagination"`

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

// SearchPaginationConfig controls cursor-based pagination on
// gramaton_search. The values below are user-facing dials; the
// MaxCandidateCapHard ceiling defined alongside this struct caps
// CandidateCap to prevent accidental memory blowouts from typo'd
// values.
type SearchPaginationConfig struct {
	// SnapshotTTL is how long a search snapshot stays alive after
	// first call. Subsequent paginated calls slice the snapshot
	// rather than re-running the query. Default 20m.
	SnapshotTTL time.Duration `yaml:"snapshot_ttl"`

	// CandidateCap is the maximum number of ranked candidates
	// materialized into a snapshot per query. Bounds the page
	// table and the snapshot's memory footprint. Default 500.
	// Silently clamped to MaxCandidateCapHard at startup if the
	// configured value exceeds it (typo defense, not feature).
	CandidateCap int `yaml:"candidate_cap"`

	// PageSizeDefault is the default number of results per page
	// when the caller doesn't specify one. Default 20.
	PageSizeDefault int `yaml:"page_size_default"`

	// PageSizeMax is the upper bound on caller-supplied page size.
	// Default 100. Requests above this are silently clamped.
	PageSizeMax int `yaml:"page_size_max"`
}

// MaxCandidateCapHard is the hardcoded ceiling on
// SearchPaginationConfig.CandidateCap. Intentionally a code
// constant (not configurable) so users can't typo their way into
// memory pressure. ~50 bytes per snapshot entry × 1000 max × 10
// concurrent snapshots ≈ 500KB.
const MaxCandidateCapHard = 1000

// JobsConfig controls the `jobs/` package: persistence (one
// jobs.db per store), TTL-based GC, default async-batch limits,
// and the result-fetch timeout. The 0 = use default convention
// applies to every duration and integer here.
type JobsConfig struct {
	// Retention. Zero = keep forever for that status. Defaults
	// pinned at the engine init layer if zero.
	Retention JobsRetention `yaml:"retention"`

	// SweepInterval is how often the GC sweeper goroutine runs.
	// Default 1h. Set to 0 to disable the sweeper entirely; jobs
	// then accumulate until manually pruned.
	SweepInterval time.Duration `yaml:"sweep_interval"`

	// ResultDefaultTimeout is the default timeout for blocking
	// result fetches when the caller doesn't specify one. Default
	// 30 minutes.
	ResultDefaultTimeout time.Duration `yaml:"result_default_timeout"`

	// MaxAsyncBatchSize caps len(Items) on async-mode capture.
	// Default 1000. 0 = use default.
	MaxAsyncBatchSize int `yaml:"max_async_batch_size"`

	// MaxBatchBytes caps the total content bytes per batch
	// (sum of len(item.Content)). Default 256MB. 0 = use default.
	MaxBatchBytes int64 `yaml:"max_batch_bytes"`
}

// JobsRetention is the per-status TTL for terminal jobs.
type JobsRetention struct {
	Completed time.Duration `yaml:"completed"`
	Failed    time.Duration `yaml:"failed"`
	Cancelled time.Duration `yaml:"cancelled"`
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

	// MemberOverlapThreshold: Jaccard similarity above which a new
	// candidate is treated as an alias for an existing concept (or a
	// peer candidate emitted earlier in the same cycle) rather than
	// emitted as a separate concept node. Each member record carries
	// 5-6 content_keywords; without this gate, several keywords that
	// happen to cluster on the same evidence set each spawn their own
	// concept, producing duplicate clusters wearing different keyword
	// hats. Default 0.6. Setting 0 disables the gate (legacy behavior).
	MemberOverlapThreshold float64 `yaml:"member_overlap_threshold"`
}

// TelemetryConfig controls observability events emitted for design-
// or quality-decision investigation. These are structured slog events
// at INFO level intended for sampled review, not user-facing logging.
type TelemetryConfig struct {
	// ConceptMatchEnabled emits a "concept_match" event whenever a
	// query's embedding scores above the threshold against any
	// existing concept embedding. The event captures the query, every
	// matched concept (id, keyword, cosine), the concept's live member
	// IDs, and the records that surfaced in top-K. Used to gather data
	// on whether concept-based query expansion (PRF) would help before
	// committing to ship it. Default true: cost is bounded (~1ms per
	// query at <100 concepts) and the events only fire when concepts
	// match. Tracker 01KQ5JVY5DY7B0WNGBMKG1C3ND.
	ConceptMatchEnabled bool `yaml:"concept_match_enabled"`

	// ConceptMatchThreshold sets the cosine threshold above which
	// concept matches are logged. Default 0.7. Lower threshold = more
	// matches logged (noisier, but catches weaker would-have-PRF
	// effects); higher threshold = only strong matches (less noise).
	ConceptMatchThreshold float64 `yaml:"concept_match_threshold"`
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
			MaxObservationAttempts:      5,
			ObservationMinContentLength: 1500,
		},

		LLM: LLMConfig{
			Rerank: LLMRerankConfig{
				Enabled:    false,
				Candidates: 50,
			},
			Models: LLMModels{
				Low:    "claude-haiku-4-5",
				Medium: "claude-sonnet-4-6",
				High:   "claude-opus-4-7",
				// Tasks: explicit defaults so the rendered config
				// shows the affordance. Removing a key reverts to
				// defaultEffortForTask. Mirrors that function's
				// internal mapping; both must be updated together
				// if the canonical assignments change.
				Tasks: map[string]string{
					string(TaskClassificationShort): string(EffortLow),
					string(TaskClassificationLong):  string(EffortMedium),
					string(TaskSummarization):       string(EffortLow),
					string(TaskContradiction):       string(EffortMedium),
					string(TaskConcept):             string(EffortMedium),
					string(TaskManifest):            string(EffortLow),
					string(TaskRerank):              string(EffortLow),
					string(TaskDecompose):           string(EffortLow),
				},
			},
			CostLimits: LLMCostLimitsConfig{
				// All caps off by default. Operators set these per
				// their cost envelope.
			},
			Curation: LLMCurationConfig{
				BatchSize:                     10,
				MaxCallsPerRun:                20,
				LongClassificationThreshold:   2000,
				TaskTimeout:                   90 * time.Second,
				PromptCachingEnabled:          true,
				ManifestCacheEnabled:          true,
				ClassifyShortPromptCompressed: true,
				Contradiction: LLMContradictionConfig{
					MaxChecks:         5,
					MinSimilarity:     0.5,
					MaxSimilarity:     0.85,
					BatchSize:         5, // batched (~5x call reduction at saturation)
					CheckReverseEdges: true,
				},
				Concept: LLMConceptConfig{
					MaxPerRun:               5,
					SynthesisBatchSize:      5,
					SynthesisMaxInputTokens: 8000,
					CoherenceMin:            0.6, // skip when cluster mean cosine < 0.6
				},
				Retries: LLMRetriesConfig{
					MaxClassifyAttempts:      3,
					MaxSummaryAttempts:       3,
					MaxSynthesisAttempts:     3,
					MaxManifestAttempts:      3,
					MaxContradictionAttempts: 3,
					MaxEmbedAttempts:         3,
				},
			},
		},

		Observe: ObserveConfig{
			MaxFactsPerCall: 20,
		},

		Limits: LimitsConfig{
			MaxJSONSize:      2 * 1024 * 1024,
			MaxContentLength: 1024 * 1024,
			MaxKeywords:      100,
			MaxSummaryShort:  1000,
			StdinTimeout:     30 * time.Second,
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
			SessionDedupEnabled: true,
			Pagination: SearchPaginationConfig{
				SnapshotTTL:     20 * time.Minute,
				CandidateCap:    500,
				PageSizeDefault: 20,
				PageSizeMax:     100,
			},
		},

		Jobs: JobsConfig{
			Retention: JobsRetention{
				Completed: 90 * 24 * time.Hour,
				Failed:    365 * 24 * time.Hour,
				Cancelled: 90 * 24 * time.Hour,
			},
			SweepInterval:        1 * time.Hour,
			ResultDefaultTimeout: 30 * time.Minute,
			MaxAsyncBatchSize:    10000,
			MaxBatchBytes:        256 * 1024 * 1024, // 256MB
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
			MemberOverlapThreshold: 0.6,
		},

		Telemetry: TelemetryConfig{
			ConceptMatchEnabled:   true,
			ConceptMatchThreshold: 0.7,
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
	if err := Validate(&cfg); err != nil {
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
	if err := Validate(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate reports the first invariant violation in cfg, or nil if the
// config is self-consistent. Called automatically by Load and
// LoadWithFallback after normalize. Safe to call independently on a
// hand-constructed Config (e.g. from tests).
//
// Checked invariants:
//   - LLM.Provider is one of the supported providers (or empty = disabled)
//   - Embedding.Provider is one of the supported providers (or empty)
//   - Server.Port in [0, 65535]
//   - Decay.Rates.Immutable == 0 (immutable records never decay)
//   - Decay rates and scoring/BM25 weights are all non-negative
//
// Sum-to-1 is NOT enforced for scoring weights because search/score.go
// re-normalizes the meta weights at runtime; nor for BM25 weights
// because they are RRF weights with a documented non-unit default
// (1/2/3).
func Validate(cfg *Config) error {
	switch cfg.LLM.Provider {
	case "", "anthropic", "openai", "bedrock", "claude-cli", "kiro-cli":
		// ok
	default:
		return fmt.Errorf("config: invalid llm.provider %q; expected one of anthropic, openai, bedrock, claude-cli, kiro-cli (or empty to disable)", cfg.LLM.Provider)
	}

	switch cfg.Embedding.Provider {
	case "", "bert", "ollama", "openai", "bedrock":
		// ok
	default:
		return fmt.Errorf("config: invalid embedding.provider %q; expected one of bert, ollama, openai, bedrock (or empty to disable)", cfg.Embedding.Provider)
	}

	if cfg.Server.Port < 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("config: server.port %d out of range; expected 0-65535 (0 = auto-select)", cfg.Server.Port)
	}

	if cfg.Decay.Rates.Immutable != 0 {
		return fmt.Errorf("config: decay.rates.immutable must be 0 (immutable records never decay); got %v", cfg.Decay.Rates.Immutable)
	}
	for _, r := range []struct {
		name string
		v    float64
	}{
		{"decay.rates.ephemeral", cfg.Decay.Rates.Ephemeral},
		{"decay.rates.temporal", cfg.Decay.Rates.Temporal},
		{"decay.rates.durable", cfg.Decay.Rates.Durable},
	} {
		if r.v < 0 {
			return fmt.Errorf("config: %s must be non-negative; got %v", r.name, r.v)
		}
	}

	for _, w := range []struct {
		name string
		v    float64
	}{
		{"scoring.weight_similarity", cfg.Scoring.WeightSimilarity},
		{"scoring.weight_freshness", cfg.Scoring.WeightFreshness},
		{"scoring.weight_activation", cfg.Scoring.WeightActivation},
		{"scoring.weight_confidence", cfg.Scoring.WeightConfidence},
		{"search.bm25_weight_full", cfg.Search.BM25WeightFull},
		{"search.bm25_weight_medium", cfg.Search.BM25WeightMedium},
		{"search.bm25_weight_short", cfg.Search.BM25WeightShort},
	} {
		if w.v < 0 {
			return fmt.Errorf("config: %s must be non-negative; got %v", w.name, w.v)
		}
	}

	return nil
}

// overlay unmarshals the YAML at path onto cfg in place. Missing file is
// not an error (the layer is simply absent). Fields absent from the YAML
// retain whatever value cfg already held.
//
// The decoder runs with KnownFields(true): unknown keys fail loud with
// the offending name and line so typos surface at startup instead of
// silently reverting to defaults.
func overlay(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if err == io.EOF {
			// Empty file is a valid no-op overlay.
			return nil
		}
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}

// trimConfigStrings strips leading/trailing whitespace from string
// fields that represent paths, env var names, URLs, or identifiers.
// YAML preserves trailing whitespace inside values by design, but
// users hand-editing config.yaml (or generating it via here-docs,
// copy-paste, terminal clipboard artifacts) routinely introduce it
// by accident. Common symptom: api_key_file resolves to
// "/path/to/key.key   " (trailing spaces), gramaton tries to open
// that literal path, fails file-not-found, and the CLI's startup
// timeout hides the real error.
//
// Trimming happens after YAML unmarshal, not during, so code that
// constructs Config values directly (without Load / LoadWithFallback)
// is unaffected. Only string fields that semantically can't contain
// leading/trailing whitespace are trimmed -- identifiers and paths.
// We don't touch freeform fields (content bodies, descriptions)
// because Gramaton's config schema doesn't have any.
func trimConfigStrings(cfg *Config) {
	trim := func(s *string) { *s = strings.TrimSpace(*s) }

	trim(&cfg.DataDir)

	trim(&cfg.Embedding.Provider)
	trim(&cfg.Embedding.Endpoint)
	trim(&cfg.Embedding.Model)
	trim(&cfg.Embedding.BaseURL)
	trim(&cfg.Embedding.APIKeyFile)
	trim(&cfg.Embedding.APIKeyEnv)
	// APIKey is an opaque secret -- don't trim. Current providers
	// (Anthropic, OpenAI, Bedrock) use whitespace-free keys, but a
	// future proxy could legitimately emit a padded token, and
	// silent trimming would corrupt it. The APIKeyFile path and
	// APIKeyEnv name (which are trimmed) are the common input
	// modes; inline APIKey values are power-user territory.
	trim(&cfg.Embedding.Region)
	trim(&cfg.Embedding.AWSProfile)
	trim(&cfg.Embedding.AWSAccessKeyIDEnv)
	trim(&cfg.Embedding.AWSSecretAccessKeyEnv)

	trim(&cfg.LLM.Provider)
	trim(&cfg.LLM.BaseURL)
	trim(&cfg.LLM.APIKeyFile)
	trim(&cfg.LLM.APIKeyEnv)
	// APIKey is an opaque secret -- don't trim. See Embedding.APIKey
	// comment above for rationale.
	trim(&cfg.LLM.Region)
	trim(&cfg.LLM.AWSProfile)
	trim(&cfg.LLM.AWSAccessKeyIDEnv)
	trim(&cfg.LLM.AWSSecretAccessKeyEnv)
	trim(&cfg.LLM.Models.Low)
	trim(&cfg.LLM.Models.Medium)
	trim(&cfg.LLM.Models.High)

	trim(&cfg.Backup.Dir)
}

// normalize coerces legacy aliases and clamps out-of-range values on a
// loaded config. Runs once per load after all overlays have been applied.
func normalize(cfg *Config) error {
	// Whitespace trim first: downstream checks in this function
	// (empty-string comparisons for e.g. provider vs dedup.action)
	// assume strings are already trimmed. Also positions the trim
	// to run regardless of which error path follows.
	trimConfigStrings(cfg)

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

	if cfg.LLM.Curation.MaxCallsPerRun > 10000 {
		cfg.LLM.Curation.MaxCallsPerRun = 10000
	}
	if cfg.LLM.Curation.BatchSize > 5000 {
		cfg.LLM.Curation.BatchSize = 5000
	}
	if cfg.LLM.Curation.Contradiction.BatchSize < 0 {
		cfg.LLM.Curation.Contradiction.BatchSize = 0
	}
	if cfg.LLM.Curation.Contradiction.BatchSize > 20 {
		// Beyond ~20 pairs the prompt size and parser reliability degrade.
		cfg.LLM.Curation.Contradiction.BatchSize = 20
	}
	if cfg.LLM.Curation.Concept.CoherenceMin < 0 {
		cfg.LLM.Curation.Concept.CoherenceMin = 0
	}
	if cfg.LLM.Curation.Concept.CoherenceMin > 1 {
		cfg.LLM.Curation.Concept.CoherenceMin = 1
	}
	if cfg.Curation.MaxOrphansPerRun > 200 {
		cfg.Curation.MaxOrphansPerRun = 200
	}
	if cfg.Curation.MaxDedupPerRun > 200 {
		cfg.Curation.MaxDedupPerRun = 200
	}

	// Search pagination: zero-fill defaults so a partial yaml
	// override doesn't leave empty knobs, then clamp candidate_cap
	// to the hardcoded ceiling.
	if cfg.Search.Pagination.SnapshotTTL <= 0 {
		cfg.Search.Pagination.SnapshotTTL = 20 * time.Minute
	}
	if cfg.Search.Pagination.CandidateCap <= 0 {
		cfg.Search.Pagination.CandidateCap = 500
	}
	if cfg.Search.Pagination.CandidateCap > MaxCandidateCapHard {
		cfg.Search.Pagination.CandidateCap = MaxCandidateCapHard
	}
	if cfg.Search.Pagination.PageSizeDefault <= 0 {
		cfg.Search.Pagination.PageSizeDefault = 20
	}
	if cfg.Search.Pagination.PageSizeMax <= 0 {
		cfg.Search.Pagination.PageSizeMax = 100
	}
	if cfg.Search.Pagination.PageSizeDefault > cfg.Search.Pagination.PageSizeMax {
		cfg.Search.Pagination.PageSizeDefault = cfg.Search.Pagination.PageSizeMax
	}

	return nil
}

// Save writes the config to the given path, creating parent directories
// as needed. Permissions follow the convention used throughout Gramaton
// for files under ~/.gramaton/: directories at 0700 (owner-only access),
// files at 0600 (owner read/write only) -- they may carry credentials
// (api_key, api_key_file paths, server bind addresses) so any group/
// other access is wrong.
func Save(cfg Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create dir %s: %w", dir, err)
	}

	data, err := renderConfig(cfg)
	if err != nil {
		return err
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
