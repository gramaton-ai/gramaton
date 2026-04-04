package search

import (
	"context"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
)

// mockEmbedder returns pre-configured embeddings.
type mockEmbedder struct {
	vectors map[string][]float32
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		if v, ok := m.vectors[text]; ok {
			result[i] = v
		} else {
			result[i] = []float32{0, 0, 0}
		}
	}
	return result, nil
}

// setupTestGraph creates a graph with several knowledge records for testing.
func setupTestGraph() (*graph.Graph, *index.PropertyIndex, index.VectorIndex) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()
	now := time.Now().UTC()

	// Record 1: High-confidence durable record about Kafka.
	n1 := g.AddNode(graph.Properties{
		"content_full":     graph.StringProperty("We chose Kafka over RabbitMQ for the event pipeline"),
		"content_short":    graph.StringProperty("Chose Kafka for event pipeline"),
		"content_keywords": graph.StringListProperty([]string{"kafka", "rabbitmq", "event-pipeline"}),
		"temporality":      graph.StringProperty("durable"),
		"confidence":       graph.Float64Property(0.9),
		"knowledge_type":   graph.StringProperty("episodic"),
		"epistemic_status": graph.StringProperty("well_established"),
		"created_at":       graph.TimestampProperty(now.Add(-48 * time.Hour)),
		"access_count":     graph.Int64Property(5),
		"last_accessed":    graph.TimestampProperty(now.Add(-2 * time.Hour)),
	})

	// Record 2: Low-confidence speculative record.
	n2 := g.AddNode(graph.Properties{
		"content_full":     graph.StringProperty("Maybe we should try Pulsar instead"),
		"content_short":    graph.StringProperty("Consider Pulsar as alternative"),
		"content_keywords": graph.StringListProperty([]string{"pulsar", "event-pipeline"}),
		"temporality":      graph.StringProperty("temporal"),
		"confidence":       graph.Float64Property(0.3),
		"knowledge_type":   graph.StringProperty("episodic"),
		"epistemic_status": graph.StringProperty("speculative"),
		"created_at":       graph.TimestampProperty(now.Add(-24 * time.Hour)),
		"access_count":     graph.Int64Property(1),
		"last_accessed":    graph.TimestampProperty(now.Add(-12 * time.Hour)),
	})

	// Record 3: Immutable reference record.
	n3 := g.AddNode(graph.Properties{
		"content_full":     graph.StringProperty("HTTP 200 means success per RFC 7231"),
		"content_short":    graph.StringProperty("HTTP 200 = success"),
		"content_keywords": graph.StringListProperty([]string{"http", "status-code", "rfc-7231"}),
		"temporality":      graph.StringProperty("immutable"),
		"confidence":       graph.Float64Property(0.99),
		"knowledge_type":   graph.StringProperty("reference"),
		"epistemic_status": graph.StringProperty("well_established"),
		"created_at":       graph.TimestampProperty(now.Add(-365 * 24 * time.Hour)),
		"access_count":     graph.Int64Property(20),
		"last_accessed":    graph.TimestampProperty(now.Add(-1 * time.Hour)),
	})

	// Record 4: Historical (superseded) record.
	n4 := g.AddNode(graph.Properties{
		"content_full":     graph.StringProperty("We use Redis for caching"),
		"content_short":    graph.StringProperty("Redis caching strategy"),
		"content_keywords": graph.StringListProperty([]string{"redis", "caching"}),
		"temporality":      graph.StringProperty("durable"),
		"confidence":       graph.Float64Property(0.3),
		"knowledge_type":   graph.StringProperty("episodic"),
		"epistemic_status": graph.StringProperty("well_established"),
		"created_at":       graph.TimestampProperty(now.Add(-180 * 24 * time.Hour)),
		"valid_until":      graph.TimestampProperty(now.Add(-30 * 24 * time.Hour)),
		"access_count":     graph.Int64Property(2),
	})

	// Index all properties.
	for _, n := range []*graph.Node{n1, n2, n3, n4} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
	}

	// Add vectors for similarity search.
	vecIdx.Add(n1.ID, []float32{0.9, 0.1, 0.0}) // "kafka-like"
	vecIdx.Add(n2.ID, []float32{0.7, 0.3, 0.0}) // somewhat similar
	vecIdx.Add(n3.ID, []float32{0.0, 0.0, 1.0}) // completely different topic
	vecIdx.Add(n4.ID, []float32{0.1, 0.8, 0.1}) // caching topic

	return g, propIdx, vecIdx
}

