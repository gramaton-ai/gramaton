package curation

import (
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
)

func setupEngine(t *testing.T) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	return eng
}

func addNode(t *testing.T, eng *core.Engine, content, temporality string, conf float64, keywords []string, createdAt time.Time) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"temporality":       graph.StringProperty(temporality),
		"confidence":        graph.Float64Property(conf),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(createdAt),
		"access_count":      graph.Int64Property(0),
	}
	if len(keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(keywords)
	}

	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

func TestDeterministicLifecycleTransitions(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	// Create an ephemeral record from a long time ago (should be stale).
	addNode(t, eng, "Old ephemeral", "ephemeral", 0.9, nil,
		now.Add(-30*24*time.Hour))

	// Create a durable record (should NOT be expired).
	addNode(t, eng, "Durable record", "durable", 0.9, nil, now)

	result := RunDeterministic(eng, cfg, nil)

	if result.LifecycleTransitions < 1 {
		t.Fatalf("expected at least 1 lifecycle transition, got %d", result.LifecycleTransitions)
	}

	// Verify valid_until was set on the ephemeral record.
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		temp, _ := n.Properties.GetString("temporality")
		if temp == "ephemeral" {
			if _, ok := n.Properties.GetTimestamp("valid_until"); !ok {
				t.Fatal("ephemeral record should have valid_until set")
			}
		}
		if temp == "durable" {
			if _, ok := n.Properties.GetTimestamp("valid_until"); ok {
				t.Fatal("durable record should NOT have valid_until set")
			}
		}
	}
}

func TestDeterministicConceptCandidates(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()

	// Create 3 records sharing the keyword "kafka" (should trigger candidate).
	addNode(t, eng, "Kafka record 1", "durable", 0.9, []string{"kafka", "events"}, now)
	addNode(t, eng, "Kafka record 2", "durable", 0.8, []string{"kafka", "pipeline"}, now)
	addNode(t, eng, "Kafka record 3", "durable", 0.7, []string{"kafka", "streaming"}, now)

	// Create 2 records sharing "redis" (should NOT trigger, below threshold).
	addNode(t, eng, "Redis record 1", "durable", 0.9, []string{"redis", "cache"}, now)
	addNode(t, eng, "Redis record 2", "durable", 0.8, []string{"redis", "session"}, now)

	result := RunDeterministic(eng, cfg, nil)

	// Find kafka candidate.
	found := false
	for _, c := range result.ConceptCandidates {
		if c.Keyword == "kafka" {
			found = true
			if c.Count != 3 {
				t.Fatalf("expected kafka count 3, got %d", c.Count)
			}
			if len(c.NodeIDs) != 3 {
				t.Fatalf("expected 3 node IDs, got %d", len(c.NodeIDs))
			}
		}
		if c.Keyword == "redis" {
			t.Fatal("redis should not be a candidate (only 2 records)")
		}
	}
	if !found {
		t.Fatal("kafka should be a concept candidate")
	}
}

func TestDeterministicManifest(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	addNode(t, eng, "Record 1", "durable", 0.9, nil, now)
	addNode(t, eng, "Record 2", "temporal", 0.7, nil, now)

	// Add a pending record.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Pending record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)
	m := result.Manifest

	if m == nil {
		t.Fatal("manifest should not be nil")
	}
	if m.TotalRecords != 3 {
		t.Fatalf("expected 3 total records, got %d", m.TotalRecords)
	}
	if m.PendingCount != 1 {
		t.Fatalf("expected 1 pending, got %d", m.PendingCount)
	}
}

func TestParseClassification(t *testing.T) {
	input := `{
		"temporality": "durable",
		"confidence": 0.85,
		"knowledge_type": "semantic",
		"epistemic_status": "probable",
		"keywords": ["auth", "security"],
		"summary_short": "Authentication security practices"
	}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Temporality != "durable" {
		t.Fatalf("expected durable, got %q", result.Temporality)
	}
	if result.Confidence != 0.85 {
		t.Fatalf("expected 0.85, got %f", result.Confidence)
	}
	if len(result.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(result.Keywords))
	}
}

func TestParseClassificationWithCodeFences(t *testing.T) {
	input := "Here's the classification:\n```json\n" +
		`{"temporality":"temporal","confidence":0.7,"knowledge_type":"episodic","keywords":["test"],"summary_short":"Test"}` +
		"\n```\n"

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Temporality != "temporal" {
		t.Fatalf("expected temporal, got %q", result.Temporality)
	}
}

func TestParseClassificationInvalidEnum(t *testing.T) {
	input := `{"temporality":"invalid_value","confidence":0.5,"knowledge_type":"wrong"}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	// Invalid enums should be empty-stringed.
	if result.Temporality != "" {
		t.Fatalf("invalid temporality should be empty, got %q", result.Temporality)
	}
	if result.KnowledgeType != "" {
		t.Fatalf("invalid knowledge_type should be empty, got %q", result.KnowledgeType)
	}
}

func TestParseClassificationConfidenceClamp(t *testing.T) {
	input := `{"temporality":"durable","confidence":5.0}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should be clamped to 0.5, got %f", result.Confidence)
	}
}

func TestParseClassificationKeywordLimits(t *testing.T) {
	// Test that keywords are capped at 100.
	parts := make([]string, 150)
	for i := range parts {
		parts[i] = `"keyword"`
	}
	jsonStr := `{"keywords":[` + joinStrings(parts) + `]}`

	result, err := parseClassification(jsonStr)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if len(result.Keywords) > 100 {
		t.Fatalf("keywords should be capped at 100, got %d", len(result.Keywords))
	}
}

func joinStrings(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ","
		}
		result += v
	}
	return result
}

func TestParseClassificationSummaryTruncation(t *testing.T) {
	longSummary := ""
	for i := 0; i < 300; i++ {
		longSummary += "x"
	}
	input := `{"summary_short":"` + longSummary + `"}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if len([]rune(result.SummaryShort)) > 200 {
		t.Fatalf("summary should be truncated to 200 runes, got %d", len([]rune(result.SummaryShort)))
	}
}
