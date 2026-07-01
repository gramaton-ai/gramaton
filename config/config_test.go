package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	// Spot-check key defaults from the design docs.
	if cfg.Server.AutoStart != true {
		t.Fatal("server.auto_start should default to true")
	}
	if cfg.Server.IdleTimeout != 4*time.Hour {
		t.Fatalf("server.idle_timeout: expected 4h, got %v", cfg.Server.IdleTimeout)
	}
	if cfg.Scoring.WeightSimilarity != 0.55 {
		t.Fatalf("scoring.weight_similarity: expected 0.55, got %f", cfg.Scoring.WeightSimilarity)
	}
	if cfg.Scoring.WeightConfidence != 0.15 {
		t.Fatalf("scoring.weight_confidence: expected 0.15, got %f", cfg.Scoring.WeightConfidence)
	}
	if cfg.Decay.Rates.Ephemeral != 0.173 {
		t.Fatalf("decay.rates.ephemeral: expected 0.173, got %f", cfg.Decay.Rates.Ephemeral)
	}
	if cfg.Decay.Rates.Durable != 0.000321 {
		t.Fatalf("decay.rates.durable: expected 0.000321, got %f", cfg.Decay.Rates.Durable)
	}
	if cfg.Freshness.Scale != 8760 {
		t.Fatalf("freshness.scale: expected 8760, got %f", cfg.Freshness.Scale)
	}
	if cfg.Chunking.Threshold != 512 {
		t.Fatalf("chunking.threshold: expected 512, got %d", cfg.Chunking.Threshold)
	}
	if cfg.Concepts.EmergenceThreshold != 3 {
		t.Fatalf("concepts.emergence_threshold: expected 3, got %d", cfg.Concepts.EmergenceThreshold)
	}
	if cfg.Dedup.SimilarityThreshold != 0.92 {
		t.Fatalf("dedup.similarity_threshold: expected 0.92, got %f", cfg.Dedup.SimilarityThreshold)
	}
	if cfg.Dedup.Action != "supersede" {
		t.Fatalf("dedup.action: expected 'supersede', got %q", cfg.Dedup.Action)
	}
	if cfg.Graph.EdgeWeightTraversalThreshold != 0.3 {
		t.Fatalf("graph.edge_weight_traversal_threshold: expected 0.3, got %f", cfg.Graph.EdgeWeightTraversalThreshold)
	}
	if cfg.Limits.MaxJSONSize != 2*1024*1024 {
		t.Fatalf("limits.max_json_size: expected 2MB, got %d", cfg.Limits.MaxJSONSize)
	}
	if cfg.Limits.StdinTimeout != 30*time.Second {
		t.Fatalf("limits.stdin_timeout: expected 30s, got %v", cfg.Limits.StdinTimeout)
	}
	if cfg.Merge.ConflictStrategy != "timestamp_wins" {
		t.Fatalf("merge.conflict_strategy: expected 'timestamp_wins', got %q", cfg.Merge.ConflictStrategy)
	}
	if cfg.Embedding.Endpoint != "http://localhost:11434" {
		t.Fatalf("embedding.endpoint: expected ollama default, got %q", cfg.Embedding.Endpoint)
	}

	// Search pagination defaults.
	if cfg.Search.Pagination.SnapshotTTL != 20*time.Minute {
		t.Fatalf("search.pagination.snapshot_ttl: expected 20m, got %v", cfg.Search.Pagination.SnapshotTTL)
	}
	if cfg.Search.Pagination.CandidateCap != 500 {
		t.Fatalf("search.pagination.candidate_cap: expected 500, got %d", cfg.Search.Pagination.CandidateCap)
	}
	if cfg.Search.Pagination.PageSizeDefault != 20 {
		t.Fatalf("search.pagination.page_size_default: expected 20, got %d", cfg.Search.Pagination.PageSizeDefault)
	}
	if cfg.Search.Pagination.PageSizeMax != 100 {
		t.Fatalf("search.pagination.page_size_max: expected 100, got %d", cfg.Search.Pagination.PageSizeMax)
	}
}

