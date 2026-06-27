package search

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// setupCombinedGraph creates a diverse set of records for combined filter testing.
func setupCombinedGraph() (*graph.Graph, index.PropertyIndex, index.VectorIndex) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()
	now := time.Now().UTC()

	records := []struct {
		content     string
		short       string
		temp        string
		kt          string
		es          string
		conf        float64
		imp         float64
		keywords    []string
		createdAgo  time.Duration
		accessCount int64
		vec         []float32
	}{
		{"Kafka event pipeline architecture", "Kafka pipeline", "durable", "semantic", "well_established", 0.95, 0.8, []string{"kafka", "events", "architecture"}, 48 * time.Hour, 10, []float32{0.9, 0.1, 0.0}},
		{"Redis caching strategy for sessions", "Redis caching", "durable", "procedural", "probable", 0.7, 0.5, []string{"redis", "cache", "sessions"}, 24 * time.Hour, 5, []float32{0.1, 0.9, 0.0}},
		{"Sprint planning notes from Friday", "Sprint notes", "temporal", "episodic", "well_established", 0.9, 0.3, []string{"sprint", "planning", "agile"}, 2 * time.Hour, 2, []float32{0.0, 0.1, 0.9}},
		{"OAuth 2.0 implementation guide", "OAuth guide", "immutable", "procedural", "well_established", 0.99, 0.9, []string{"auth", "oauth", "security"}, 365 * 24 * time.Hour, 50, []float32{0.5, 0.5, 0.0}},
		{"Speculative: try gRPC for internal APIs", "gRPC speculation", "temporal", "episodic", "speculative", 0.3, 0.2, []string{"grpc", "api", "speculation"}, 12 * time.Hour, 1, []float32{0.3, 0.3, 0.4}},
		{"Battery at 42% need to charge", "Battery status", "ephemeral", "episodic", "well_established", 1.0, 0.0, []string{"battery", "device"}, 1 * time.Hour, 0, []float32{0.0, 0.0, 0.1}},
	}

	for _, r := range records {
		props := graph.Properties{
			"content_full":     graph.StringProperty(r.content),
			"content_short":    graph.StringProperty(r.short),
			"temporality":      graph.StringProperty(r.temp),
			"knowledge_type":   graph.StringProperty(r.kt),
			"epistemic_status": graph.StringProperty(r.es),
			"confidence":       graph.Float64Property(r.conf),
			"importance":       graph.Float64Property(r.imp),
			"content_keywords": graph.StringListProperty(r.keywords),
			"created_at":       graph.TimestampProperty(now.Add(-r.createdAgo)),
			"access_count":     graph.Int64Property(r.accessCount),
			"last_accessed":    graph.TimestampProperty(now.Add(-r.createdAgo / 2)),
		}
		n := g.AddNode(props)
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
		vecIdx.Add(n.ID, r.vec)
	}

	return g, propIdx, vecIdx
}

