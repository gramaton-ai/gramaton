package config

import (
	"os"
	"path/filepath"
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
	if cfg.Dedup.Action != "flag" {
		t.Fatalf("dedup.action: expected 'flag', got %q", cfg.Dedup.Action)
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
	if cfg.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("expected claude-sonnet-4-6, got %q", cfg.LLM.Model)
	}

	// LLM Curation.
	if cfg.LLMCuration.BatchSize != 10 {
		t.Fatalf("expected batch 10, got %d", cfg.LLMCuration.BatchSize)
	}
	if cfg.LLMCuration.MaxCallsPerRun != 20 {
		t.Fatalf("expected 20 max calls, got %d", cfg.LLMCuration.MaxCallsPerRun)
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
llm_curation:
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

	if cfg.LLMCuration.MaxCallsPerRun > 10000 {
		t.Fatalf("MaxCallsPerRun should be capped at 10000, got %d", cfg.LLMCuration.MaxCallsPerRun)
	}
	if cfg.LLMCuration.BatchSize > 5000 {
		t.Fatalf("BatchSize should be capped at 5000, got %d", cfg.LLMCuration.BatchSize)
	}
	if cfg.Curation.MaxOrphansPerRun > 200 {
		t.Fatalf("MaxOrphansPerRun should be capped, got %d", cfg.Curation.MaxOrphansPerRun)
	}
	if cfg.Curation.MaxDedupPerRun > 200 {
		t.Fatalf("MaxDedupPerRun should be capped, got %d", cfg.Curation.MaxDedupPerRun)
	}
}

func TestLoadWithFallbackUsesStoreConfig(t *testing.T) {
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
	// Should use store config when it exists.
	if cfg.Scoring.WeightSimilarity != 0.77 {
		t.Fatalf("expected 0.77 (store), got %f", cfg.Scoring.WeightSimilarity)
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
	// Should fall back to global.
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
	// Should return defaults.
	defaults := Defaults()
	if cfg.Scoring.WeightSimilarity != defaults.Scoring.WeightSimilarity {
		t.Fatal("both missing should return defaults")
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
	cfg.LLMCuration.ContradictionEffort = "high"
	if got := cfg.EffortForTask(TaskContradiction); got != EffortHigh {
		t.Errorf("override effort for contradiction = %s, want high", got)
	}

	// Unknown override string falls back to default.
	cfg.LLMCuration.ConceptEffort = "extreme"
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
	cfg.LLMCuration.SummarizationEffort = "high"
	cfg.LLM.Models.High = "premium-summarizer"
	if got := cfg.ModelForTask(TaskSummarization); got != "premium-summarizer" {
		t.Errorf("summarization model after effort+tier override = %q, want premium-summarizer", got)
	}
}