// TestNormalizeSearchPaginationClamps pins the safety rails on
// SearchPaginationConfig: candidate_cap > MaxCandidateCapHard
// silently clamps; zero values get the defaults; page_size_default
// can't exceed page_size_max.
func TestNormalizeSearchPaginationClamps(t *testing.T) {
	cases := []struct {
		name string
		in   SearchPaginationConfig
		want SearchPaginationConfig
	}{
		{
			name: "all zeros -> defaults",
			in:   SearchPaginationConfig{},
			want: SearchPaginationConfig{
				SnapshotTTL:     20 * time.Minute,
				CandidateCap:    500,
				PageSizeDefault: 20,
				PageSizeMax:     100,
			},
		},
		{
			name: "candidate_cap above ceiling -> clamps to ceiling",
			in:   SearchPaginationConfig{CandidateCap: 100000, PageSizeDefault: 20, PageSizeMax: 100, SnapshotTTL: time.Minute},
			want: SearchPaginationConfig{
				SnapshotTTL:     time.Minute,
				CandidateCap:    MaxCandidateCapHard,
				PageSizeDefault: 20,
				PageSizeMax:     100,
			},
		},
		{
			name: "page_size_default above page_size_max -> clamps to max",
			in:   SearchPaginationConfig{CandidateCap: 500, PageSizeDefault: 200, PageSizeMax: 50, SnapshotTTL: time.Minute},
			want: SearchPaginationConfig{
				SnapshotTTL:     time.Minute,
				CandidateCap:    500,
				PageSizeDefault: 50,
				PageSizeMax:     50,
			},
		},
		{
			name: "negative values -> filled with defaults",
			in:   SearchPaginationConfig{SnapshotTTL: -1, CandidateCap: -1, PageSizeDefault: -1, PageSizeMax: -1},
			want: SearchPaginationConfig{
				SnapshotTTL:     20 * time.Minute,
				CandidateCap:    500,
				PageSizeDefault: 20,
				PageSizeMax:     100,
			},
		},
		{
			name: "valid values preserved",
			in:   SearchPaginationConfig{SnapshotTTL: 5 * time.Minute, CandidateCap: 750, PageSizeDefault: 10, PageSizeMax: 50},
			want: SearchPaginationConfig{
				SnapshotTTL:     5 * time.Minute,
				CandidateCap:    750,
				PageSizeDefault: 10,
				PageSizeMax:     50,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Search.Pagination = tc.in
			cfg.Dedup.Action = "supersede" // satisfy normalize's required field
			if err := normalize(&cfg); err != nil {
				t.Fatalf("normalize: %v", err)
			}
			got := cfg.Search.Pagination
			if got != tc.want {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := Defaults()
	cfg.Scoring.WeightSimilarity = 0.5
	cfg.Embedding.Provider = "ollama"
	cfg.Dedup.Action = "reject"

	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Scoring.WeightSimilarity != 0.5 {
		t.Fatalf("expected 0.5, got %f", loaded.Scoring.WeightSimilarity)
	}
	if loaded.Embedding.Provider != "ollama" {
		t.Fatalf("expected 'ollama', got %q", loaded.Embedding.Provider)
	}
	if loaded.Dedup.Action != "reject" {
		t.Fatalf("expected 'reject', got %q", loaded.Dedup.Action)
	}
}

// TestLoadCoercesLegacyFlagAction asserts that pre-2026-04 configs with
// dedup.action: "flag" are silently coerced to "supersede" at load time.
// See design-decisions.md D37: "flag" never had behavior distinct from
// "supersede" in any capture path, so the coercion is a cosmetic rename
// preserving prior behavior.
func TestLoadCoercesLegacyFlagAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte("dedup:\n  action: flag\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Dedup.Action != "supersede" {
		t.Fatalf("legacy 'flag' should coerce to 'supersede', got %q", cfg.Dedup.Action)
	}
}

// TestLoadRejectsUnknownDedupAction asserts that typos or unsupported
// values in dedup.action error at config load rather than silently
// being ignored or coerced to a default. Surfaces invalid configs
// before they produce surprising runtime behavior.
func TestLoadRejectsUnknownDedupAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Common typo: "supercede" (missing 's'). Should not silently coerce.
	content := []byte("dedup:\n  action: supercede\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid dedup.action, got nil")
	}
}

// TestLoadEmptyDedupActionCoercesToSupersede asserts that a config that
// omits dedup.action (or sets it to the empty string explicitly) ends up
// with the default value populated. This matches Defaults() and keeps
// the downstream capture paths from having to handle the empty string.
func TestLoadEmptyDedupActionCoercesToSupersede(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte("dedup:\n  action: \"\"\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Dedup.Action != "supersede" {
		t.Fatalf("empty dedup.action should default to 'supersede', got %q", cfg.Dedup.Action)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}

	defaults := Defaults()
	if cfg.Scoring.WeightSimilarity != defaults.Scoring.WeightSimilarity {
		t.Fatal("missing file should return defaults")
	}
}

func TestLoadPartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Write a config that only sets one field.
	content := []byte("scoring:\n  weight_similarity: 0.99\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Overridden field.
	if cfg.Scoring.WeightSimilarity != 0.99 {
		t.Fatalf("expected 0.99, got %f", cfg.Scoring.WeightSimilarity)
	}

	// Non-overridden fields should retain defaults.
	if cfg.Scoring.WeightConfidence != 0.15 {
		t.Fatalf("expected default 0.15, got %f", cfg.Scoring.WeightConfidence)
	}
	if cfg.Chunking.Threshold != 512 {
		t.Fatalf("expected default 512, got %d", cfg.Chunking.Threshold)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("{{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestSaveCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "config.yaml")

	if err := Save(Defaults(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
}

// TestLoadTrimsWhitespaceFromPathsAndIdentifiers exercises the
// trim-on-load behavior. Users hand-editing config.yaml via `cat
// >> <<EOF` heredocs, clipboard paste, or terminal emulators that
// add trailing whitespace routinely leave values like:
//
//	api_key_file: /Users/b/.gramaton/anthropic.key[trailing spaces]
//
// Without trimming, gramaton tries to open a literal path containing
// those trailing spaces and fails file-not-found. The CLI's server
// startup timeout then hides the real error. The fix: trim on load
// so downstream consumers see clean values.
func TestLoadTrimsWhitespaceFromPathsAndIdentifiers(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	// Seed a config where every trim-candidate field has trailing
	// whitespace. Using string concatenation so a careless future
	// editor doesn't accidentally "fix" the test data via auto-
	// whitespace-strip. YAML spec: trailing spaces on a scalar
	// value ARE part of the value -- that's the exact bug we're
	// fixing on load.
	yamlBody := "data_dir: /tmp/example   \n" +
		"llm:\n" +
		"    provider: anthropic  \n" +
		"    api_key_file: /tmp/key   \n" +
		"    api_key_env: MY_ENV   \n" +
		"    region: us-west-2  \n" +
		"    aws_profile: my-profile   \n" +
		"    base_url: https://example.com   \n" +
		"    models:\n" +
		"        medium: claude-sonnet-4-6   \n" +
		"embedding:\n" +
		"    provider: bert   \n" +
		"    model: bge-small-en-v1.5   \n" +
		"    api_key_file: /tmp/openai.key   \n" +
		"backup:\n" +
		"    dir: /tmp/backups   \n"

	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"DataDir", cfg.DataDir, "/tmp/example"},
		{"LLM.Provider", cfg.LLM.Provider, "anthropic"},
		{"LLM.Models.Medium", cfg.LLM.Models.Medium, "claude-sonnet-4-6"},
		{"LLM.APIKeyFile", cfg.LLM.APIKeyFile, "/tmp/key"},
		{"LLM.APIKeyEnv", cfg.LLM.APIKeyEnv, "MY_ENV"},
		{"LLM.Region", cfg.LLM.Region, "us-west-2"},
		{"LLM.AWSProfile", cfg.LLM.AWSProfile, "my-profile"},
		{"LLM.BaseURL", cfg.LLM.BaseURL, "https://example.com"},
		{"Embedding.Provider", cfg.Embedding.Provider, "bert"},
		{"Embedding.Model", cfg.Embedding.Model, "bge-small-en-v1.5"},
		{"Embedding.APIKeyFile", cfg.Embedding.APIKeyFile, "/tmp/openai.key"},
		{"Backup.Dir", cfg.Backup.Dir, "/tmp/backups"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
		if strings.HasSuffix(c.got, " ") || strings.HasPrefix(c.got, " ") {
			t.Errorf("%s: value has leading/trailing whitespace: %q", c.name, c.got)
		}
	}
}

// TestLoadDoesNotTrimAPIKeyLiterals guards the carve-out in
// trimConfigStrings: APIKey fields hold opaque secrets and are
// deliberately NOT trimmed. Current providers use whitespace-free
// keys, but a future proxy could emit a padded token, and silent
// trimming would corrupt it. Users should supply APIKeyFile or
// APIKeyEnv in practice; this test just asserts the escape hatch.
func TestLoadDoesNotTrimAPIKeyLiterals(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	// Note the padded api_key values -- the test asserts they
	// round-trip untouched.
	yamlBody := "llm:\n" +
		"    provider: anthropic\n" +
		"    api_key: \"  sk-padded-secret  \"\n" +
		"embedding:\n" +
		"    provider: openai\n" +
		"    api_key: \"  another-padded-key  \"\n"

	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.APIKey != "  sk-padded-secret  " {
		t.Errorf("LLM.APIKey got %q, want %q (trim must not touch APIKey)",
			cfg.LLM.APIKey, "  sk-padded-secret  ")
	}
	if cfg.Embedding.APIKey != "  another-padded-key  " {
		t.Errorf("Embedding.APIKey got %q, want %q (trim must not touch APIKey)",
			cfg.Embedding.APIKey, "  another-padded-key  ")
	}
}

func TestNewConfigDefaults(t *testing.T) {
	cfg := Defaults()

	// Curation. Default was tightened from 5m -> 1m in commit 469f828 so
	// curation activity is more responsive after captures; test now
	// tracks that default.
	if cfg.Curation.Interval != 1*time.Minute {
		t.Fatalf("expected 1m curation interval, got %v", cfg.Curation.Interval)
	}
	if !cfg.Curation.Enabled {
		t.Fatal("curation should be enabled by default")
	}
	if cfg.Curation.OrphanSimilarityMin != 0.6 {
		t.Fatalf("expected 0.6, got %f", cfg.Curation.OrphanSimilarityMin)
	}

	// LLM.
	if cfg.LLM.Models.Medium != "claude-sonnet-4-6" {
		t.Fatalf("expected claude-sonnet-4-6, got %q", cfg.LLM.Models.Medium)
	}

	// LLM Curation.
	if cfg.LLM.Curation.BatchSize != 10 {
		t.Fatalf("expected batch 10, got %d", cfg.LLM.Curation.BatchSize)
	}
	if cfg.LLM.Curation.MaxCallsPerRun != 20 {
		t.Fatalf("expected 20 max calls, got %d", cfg.LLM.Curation.MaxCallsPerRun)
	}

	// Backup.
	if !cfg.Backup.Enabled {
		t.Fatal("backup should be enabled by default")
	}
	if cfg.Backup.Retain != 2 {
		t.Fatalf("expected retain 2, got %d", cfg.Backup.Retain)
	}
	if cfg.Backup.Schedule != 24*time.Hour {
		t.Fatalf("expected 24h schedule, got %v", cfg.Backup.Schedule)
	}

	// Logging.
	if cfg.Logging.Level != "info" {
		t.Fatalf("expected info, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.MaxSizeMB != 512 {
		t.Fatalf("expected 512MB, got %d", cfg.Logging.MaxSizeMB)
	}
	if cfg.Logging.RotateSizeMB != 50 {
		t.Fatalf("expected 50MB, got %d", cfg.Logging.RotateSizeMB)
	}
}

func TestLoadBoundsEnforcement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	os.WriteFile(path, []byte(`
llm:
  curation:
    max_calls_per_run: 999999
    batch_size: 999
curation:
  max_orphans_per_run: 999
  max_dedup_per_run: 999
`), 0o600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.Curation.MaxCallsPerRun > 10000 {
		t.Fatalf("MaxCallsPerRun should be capped at 10000, got %d", cfg.LLM.Curation.MaxCallsPerRun)
	}
	if cfg.LLM.Curation.BatchSize > 5000 {
		t.Fatalf("BatchSize should be capped at 5000, got %d", cfg.LLM.Curation.BatchSize)
	}
	if cfg.Curation.MaxOrphansPerRun > 200 {
		t.Fatalf("MaxOrphansPerRun should be capped, got %d", cfg.Curation.MaxOrphansPerRun)
	}
	if cfg.Curation.MaxDedupPerRun > 200 {
		t.Fatalf("MaxDedupPerRun should be capped, got %d", cfg.Curation.MaxDedupPerRun)
	}
}

func TestLoadWithFallbackStoreOverridesGlobal(t *testing.T) {
	storeDir := t.TempDir()
	globalDir := t.TempDir()

	storePath := filepath.Join(storeDir, "config.yaml")
	globalPath := filepath.Join(globalDir, "config.yaml")

	os.WriteFile(storePath, []byte("scoring:\n  weight_similarity: 0.77\n"), 0o600)
	os.WriteFile(globalPath, []byte("scoring:\n  weight_similarity: 0.33\n"), 0o600)

	cfg, err := LoadWithFallback(storePath, globalPath)
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if cfg.Scoring.WeightSimilarity != 0.77 {
		t.Fatalf("expected 0.77 (store overrides global), got %f", cfg.Scoring.WeightSimilarity)
	}
}

func TestLoadWithFallbackFallsToGlobal(t *testing.T) {
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, "config.yaml")

	os.WriteFile(globalPath, []byte("scoring:\n  weight_similarity: 0.33\n"), 0o600)

	// Store config doesn't exist.
	storePath := filepath.Join(t.TempDir(), "config.yaml")

	cfg, err := LoadWithFallback(storePath, globalPath)
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if cfg.Scoring.WeightSimilarity != 0.33 {
		t.Fatalf("expected 0.33 (global), got %f", cfg.Scoring.WeightSimilarity)
	}
}

func TestLoadWithFallbackBothMissing(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "config.yaml")
	globalPath := filepath.Join(t.TempDir(), "config.yaml")

	cfg, err := LoadWithFallback(storePath, globalPath)
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	defaults := Defaults()
	if cfg.Scoring.WeightSimilarity != defaults.Scoring.WeightSimilarity {
		t.Fatal("both missing should return defaults")
	}
}

// TestLoadWithFallbackMergeInheritsFromGlobal proves the deep-merge
// semantics: a partial store config inherits fields from the global
// config rather than silently zero-valuing them via Defaults(). This
// is the regression that the 2026-04-20 named-store setup hit --
// minimal store override left LLM empty and the server's New()
// constructor failed at startup.
func TestLoadWithFallbackMergeInheritsFromGlobal(t *testing.T) {
	storeDir := t.TempDir()
	globalDir := t.TempDir()
	storePath := filepath.Join(storeDir, "config.yaml")
	globalPath := filepath.Join(globalDir, "config.yaml")

	// Global sets LLM provider + model + logging. Store only overrides port.
	global := "" +
		"server:\n  port: 42982\n" +
		"llm:\n  provider: anthropic\n  models:\n    medium: claude-haiku-4-5\n" +
		"logging:\n  level: debug\n"
	store := "" +
		"server:\n  port: 7338\n"
	os.WriteFile(globalPath, []byte(global), 0o600)
	os.WriteFile(storePath, []byte(store), 0o600)

	cfg, err := LoadWithFallback(storePath, globalPath)
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}

	// Store's override applies.
	if cfg.Server.Port != 7338 {
		t.Fatalf("expected store port 7338, got %d", cfg.Server.Port)
	}
	// Fields only in global must be inherited by the merged config.
	if cfg.LLM.Provider != "anthropic" {
		t.Fatalf("expected inherited LLM provider=anthropic, got %q", cfg.LLM.Provider)
	}
	if cfg.LLM.Models.Medium != "claude-haiku-4-5" {
		t.Fatalf("expected inherited LLM model, got %q", cfg.LLM.Models.Medium)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("expected inherited logging level=debug, got %q", cfg.Logging.Level)
	}
}

// TestLoadWithFallbackSamePathNoDoubleLoad confirms that when the
// caller passes the same path for both layers (normal unnamed-store
// case) the function doesn't attempt to unmarshal the file twice and
// still returns the normalized config.
func TestLoadWithFallbackSamePathNoDoubleLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("scoring:\n  weight_similarity: 0.5\n"), 0o600)

	cfg, err := LoadWithFallback(path, path)
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if cfg.Scoring.WeightSimilarity != 0.5 {
		t.Fatalf("expected 0.5, got %f", cfg.Scoring.WeightSimilarity)
	}
}

