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
	Limits     LimitsConfig     `yaml:"limits"`
	Merge      MergeConfig      `yaml:"merge"`
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

	// OpenAI-compatible
	BaseURL   string `yaml:"base_url,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`

	// Bedrock
	Region              string `yaml:"region,omitempty"`
	AWSProfile          string `yaml:"aws_profile,omitempty"`
	AWSAccessKeyIDEnv   string `yaml:"aws_access_key_id_env,omitempty"`
	AWSSecretAccessKey  string `yaml:"aws_secret_access_key_env,omitempty"`
}

type ScoringConfig struct {
	WeightSimilarity    float64 `yaml:"weight_similarity"`
	WeightRecency       float64 `yaml:"weight_recency"`
	WeightFreshness     float64 `yaml:"weight_freshness"`
	WeightFrequency     float64 `yaml:"weight_frequency"`
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
	DecayRate         float64 `yaml:"decay_rate"`
}

type ChunkingConfig struct {
	Threshold int `yaml:"threshold"`
	ChunkSize int `yaml:"chunk_size"`
	Overlap   int `yaml:"overlap"`
}

type ConceptsConfig struct {
	EmergenceThreshold    int `yaml:"emergence_threshold"`
	MinContentLengthDirect int `yaml:"min_content_length_direct"`
}

type DedupConfig struct {
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	Action              string  `yaml:"action"`
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
	MaxSummaryAbstract int           `yaml:"max_summary_abstract"`
	StdinTimeout       time.Duration `yaml:"stdin_timeout"`
	MaxWritesPerSecond int           `yaml:"max_writes_per_second"`
}

type MergeConfig struct {
	ConflictStrategy string `yaml:"conflict_strategy"`
}

// Defaults returns a Config with all values set to their documented defaults.
func Defaults() Config {
	return Config{
		DataDir: filepath.Join(defaultHomeDir(), ".gramaton", "data"),

		Server: ServerConfig{
			Port:        0,
			AutoStart:   true,
			IdleTimeout: 30 * time.Minute,
		},

		Embedding: EmbeddingConfig{
			Provider: "",
			Endpoint: "http://localhost:11434",
			Model:    "nomic-embed-text",
		},

		Scoring: ScoringConfig{
			WeightSimilarity:    0.35,
			WeightRecency:       0.15,
			WeightFreshness:     0.15,
			WeightFrequency:     0.1,
			WeightActivation:    0.05,
			WeightConfidence:    0.2,
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
			DecayRate:         0.05,
		},

		Chunking: ChunkingConfig{
			Threshold: 512,
			ChunkSize: 512,
			Overlap:   128,
		},

		Concepts: ConceptsConfig{
			EmergenceThreshold:    3,
			MinContentLengthDirect: 50,
		},

		Dedup: DedupConfig{
			SimilarityThreshold: 0.92,
			Action:              "flag",
		},

		Graph: GraphConfig{
			EdgeWeightTraversalThreshold: 0.3,
		},

		Limits: LimitsConfig{
			MaxJSONSize:        2 * 1024 * 1024,
			MaxNestingDepth:    10,
			MaxContentLength:   1024 * 1024,
			MaxKeywords:        100,
			MaxSummaryShort:    500,
			MaxSummaryAbstract: 5000,
			StdinTimeout:       30 * time.Second,
			MaxWritesPerSecond: 100,
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

	return cfg, nil
}

// Save writes the config to the given path, creating parent directories
// as needed.
func Save(cfg Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create dir %s: %w", dir, err)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
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