func TestCombinedTemporalityAndConfidence(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	min := 0.8
	results, err := tool.Execute(context.Background(), Query{
		Temporality:   "durable",
		ConfidenceMin: &min,
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Kafka (durable, 0.95) only -- Redis is 0.7
	if len(results) != 1 {
		t.Fatalf("expected 1 result (durable + conf>=0.8), got %d", len(results))
	}
	if results[0].SummaryShort != "Kafka pipeline" {
		t.Fatalf("expected Kafka, got %q", results[0].SummaryShort)
	}
}

func TestCombinedNegationAndKeyword(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Temporality: "!ephemeral",
		Keywords:    []string{"auth"},
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// OAuth (immutable, has "auth") -- battery is ephemeral (excluded)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestCombinedMatchAndTemporality(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Match:       "caching",
		Temporality: "durable",
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 (Redis, durable + caching), got %d", len(results))
	}
}

func TestCombinedKnowledgeTypeAndSort(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		KnowledgeType: "procedural",
		Sort:          SortConfidence,
		Order:         "desc",
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// OAuth (0.99) and Redis (0.7) are procedural.
	if len(results) != 2 {
		t.Fatalf("expected 2 procedural, got %d", len(results))
	}
	if results[0].Confidence < results[1].Confidence {
		t.Fatal("should be sorted by confidence desc")
	}
}

func TestCombinedMissingAndKnowledgeType(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// All records have temporality, so missing=["temporality"] returns 0.
	results, err := tool.Execute(context.Background(), Query{
		Missing: []string{"temporality"},
		Top:     10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 (all have temporality), got %d", len(results))
	}
}

func TestCombinedAccessCountAndSort(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	min := int64(5)
	results, err := tool.Execute(context.Background(), Query{
		AccessCountMin: &min,
		Sort:           SortAccessCount,
		Order:          "desc",
		Top:            10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// OAuth (50), Kafka (10), Redis (5)
	if len(results) != 3 {
		t.Fatalf("expected 3 with access>=5, got %d", len(results))
	}
	if results[0].AccessCount < results[1].AccessCount {
		t.Fatal("should be sorted by access_count desc")
	}
}

func TestCombinedImportanceAndEpistemicStatus(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	min := 0.5
	results, err := tool.Execute(context.Background(), Query{
		EpistemicStatus: "well_established",
		ImportanceMin:   &min,
		Top:             10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Kafka (0.8, well_established) and OAuth (0.9, well_established)
	// Sprint (0.3) and Battery (0.0) have importance below 0.5
	if len(results) != 2 {
		t.Fatalf("expected 2, got %d", len(results))
	}
}

func TestCombinedTextAndFilters(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	emb := &mockEmbedder{
		vectors: map[string][]float32{
			"event streaming": {0.85, 0.15, 0.0},
		},
	}
	tool := New(g, propIdx, vecIdx, nil, emb, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Text:        "event streaming",
		Temporality: "durable",
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Should find durable records ranked by similarity to "event streaming".
	// Kafka (0.9,0.1,0.0) is most similar and is durable.
	if len(results) < 1 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].SummaryShort != "Kafka pipeline" {
		t.Fatalf("expected Kafka first, got %q", results[0].SummaryShort)
	}
}

func TestCombinedRandomWithFilter(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Random:      true,
		Temporality: "durable",
		Top:         2,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 random durable, got %d", len(results))
	}
	for _, r := range results {
		if r.Temporality != "durable" {
			t.Fatalf("random should respect filter, got %q", r.Temporality)
		}
	}
}

func TestContradictoryFiltersReturnEmpty(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// durable AND keyword=battery -- battery is ephemeral, not durable.
	results, err := tool.Execute(context.Background(), Query{
		Temporality: "durable",
		Keywords:    []string{"battery"},
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("contradictory filters should return 0, got %d", len(results))
	}
}

func TestAllFiltersZeroResults(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	min := 0.99
	results, err := tool.Execute(context.Background(), Query{
		Temporality:     "ephemeral",
		KnowledgeType:   "procedural",
		EpistemicStatus: "refuted",
		ConfidenceMin:   &min,
		Keywords:        []string{"nonexistent"},
		Top:             10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("impossible filter combo should return 0, got %d", len(results))
	}
}

func TestSortStaleness(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Sort:  SortStaleness,
		Order: "desc",
		Top:   10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) < 2 {
		t.Fatal("expected multiple results")
	}
	// Most stale should be first.
	if results[0].Staleness < results[len(results)-1].Staleness {
		t.Fatal("staleness should be descending")
	}
}

func TestSortContentLength(t *testing.T) {
	g, propIdx, vecIdx := setupCombinedGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Sort:  SortContentLength,
		Order: "desc",
		Top:   10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for i := 1; i < len(results); i++ {
		if results[i].ContentLength > results[i-1].ContentLength {
			t.Fatalf("content_length not descending at index %d", i)
		}
	}
}