func TestDefaultDir(t *testing.T) {
	dir := DefaultDir()
	if dir == "" {
		t.Fatal("DefaultDir should not be empty")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	if filepath.Base(path) != "config.yaml" {
		t.Fatalf("expected config.yaml, got %s", filepath.Base(path))
	}
}

func TestEffortForTask_DefaultsAndOverrides(t *testing.T) {
	cfg := Defaults()

	// Baked-in defaults.
	lowTasks := []CurationTask{TaskClassificationShort, TaskSummarization, TaskManifest}
	for _, task := range lowTasks {
		if got := cfg.EffortForTask(task); got != EffortLow {
			t.Errorf("default effort for %s = %s, want %s", task, got, EffortLow)
		}
	}
	medTasks := []CurationTask{TaskClassificationLong, TaskContradiction, TaskConcept}
	for _, task := range medTasks {
		if got := cfg.EffortForTask(task); got != EffortMedium {
			t.Errorf("default effort for %s = %s, want %s", task, got, EffortMedium)
		}
	}

	// Override wins.
	if cfg.LLM.Models.Tasks == nil {
		cfg.LLM.Models.Tasks = map[string]string{}
	}
	cfg.LLM.Models.Tasks["contradiction"] = "high"
	if got := cfg.EffortForTask(TaskContradiction); got != EffortHigh {
		t.Errorf("override effort for contradiction = %s, want high", got)
	}

	// Unknown override string falls back to default.
	cfg.LLM.Models.Tasks["concept"] = "extreme"
	if got := cfg.EffortForTask(TaskConcept); got != EffortMedium {
		t.Errorf("unknown override should fall back, got %s", got)
	}
}

func TestModelAtEffort_DefaultsAndOverrides(t *testing.T) {
	cfg := Defaults()

	if cfg.ModelAtEffort(EffortLow) != "claude-haiku-4-5" {
		t.Errorf("default low model = %q", cfg.ModelAtEffort(EffortLow))
	}
	if cfg.ModelAtEffort(EffortMedium) != "claude-sonnet-4-6" {
		t.Errorf("default medium model = %q", cfg.ModelAtEffort(EffortMedium))
	}
	if cfg.ModelAtEffort(EffortHigh) != "claude-opus-4-7" {
		t.Errorf("default high model = %q", cfg.ModelAtEffort(EffortHigh))
	}

	// User override.
	cfg.LLM.Models.Low = "my-local-model"
	if cfg.ModelAtEffort(EffortLow) != "my-local-model" {
		t.Errorf("overridden low model not picked up")
	}

	// Unset tier returns empty (caller handles fallback).
	cfg.LLM.Models.High = ""
	if cfg.ModelAtEffort(EffortHigh) != "" {
		t.Errorf("cleared tier should return empty, got %q", cfg.ModelAtEffort(EffortHigh))
	}
}

func TestModelForTask_EndToEnd(t *testing.T) {
	cfg := Defaults()

	// Short classification -> low tier -> haiku default.
	if got := cfg.ModelForTask(TaskClassificationShort); got != "claude-haiku-4-5" {
		t.Errorf("classification_short model = %q, want claude-haiku-4-5", got)
	}
	// Contradiction -> medium tier -> sonnet default.
	if got := cfg.ModelForTask(TaskContradiction); got != "claude-sonnet-4-6" {
		t.Errorf("contradiction model = %q, want claude-sonnet-4-6", got)
	}
	// Override effort + override tier model.
	if cfg.LLM.Models.Tasks == nil {
		cfg.LLM.Models.Tasks = map[string]string{}
	}
	cfg.LLM.Models.Tasks["summarization"] = "high"
	cfg.LLM.Models.High = "premium-summarizer"
	if got := cfg.ModelForTask(TaskSummarization); got != "premium-summarizer" {
		t.Errorf("summarization model after effort+tier override = %q, want premium-summarizer", got)
	}
}

func TestValidateDefaultsAccepted(t *testing.T) {
	cfg := Defaults()
	if err := Validate(&cfg); err != nil {
		t.Fatalf("Defaults() should validate, got %v", err)
	}
}

// TestDefaultsRoundTripYAML verifies the new LLM config schema marshals
// to YAML and unmarshals back to an equivalent Config. Pin against
// silent regressions where a field gains a yaml tag mismatch (e.g.
// rename in struct without rename in marshalled key) and the load path
// silently drops the value.
func TestDefaultsRoundTripYAML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	cfg := Defaults()
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Spot-check the nested fields most likely to silently drift.
	if loaded.LLM.Models.Low != cfg.LLM.Models.Low {
		t.Errorf("Models.Low: round-tripped %q, want %q", loaded.LLM.Models.Low, cfg.LLM.Models.Low)
	}
	if loaded.LLM.Curation.Contradiction.MaxChecks != cfg.LLM.Curation.Contradiction.MaxChecks {
		t.Errorf("Curation.Contradiction.MaxChecks: round-tripped %d, want %d",
			loaded.LLM.Curation.Contradiction.MaxChecks, cfg.LLM.Curation.Contradiction.MaxChecks)
	}
	if loaded.LLM.Curation.Concept.CoherenceMin != cfg.LLM.Curation.Concept.CoherenceMin {
		t.Errorf("Curation.Concept.CoherenceMin: round-tripped %f, want %f",
			loaded.LLM.Curation.Concept.CoherenceMin, cfg.LLM.Curation.Concept.CoherenceMin)
	}
	if loaded.LLM.Curation.Retries.MaxClassifyAttempts != cfg.LLM.Curation.Retries.MaxClassifyAttempts {
		t.Errorf("Curation.Retries.MaxClassifyAttempts: round-tripped %d, want %d",
			loaded.LLM.Curation.Retries.MaxClassifyAttempts, cfg.LLM.Curation.Retries.MaxClassifyAttempts)
	}
	if loaded.LLM.Rerank.Enabled != cfg.LLM.Rerank.Enabled {
		t.Errorf("Rerank.Enabled: round-tripped %v, want %v", loaded.LLM.Rerank.Enabled, cfg.LLM.Rerank.Enabled)
	}
}

