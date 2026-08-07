package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveProducesComments pins the contract that Save() embeds
// section banners + per-field descriptions in the rendered YAML.
// Catches drift if commentRegistry loses entries or the walker
// stops attaching HeadComments. Spot-checks a handful of strings
// across each major sub-section.
func TestSaveProducesComments(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := Save(Defaults(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	wants := []string{
		// Umbrella banner
		"LLM CONFIGURATION",
		"https://docs.anthropic.com/en/docs/about-claude/models",
		// Provider field with CLI-shim warning
		"provider: which LLM service to call.",
		"UNSUPPORTED. These shell out to the",
		"vendor's interactive CLI",
		// Section banners
		"RERANK -- search-time LLM reranking",
		"MODELS -- three tiers + which tier each task uses.",
		"COST LIMITS -- caps that apply to ALL LLM calls.",
		"CURATION KNOBS -- autonomous cleanup-cycle tuning.",
		"WARNING: these values control algorithmic behavior",
		// Per-task descriptions
		"classification_short: classify records below long_threshold",
		"contradiction: detect conflicts between similar",
		"rerank: reorder search candidates by relevance",
		"decompose: split complex search queries into sub-queries",
		// Sub-section banners under curation
		"contradiction: tuning for the contradiction detector",
		"concept: tuning for concept synthesis.",
		"retries: per-record / per-pair retry caps.",
	}

	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered config missing comment fragment: %q", w)
		}
	}
}

// TestSaveTasksDeterministicOrder pins that the llm.models.tasks
// keys render in the canonical sequence (classification_short ->
// classification_long -> summarization -> contradiction -> concept
// -> manifest -> rerank -> decompose). Without the post-encode
// reorder, yaml.v3 alphabetizes string-keyed maps and the order
// drifts (concept before contradiction, etc.) -- not wrong, but
// reads weird vs the docs and shifts on every save.
func TestSaveTasksDeterministicOrder(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := Save(Defaults(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Slice out the tasks block so we don't pick up the matching
	// `rerank:` key on the section header (llm.rerank, two scopes
	// up). Block runs from `tasks:` to the next sibling-or-shallower
	// key (cost_limits at indent 4).
	tasksStart := strings.Index(out, "        tasks:")
	if tasksStart < 0 {
		t.Fatalf("rendered config missing `tasks:` block")
	}
	rest := out[tasksStart:]
	tasksEnd := strings.Index(rest[1:], "    cost_limits:")
	if tasksEnd < 0 {
		t.Fatalf("rendered config missing `cost_limits:` after tasks block")
	}
	tasksBlock := rest[:tasksEnd]

	want := []string{
		"classification_short:",
		"classification_long:",
		"summarization:",
		"contradiction:",
		"concept:",
		"manifest:",
		"rerank:",
		"decompose:",
	}
	prev := -1
	for _, key := range want {
		idx := strings.Index(tasksBlock, key)
		if idx < 0 {
			t.Fatalf("tasks block missing key %q", key)
		}
		if idx <= prev {
			t.Fatalf("task key %q out of order in tasks block: index %d <= previous %d", key, idx, prev)
		}
		prev = idx
	}
}

// TestSaveRoundTripPreservesValues verifies that Save() (which now
// runs through renderConfig + yaml.Node) followed by Load() yields
// the same Config. Comments on the disk are dropped on parse, but
// values must round-trip exactly.
func TestSaveRoundTripPreservesValues(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	cfg := Defaults()
	cfg.LLM.Rerank.Enabled = true
	cfg.LLM.Models.Tasks["rerank"] = "high"
	cfg.LLM.CostLimits.MaxCallsPerDay = 1000
	cfg.LLM.Curation.Contradiction.MinSimilarity = 0.7
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.LLM.Rerank.Enabled {
		t.Error("Rerank.Enabled lost in round trip")
	}
	if loaded.LLM.Models.Tasks["rerank"] != "high" {
		t.Errorf("rerank tier override lost: %q", loaded.LLM.Models.Tasks["rerank"])
	}
	if loaded.LLM.CostLimits.MaxCallsPerDay != 1000 {
		t.Errorf("MaxCallsPerDay lost: %d", loaded.LLM.CostLimits.MaxCallsPerDay)
	}
	if loaded.LLM.Curation.Contradiction.MinSimilarity != 0.7 {
		t.Errorf("Contradiction.MinSimilarity lost: %f", loaded.LLM.Curation.Contradiction.MinSimilarity)
	}
}

// TestRegistryCoversAllLLMFields populates every optional/omitempty
// field under llm: with a non-zero value, then renders and verifies
// each registry path produces a matching key in the output. This
// catches drift in two directions: a registry entry that names a
// field that no longer exists (would not appear in output), and a
// new field added without a registry entry (separately checked by
// running gramaton-review on the diff).
func TestRegistryCoversAllLLMFields(t *testing.T) {
	cfg := Defaults()
	// Populate omitempty fields so they emit in the rendered output.
	cfg.LLM.APIKeyFile = "/path/to/key"
	cfg.LLM.APIKeyEnv = "ENV_NAME"
	cfg.LLM.APIKey = "secret"
	cfg.LLM.BaseURL = "https://example.com"
	cfg.LLM.Region = "us-west-2"
	cfg.LLM.AWSProfile = "default"
	cfg.LLM.AWSAccessKeyIDEnv = "AWS_AKID"
	cfg.LLM.AWSSecretAccessKeyEnv = "AWS_SAK"

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for path := range commentRegistry {
		if !strings.HasPrefix(path, "llm.") && path != "llm" {
			continue
		}
		segments := strings.Split(path, ".")
		key := segments[len(segments)-1]
		if !strings.Contains(out, key+":") {
			t.Errorf("registry path %q has no matching %q in output", path, key+":")
		}
	}
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
