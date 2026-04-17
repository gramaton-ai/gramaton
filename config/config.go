package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all Gramaton configuration.
type Config struct {
	DataDir string `yaml:"data_dir"`

	Server     ServerConfig     `yaml:"server"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	Scoring    ScoringConfig    `yaml:"scoring"`
	Decay      DecayConfig      `yaml:"decay"`
	Freshness  FreshnessConfig  `yaml:"freshness"`
	Activation ActivationConfig `yaml:"activation"`
	Chunking   ChunkingConfig   `yaml:"chunking"`
	Concepts   ConceptsConfig   `yaml:"concepts"`
	Dedup      DedupConfig      `yaml:"dedup"`
	Graph      GraphConfig      `yaml:"graph"`
	Storage    StorageConfig    `yaml:"storage"`
	Limits     LimitsConfig     `yaml:"limits"`
	Merge      MergeConfig      `yaml:"merge"`
	Logging    LoggingConfig    `yaml:"logging"`
	Curation   CurationConfig   `yaml:"curation"`
	LLM        LLMConfig        `yaml:"llm"`
	LLMCuration LLMCurationConfig `yaml:"llm_curation"`
	Observe     ObserveConfig     `yaml:"observe"`
	GC          GCConfig          `yaml:"gc"`
	Backup      BackupConfig      `yaml:"backup"`
	Search      SearchConfig      `yaml:"search"`
}

type ServerConfig struct {
	Port        int           `yaml:"port"`
	AutoStart   bool          `yaml:"auto_start"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

type EmbeddingConfig struct {
	Provider string `yaml:"provider"`
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`

	// MaxTokens overrides the model's context window (in tokens).
	// Auto-detected for Ollama models. Set manually for OpenAI or
	// Bedrock if the default (512) produces suboptimal chunk sizes.
	MaxTokens int `yaml:"max_tokens,omitempty"`

	// Dimension is the embedding vector dimension. Default 384
	// (mxbai-embed-large). Must match the model's output dimension. The flat vector
	// index file records its dimension; changing this after initial setup
	// requires re-embedding all records.
	Dimension int `yaml:"dimension,omitempty"`

	// OpenAI-compatible
	BaseURL    string `yaml:"base_url,omitempty"`
	APIKeyFile string `yaml:"api_key_file,omitempty"`
	APIKeyEnv  string `yaml:"api_key_env,omitempty"`

	// Bedrock
	Region              string `yaml:"region,omitempty"`
	AWSProfile          string `yaml:"aws_profile,omitempty"`
	AWSAccessKeyIDEnv   string `yaml:"aws_access_key_id_env,omitempty"`
	AWSSecretAccessKeyEnv string `yaml:"aws_secret_access_key_env,omitempty"`
}

type ScoringConfig struct {
	WeightSimilarity    float64 `yaml:"weight_similarity"`
	WeightFreshness     float64 `yaml:"weight_freshness"`
	WeightActivation    float64 `yaml:"weight_activation"`
	WeightConfidence    float64 `yaml:"weight_confidence"`
	ImportanceThreshold float64 `yaml:"importance_threshold"`
	ImportanceFloor     float64 `yaml:"importance_floor_ratio"`
	HistoricalPenalty   float64 `yaml:"historical_penalty"`
}

type DecayConfig struct {
	Rates DecayRates `yaml:"rates"`
}

type DecayRates struct {
	Ephemeral float64 `yaml:"ephemeral"`
	Temporal  float64 `yaml:"temporal"`
	Durable   float64 `yaml:"durable"`
	Immutable float64 `yaml:"immutable"`
}

type FreshnessConfig struct {
	Scale     float64          `yaml:"scale"`
	Exponents FreshnessExponents `yaml:"exponents"`
}

type FreshnessExponents struct {
	Immutable float64 `yaml:"immutable"`
	Durable   float64 `yaml:"durable"`
	Temporal  float64 `yaml:"temporal"`
	Ephemeral float64 `yaml:"ephemeral"`
}

type ActivationConfig struct {
	BaseAmount        float64 `yaml:"base_amount"`
	AttenuationFactor float64 `yaml:"attenuation_factor"`
}

// ChunkingConfig controls structural text splitting. NOT used in the
// capture hot path -- observation extraction (D18/D23) handles content
// decomposition in the curation cycle. Retained for internal utilities.
type ChunkingConfig struct {
	Threshold  int `yaml:"threshold"`
	ChunkSize  int `yaml:"chunk_size"`
	Overlap    int `yaml:"overlap"`
	SectionMin int `yaml:"section_min"` // min section size in chars (default 500)
	SectionMax int `yaml:"section_max"` // max section size in chars (default 5000)
}

type ConceptsConfig struct {
	EmergenceThreshold     int     `yaml:"emergence_threshold"`
	MinContentLengthDirect int     `yaml:"min_content_length_direct"`
	MaxKeywordPct          float64 `yaml:"max_keyword_pct"` // skip keywords in > this % of records (0-1.0, default 0.2)
}

type DedupConfig struct {
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	Action              string  `yaml:"action"`
}

type SearchConfig struct {
	BM25K1              float64 `yaml:"bm25_k1"`              // term frequency saturation (default 1.2)
	BM25B               float64 `yaml:"bm25_b"`               // length normalization (default 0.75)
	BM25WeightFull      float64 `yaml:"bm25_weight_full"`     // RRF weight for content_full BM25 (default 1.0)
	BM25WeightMedium    float64 `yaml:"bm25_weight_medium"`   // RRF weight for content_medium BM25 (default 2.0)
	BM25WeightShort     float64 `yaml:"bm25_weight_short"`    // RRF weight for content_short BM25 (default 3.0)
	RRFK                int     `yaml:"rrf_k"`                // RRF rank constant (default 60)
	SuggestionThreshold float64 `yaml:"suggestion_threshold"` // top-result score below which suggestions are returned (default 0.75)
	HNSWThreshold       int     `yaml:"hnsw_threshold"`       // vector count above which HNSW is used instead of flat scan (default 5000)
	HNSWM               int     `yaml:"hnsw_m"`               // HNSW max connections per layer (default 16)
	HNSWEfConstruction  int     `yaml:"hnsw_ef_construction"` // HNSW build quality (default 200)
	HNSWEfSearch        int     `yaml:"hnsw_ef_search"`       // HNSW search width (default 100)
	VectorOnlyPenalty   float64 `yaml:"vector_only_penalty"`  // score multiplier for results with no BM25 match (default 0.1)
	RetrievalCandidates int     `yaml:"retrieval_candidates"` // candidates from vector+BM25 before reranking (default 200)
	RerankEnabled       bool    `yaml:"rerank_enabled"`       // enable LLM reranking of candidates (default false)
	RerankCandidates    int     `yaml:"rerank_candidates"`    // how many candidates to send to LLM reranker (default 50)
	SessionDedupEnabled bool    `yaml:"session_dedup_enabled"` // suppress Session segments when extracted Memory record is in results (default false)
}

type GraphConfig struct {
	EdgeWeightTraversalThreshold float64 `yaml:"edge_weight_traversal_threshold"`
}

type LimitsConfig struct {
	MaxJSONSize        int           `yaml:"max_json_size"`
	MaxNestingDepth    int           `yaml:"max_nesting_depth"`
	MaxContentLength   int           `yaml:"max_content_length"`
	MaxKeywords        int           `yaml:"max_keywords"`
	MaxSummaryShort    int           `yaml:"max_summary_short"`
	StdinTimeout       time.Duration `yaml:"stdin_timeout"`
	MaxWritesPerSecond int           `yaml:"max_writes_per_second"`
}

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

type MergeConfig struct {
	ConflictStrategy string `yaml:"conflict_strategy"`
}

type LoggingConfig struct {
	Level       string `yaml:"level"`        // debug, info, warn, error
	MaxSizeMB   int    `yaml:"max_size_mb"`  // total disk budget for all log files
	RotateSizeMB int   `yaml:"rotate_size_mb"` // rotate when file reaches this size
}

type CurationConfig struct {
	Enabled             bool          `yaml:"enabled"`
	Interval            time.Duration `yaml:"interval"`
	OrphanSimilarityMin float64       `yaml:"orphan_similarity_min"`
	StaleEphemeralScore float64       `yaml:"stale_ephemeral_score"`
	StaleTemporalScore  float64       `yaml:"stale_temporal_score"`
	MaxOrphansPerRun    int           `yaml:"max_orphans_per_run"`
	MaxDedupPerRun      int           `yaml:"max_dedup_per_run"`
	SectionLinkMin        float64       `yaml:"section_link_min"`          // min similarity for cross-section linking (default 0.75)
	MaxSectionLinksPerRun int           `yaml:"max_section_links_per_run"` // cap per cycle (default 30)
	ObservationBatchSize  int           `yaml:"observation_batch_size"`    // parents per observation cycle (0=auto: 500 local, 20 external)
}

type LLMConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"` // default model for non-curation paths (sessions, extraction, inline classification)
	BaseURL    string `yaml:"base_url,omitempty"`
	APIKeyFile string `yaml:"api_key_file,omitempty"`
	APIKeyEnv  string `yaml:"api_key_env,omitempty"`
	Region     string `yaml:"region,omitempty"`
	AWSProfile string `yaml:"aws_profile,omitempty"`

	// Models maps effort tiers to concrete model names. Used by curation
	// task routing (see Config.ModelForTask). Each tier has a sensible
	// default from config.Defaults(); users override to pin versions or
	// swap providers without touching task-level code.
	Models LLMModels `yaml:"models"`

	// Bedrock credential env vars (alternative to aws_profile).
	AWSAccessKeyIDEnv     string `yaml:"aws_access_key_id_env,omitempty"`
	AWSSecretAccessKeyEnv string `yaml:"aws_secret_access_key_env,omitempty"`

	// Usage caps (0 = no cap).
	MaxCallsPerDay     int           `yaml:"max_calls_per_day,omitempty"`
	MaxCallsPerSession int           `yaml:"max_calls_per_session,omitempty"`
	RateLimitInterval  time.Duration `yaml:"rate_limit_interval,omitempty"`
}

// LLMModels maps effort tiers to model names. Every curation task picks
// its effort level (low/medium/high), and this struct turns that into a
// concrete model to pass to the provider. Keeping model names off the
// task-level code means a provider swap or model revision only edits
// this struct.
type LLMModels struct {
	Low    string `yaml:"low"`    // cheap/fast tier (default: claude-haiku-4-5)
	Medium string `yaml:"medium"` // balanced tier (default: claude-sonnet-4-6)
	High   string `yaml:"high"`   // best-quality tier (default: claude-opus-4-7)
}

type LLMCurationConfig struct {
	BatchSize               int     `yaml:"batch_size"`
	MaxCallsPerRun          int     `yaml:"max_calls_per_run"`
	MaxContradictionChecks  int     `yaml:"max_contradiction_checks"`
	ContradictionMinSim     float64 `yaml:"contradiction_min_similarity"`
	ContradictionMaxSim     float64 `yaml:"contradiction_max_similarity"`
	MaxConceptsPerRun       int     `yaml:"max_concepts_per_run"`
	SynthesisBatchSize      int     `yaml:"synthesis_batch_size"`       // concepts per LLM call (default 5)
	SynthesisMaxInputTokens int     `yaml:"synthesis_max_input_tokens"` // soft cap per batch (default 8000)

	// Per-task effort levels. Empty = use the baked-in default for that
	// task (see defaultEffortForTask). Set to "low", "medium", or "high"
	// to override. The resolved effort maps to a concrete model via
	// LLM.Models -- callers never name a model here, so a provider swap
	// (or a new Haiku/Sonnet/Opus revision) only updates LLM.Models.
	ClassificationShortEffort string `yaml:"classification_short_effort"`
	ClassificationLongEffort  string `yaml:"classification_long_effort"`
	SummarizationEffort       string `yaml:"summarization_effort"`
	ContradictionEffort       string `yaml:"contradiction_effort"`
	ConceptEffort             string `yaml:"concept_effort"`
	ManifestEffort            string `yaml:"manifest_effort"`

	// Length cutoff for short-vs-long classification. Default 2000 chars.
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
// resolves task -> effort -> model name. contentLen is only consulted
// for classification (short vs long); other tasks ignore it.
func (c Config) ModelForTask(task CurationTask) string {
	return c.ModelAtEffort(c.EffortForTask(task))
}

type GCConfig struct {
	Enabled    bool `yaml:"enabled"`
	DryRun     bool `yaml:"dry_run"`
	MinAgeDays int  `yaml:"min_age_days"`
}

type ObserveConfig struct {
	Enabled                bool    `yaml:"enabled"`
	MaxFactsPerCall        int     `yaml:"max_facts_per_call"`
	DefaultConfidence      float64 `yaml:"default_confidence"`
	DefaultTemporality     string  `yaml:"default_temporality"`
	SubstanceMinLength     int     `yaml:"substance_min_length"`
	FeedbackLoopHours      int     `yaml:"feedback_loop_hours"`
	FeedbackLoopSimilarity float64 `yaml:"feedback_loop_similarity"`
	RetrievalTracking      bool    `yaml:"retrieval_tracking"`
	RetrievalSimilarity    float64 `yaml:"retrieval_similarity"`
}

type BackupConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Dir      string        `yaml:"dir"`
	Retain   int           `yaml:"retain"`
	Schedule time.Duration `yaml:"schedule"`
}

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
			EmergenceThreshold:    3,
		MaxKeywordPct:         0.2,
			MinContentLengthDirect: 50,
		},

		Dedup: DedupConfig{
			SimilarityThreshold: 0.92,
			Action:              "flag",
		},

		Graph: GraphConfig{
			EdgeWeightTraversalThreshold: 0.3,
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

		Storage: StorageConfig{
			ProllyTargetChunkSize: 64,
			ProllySplitBits:       6,
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

		Merge: MergeConfig{
			ConflictStrategy: "timestamp_wins",
		},

		Logging: LoggingConfig{
			Level:        "info",
			MaxSizeMB:    512,
			RotateSizeMB: 50,
		},

		Curation: CurationConfig{
			Enabled:             true,
			Interval:            1 * time.Minute,
			OrphanSimilarityMin: 0.6,
			StaleEphemeralScore: 0.95,
			StaleTemporalScore:  0.99,
			MaxOrphansPerRun:      20,
			MaxDedupPerRun:        20,
			SectionLinkMin:        0.75,
			MaxSectionLinksPerRun: 30,
			ObservationBatchSize: 0, // auto: 500 for local providers, 20 for external
		},

		LLM: LLMConfig{
			Model: "claude-sonnet-4-6", // used by non-curation paths (sessions, inline classification)
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
			MaxConceptsPerRun:           5,
			SynthesisBatchSize:          5,
			SynthesisMaxInputTokens:     8000,
			LongClassificationThreshold: 2000,

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

		GC: GCConfig{
			Enabled:    false,
			DryRun:     true,
			MinAgeDays: 30,
		},

		Backup: BackupConfig{
			Enabled:  true,
			Retain:   2,
			Schedule: 24 * time.Hour,
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

// LoadWithFallback loads config from storeCfgPath. If it doesn't exist,
// falls back to globalCfgPath. If neither exists, returns defaults.
func LoadWithFallback(storeCfgPath, globalCfgPath string) (Config, error) {
	if _, err := os.Stat(storeCfgPath); err == nil {
		return Load(storeCfgPath)
	}
	return Load(globalCfgPath)
}

// Load reads a config from the given path. If the file does not exist,
// returns defaults. Fields not specified in the file retain their defaults.
func Load(path string) (Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Enforce bounds on configurable limits.
	if cfg.LLMCuration.MaxCallsPerRun > 10000 {
		cfg.LLMCuration.MaxCallsPerRun = 10000
	}
	if cfg.LLMCuration.BatchSize > 5000 {
		cfg.LLMCuration.BatchSize = 5000
	}
	if cfg.Curation.MaxOrphansPerRun > 200 {
		cfg.Curation.MaxOrphansPerRun = 200
	}
	if cfg.Curation.MaxDedupPerRun > 200 {
		cfg.Curation.MaxDedupPerRun = 200
	}

	return cfg, nil
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