// TestModelForTaskRerankAndDecompose covers the two new task entries
// (TaskRerank, TaskDecompose) added in the LLM-config uplift. Both
// default to low tier; both honor the llm.models.tasks override map.
func TestModelForTaskRerankAndDecompose(t *testing.T) {
	cfg := Defaults()
	if got := cfg.ModelForTask(TaskRerank); got != "claude-haiku-4-5" {
		t.Errorf("default rerank model = %q, want claude-haiku-4-5", got)
	}
	if got := cfg.ModelForTask(TaskDecompose); got != "claude-haiku-4-5" {
		t.Errorf("default decompose model = %q, want claude-haiku-4-5", got)
	}
	cfg.LLM.Models.Tasks = map[string]string{
		"rerank":    "high",
		"decompose": "medium",
	}
	if got := cfg.ModelForTask(TaskRerank); got != "claude-opus-4-7" {
		t.Errorf("overridden rerank model = %q, want claude-opus-4-7", got)
	}
	if got := cfg.ModelForTask(TaskDecompose); got != "claude-sonnet-4-6" {
		t.Errorf("overridden decompose model = %q, want claude-sonnet-4-6", got)
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "unknown llm provider",
			mutate: func(c *Config) { c.LLM.Provider = "gemini" },
			want:   "llm.provider",
		},
		{
			name:   "unknown embedding provider",
			mutate: func(c *Config) { c.Embedding.Provider = "cohere-local" },
			want:   "embedding.provider",
		},
		{
			name:   "negative port",
			mutate: func(c *Config) { c.Server.Port = -1 },
			want:   "server.port",
		},
		{
			name:   "port too high",
			mutate: func(c *Config) { c.Server.Port = 70000 },
			want:   "server.port",
		},
		{
			name:   "immutable decay non-zero",
			mutate: func(c *Config) { c.Decay.Rates.Immutable = 0.01 },
			want:   "decay.rates.immutable",
		},
		{
			name:   "negative decay rate",
			mutate: func(c *Config) { c.Decay.Rates.Ephemeral = -0.1 },
			want:   "decay.rates.ephemeral",
		},
		{
			name:   "negative scoring weight",
			mutate: func(c *Config) { c.Scoring.WeightFreshness = -0.1 },
			want:   "scoring.weight_freshness",
		},
		{
			name:   "negative bm25 weight",
			mutate: func(c *Config) { c.Search.BM25WeightShort = -0.5 },
			want:   "search.bm25_weight_short",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.mutate(&cfg)
			err := Validate(&cfg)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateAllowsEmptyProviders(t *testing.T) {
	// Empty providers mean "disabled." Should validate.
	cfg := Defaults()
	cfg.LLM.Provider = ""
	cfg.Embedding.Provider = ""
	if err := Validate(&cfg); err != nil {
		t.Fatalf("empty providers should validate, got %v", err)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Typo: weight_similarty (missing 'i') should fail strict decoding.
	os.WriteFile(path, []byte("scoring:\n  weight_similarty: 0.5\n"), 0o600)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected strict YAML to reject unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "weight_similarty") {
		t.Fatalf("error should name the offending key; got %q", err.Error())
	}
}

func TestLoadRejectsRemovedConceptsKey(t *testing.T) {
	// concepts.min_content_length_direct was declared but never read
	// (removed in #100). Per the config-drift precedent, removed keys
	// are not shimmed: strict decoding rejects them with the offending
	// name, the same loud-fail surface as typos, so operators delete
	// the stale line instead of trusting a knob that does nothing.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("concepts:\n  min_content_length_direct: 50\n"), 0o600)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected strict YAML to reject removed key, got nil")
	}
	if !strings.Contains(err.Error(), "min_content_length_direct") {
		t.Fatalf("error should name the offending key; got %q", err.Error())
	}
}

func TestLoadRejectsInvariantViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("llm:\n  provider: gemini\n"), 0o600)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected Validate to reject unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "llm.provider") {
		t.Fatalf("error should name the offending key; got %q", err.Error())
	}
}