func TestSearchNoFilters(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
}

func TestSearchFilterByTemporality(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Temporality: "durable",
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// n1 (durable) and n4 (durable, historical)
	if len(results) != 2 {
		t.Fatalf("expected 2 durable results, got %d", len(results))
	}
	for _, r := range results {
		if r.Temporality != "durable" {
			t.Fatalf("expected durable, got %q", r.Temporality)
		}
	}
}

func TestSearchFilterByConfidenceMin(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	min := 0.5
	results, err := tool.Execute(context.Background(), Query{
		ConfidenceMin: &min,
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// n1 (0.9), n3 (0.99)
	if len(results) != 2 {
		t.Fatalf("expected 2 results with confidence >= 0.5, got %d", len(results))
	}
	for _, r := range results {
		if r.Confidence < 0.5 {
			t.Fatalf("confidence %f below minimum", r.Confidence)
		}
	}
}

func TestSearchFilterByKnowledgeType(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		KnowledgeType: "reference",
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 reference result, got %d", len(results))
	}
	if results[0].KnowledgeType != "reference" {
		t.Fatalf("expected reference, got %q", results[0].KnowledgeType)
	}
}

func TestSearchCombinedFilters(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	min := 0.5
	results, err := tool.Execute(context.Background(), Query{
		Temporality:   "durable",
		ConfidenceMin: &min,
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Only n1: durable + confidence >= 0.5
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchWithVectorSimilarity(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	emb := &mockEmbedder{
		vectors: map[string][]float32{
			"kafka event pipeline": {0.95, 0.05, 0.0}, // close to n1
		},
	}
	tool := New(g, propIdx, vecIdx, emb, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Text: "kafka event pipeline",
		Top:  4,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// n1 should rank first due to high similarity + high confidence.
	if results[0].SummaryShort != "Chose Kafka for event pipeline" {
		t.Fatalf("expected Kafka record first, got %q", results[0].SummaryShort)
	}
}

func TestSearchTopK(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 2})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 (top-k), got %d", len(results))
	}
}

func TestSearchDefaultTop(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Default top is 10, we only have 4 records.
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
}

func TestSearchResultsDescendingScore(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for i := 1; i < len(results); i++ {
		if results[i].EffectiveScore > results[i-1].EffectiveScore {
			t.Fatalf("results not in descending order at index %d", i)
		}
	}
}

func TestSearchHistoricalPenalty(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// n4 is historical (valid_until in the past). It should score lower
	// than it would if it were current. Find it and check its metadata summary.
	for _, r := range results {
		if r.SummaryShort == "Redis caching strategy" {
			if r.MetadataSummary == "" {
				t.Fatal("expected metadata summary")
			}
			// Historical records should have "Historical" in summary.
			return
		}
	}
}

func TestSearchMetadataSummary(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, r := range results {
		if r.MetadataSummary == "" {
			t.Fatalf("result %s has empty metadata summary", r.ID)
		}
	}
}

func TestSearchNoEmbedder(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg()) // nil embedder

	// Text query without embedder: should still work (no vector ranking).
	results, err := tool.Execute(context.Background(), Query{
		Text: "kafka",
		Top:  10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results (all, no filtering by text without embedder), got %d", len(results))
	}
}

func TestSearchEmptyGraph(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()
	tool := New(g, propIdx, vecIdx, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty graph, got %d", len(results))
	}
}
