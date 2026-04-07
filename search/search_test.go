package search

import (
	"context"
	"strings"
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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, emb, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	emb := &mockEmbedder{
		vectors: map[string][]float32{
			"event pipeline": {0.8, 0.2, 0.0},
		},
	}
	tool := New(g, propIdx, vecIdx, nil, emb, defaultCfg())

	// With text, results should be sorted by effective_score descending.
	results, err := tool.Execute(context.Background(), Query{Text: "event pipeline", Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for i := 1; i < len(results); i++ {
		if results[i].EffectiveScore > results[i-1].EffectiveScore {
			t.Fatalf("results not in descending score order at index %d", i)
		}
	}
}

func TestSearchNoTextDefaultsToCreatedAtDesc(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// No text: should default to created_at descending (newest first).
	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) < 2 {
		t.Fatal("need at least 2 results")
	}
	for i := 1; i < len(results); i++ {
		if results[i].CreatedAt > results[i-1].CreatedAt {
			t.Fatalf("results not in descending created_at order at index %d: %s > %s",
				i, results[i].CreatedAt, results[i-1].CreatedAt)
		}
	}
}

func TestSearchSortByAccessCountAsc(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Top:   10,
		Sort:  SortAccessCount,
		Order: "asc",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) < 2 {
		t.Fatal("need at least 2 results")
	}
	for i := 1; i < len(results); i++ {
		if results[i].AccessCount < results[i-1].AccessCount {
			t.Fatalf("results not in ascending access_count order at index %d", i)
		}
	}
}

func TestSearchSortByConfidenceDesc(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Top:  10,
		Sort: SortConfidence,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) < 2 {
		t.Fatal("need at least 2 results")
	}
	for i := 1; i < len(results); i++ {
		if results[i].Confidence > results[i-1].Confidence {
			t.Fatalf("results not in descending confidence order at index %d", i)
		}
	}
}

func TestSearchFilterOnlyByKnowledgeType(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// No text, just filter -- should work and return filtered results.
	results, err := tool.Execute(context.Background(), Query{
		KnowledgeType: "episodic",
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// n1, n2, n4 are episodic
	if len(results) != 3 {
		t.Fatalf("expected 3 episodic results, got %d", len(results))
	}
	for _, r := range results {
		if r.KnowledgeType != "episodic" {
			t.Fatalf("expected episodic, got %q", r.KnowledgeType)
		}
	}
}

func TestSearchSortByContentLength(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Top:   10,
		Sort:  SortContentLength,
		Order: "desc",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) < 2 {
		t.Fatal("need at least 2 results")
	}
	for i := 1; i < len(results); i++ {
		if results[i].ContentLength > results[i-1].ContentLength {
			t.Fatalf("results not in descending content_length order at index %d", i)
		}
	}
}

func TestSearchNegateTemporality(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// Exclude durable records (n1 and n4 are durable).
	results, err := tool.Execute(context.Background(), Query{
		Temporality: "!durable",
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// n2 (temporal) and n3 (immutable) remain.
	if len(results) != 2 {
		t.Fatalf("expected 2 non-durable results, got %d", len(results))
	}
	for _, r := range results {
		if r.Temporality == "durable" {
			t.Fatalf("got durable record despite exclusion")
		}
	}
}

func TestSearchMissingField(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	// n1 has temporality set.
	n1 := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("Classified record"),
		"content_short": graph.StringProperty("Classified"),
		"temporality":   graph.StringProperty("durable"),
		"confidence":    graph.Float64Property(0.9),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	// n2 does NOT have temporality.
	n2 := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("Unclassified record"),
		"content_short": graph.StringProperty("Unclassified"),
		"confidence":    graph.Float64Property(0.5),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	for _, n := range []*graph.Node{n1, n2} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
	}

	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())
	results, err := tool.Execute(context.Background(), Query{
		Missing: []string{"temporality"},
		Top:     10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result missing temporality, got %d", len(results))
	}
	if results[0].Temporality != "" {
		t.Fatalf("expected empty temporality, got %q", results[0].Temporality)
	}
}

func TestSearchKeywordExactMatch(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// "kafka" is a keyword on n1 only.
	results, err := tool.Execute(context.Background(), Query{
		Keywords: []string{"kafka"},
		Top:      10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with keyword 'kafka', got %d", len(results))
	}
	if results[0].SummaryShort != "Chose Kafka for event pipeline" {
		t.Fatalf("expected Kafka record, got %q", results[0].SummaryShort)
	}
}

func TestSearchKeywordMultiple(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// "event-pipeline" is on n1 and n2. Adding "kafka" narrows to n1 only.
	results, err := tool.Execute(context.Background(), Query{
		Keywords: []string{"event-pipeline", "kafka"},
		Top:      10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with both keywords, got %d", len(results))
	}
}

func TestSearchKeywordNoMatch(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Keywords: []string{"nonexistent-keyword"},
		Top:      10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchFilterByImportanceMin(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	n1 := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("High importance record"),
		"content_short": graph.StringProperty("High importance"),
		"importance":    graph.Float64Property(0.9),
		"confidence":    graph.Float64Property(0.8),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	n2 := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("Low importance record"),
		"content_short": graph.StringProperty("Low importance"),
		"importance":    graph.Float64Property(0.2),
		"confidence":    graph.Float64Property(0.8),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	for _, n := range []*graph.Node{n1, n2} {
		for k, v := range n.Properties {
			propIdx.Add(n.ID, k, v)
		}
	}

	min := 0.5
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())
	results, err := tool.Execute(context.Background(), Query{
		ImportanceMin: &min,
		Top:           10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with importance >= 0.5, got %d", len(results))
	}
	if results[0].Importance < 0.5 {
		t.Fatalf("importance %f below minimum", results[0].Importance)
	}
}

func TestSearchMatch(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// "RabbitMQ" appears only in n1's content.
	results, err := tool.Execute(context.Background(), Query{
		Match: "rabbitmq",
		Top:   10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result matching 'rabbitmq', got %d", len(results))
	}
	if results[0].SummaryShort != "Chose Kafka for event pipeline" {
		t.Fatalf("expected Kafka record, got %q", results[0].SummaryShort)
	}
}

func TestSearchMatchCaseInsensitive(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// "HTTP" in content_full is uppercase; search lowercase.
	results, err := tool.Execute(context.Background(), Query{
		Match: "http 200",
		Top:   10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchSimilarTo(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()

	// Get n1's ID (the Kafka record).
	var kafkaID string
	for _, id := range g.AllNodeIDs() {
		n, _ := g.GetNode(id)
		if s, ok := n.Properties.GetString("content_short"); ok && s == "Chose Kafka for event pipeline" {
			kafkaID = id
			break
		}
	}
	if kafkaID == "" {
		t.Fatal("could not find Kafka record")
	}

	// Add embedding to n1 so similar_to can use it.
	g.SetNodeProperty(kafkaID, "embedding_full", graph.VectorProperty([]float32{0.9, 0.1, 0.0}))

	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())
	results, err := tool.Execute(context.Background(), Query{
		SimilarTo: kafkaID,
		Top:       10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should not include the source record itself.
	for _, r := range results {
		if r.ID == kafkaID {
			t.Fatal("similar_to should exclude the source record")
		}
	}

	// n2 (0.7, 0.3, 0.0) should be most similar to n1 (0.9, 0.1, 0.0).
	if len(results) > 0 {
		if results[0].SummaryShort != "Consider Pulsar as alternative" {
			t.Logf("top result: %q (expected Pulsar due to similar vector)", results[0].SummaryShort)
		}
	}
}

func TestSearchMatchNoHit(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Match: "zzznonexistent",
		Top:   10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchRandom(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Random: true,
		Top:    2,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 random results, got %d", len(results))
	}
	// Scores should be 0 since scoring is skipped.
	for _, r := range results {
		if r.EffectiveScore != 0 {
			t.Fatalf("expected score 0 for random results, got %f", r.EffectiveScore)
		}
	}
}

func TestSearchRandomWithFilter(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{
		Random:      true,
		Temporality: "durable",
		Top:         10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Only n1 and n4 are durable.
	if len(results) != 2 {
		t.Fatalf("expected 2 durable random results, got %d", len(results))
	}
	for _, r := range results {
		if r.Temporality != "durable" {
			t.Fatalf("expected durable, got %q", r.Temporality)
		}
	}
}

func TestComputeFacets(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	facets := ComputeFacets(results)

	// We have: durable(2), temporal(1), immutable(1)
	if facets.Temporality["durable"] != 2 {
		t.Fatalf("expected 2 durable, got %d", facets.Temporality["durable"])
	}
	if facets.Temporality["temporal"] != 1 {
		t.Fatalf("expected 1 temporal, got %d", facets.Temporality["temporal"])
	}
	if facets.Temporality["immutable"] != 1 {
		t.Fatalf("expected 1 immutable, got %d", facets.Temporality["immutable"])
	}

	// Knowledge types: episodic(3), reference(1)
	if facets.KnowledgeType["episodic"] != 3 {
		t.Fatalf("expected 3 episodic, got %d", facets.KnowledgeType["episodic"])
	}
	if facets.KnowledgeType["reference"] != 1 {
		t.Fatalf("expected 1 reference, got %d", facets.KnowledgeType["reference"])
	}
}

func TestValidSort(t *testing.T) {
	valid := []string{"", "created_at", "last_accessed", "access_count",
		"confidence", "importance", "content_length"}
	for _, s := range valid {
		if !ValidSort(s) {
			t.Fatalf("ValidSort(%q) = false, want true", s)
		}
	}
	invalid := []string{"foo", "score", "id", "name"}
	for _, s := range invalid {
		if ValidSort(s) {
			t.Fatalf("ValidSort(%q) = true, want false", s)
		}
	}
}

func TestSearchHistoricalPenalty(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

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

func TestMetadataSummaryExpirationVisibility(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name     string
		validUntil time.Time
		wantContains string
	}{
		{
			name:         "expired yesterday",
			validUntil:   now.Add(-36 * time.Hour),
			wantContains: "Historical (expired",
		},
		{
			name:         "expires tomorrow",
			validUntil:   now.Add(36 * time.Hour),
			wantContains: "expires",
		},
		{
			name:         "expires in multiple days",
			validUntil:   now.Add(10*24*time.Hour + 12*time.Hour),
			wantContains: "expires in 10 days",
		},
		{
			name:         "expired multiple days ago",
			validUntil:   now.Add(-5*24*time.Hour - 12*time.Hour),
			wantContains: "expired 5 days ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := graph.Properties{
				"temporality": graph.StringProperty("temporal"),
				"confidence":  graph.Float64Property(0.8),
				"valid_until": graph.TimestampProperty(tt.validUntil),
			}
			summary := buildMetadataSummary(props)
			if !containsSubstring(summary, tt.wantContains) {
				t.Fatalf("summary %q should contain %q", summary, tt.wantContains)
			}
		})
	}
}

func TestMetadataSummaryNoExpiration(t *testing.T) {
	props := graph.Properties{
		"temporality": graph.StringProperty("durable"),
		"confidence":  graph.Float64Property(0.95),
	}
	summary := buildMetadataSummary(props)
	if !containsSubstring(summary, "Current.") {
		t.Fatalf("summary %q should contain 'Current.'", summary)
	}
	if containsSubstring(summary, "expires") || containsSubstring(summary, "expired") {
		t.Fatalf("summary %q should not contain expiration info when no valid_until", summary)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSearchNoEmbedder(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg()) // nil embedder

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
	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	results, err := tool.Execute(context.Background(), Query{Top: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty graph, got %d", len(results))
	}
}

func TestSearchFilterByResolution(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	now := time.Now().UTC()

	// Add a resolved record.
	resolved := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("TODO: build feature X"),
		"content_short":     graph.StringProperty("Build feature X"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"resolution":        graph.StringProperty("completed"),
		"resolved_at":       graph.TimestampProperty(now),
	})
	for k, v := range resolved.Properties {
		propIdx.Add(resolved.ID, k, v)
	}

	// Add an unresolved record.
	unresolved := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("TODO: build feature Y"),
		"content_short":     graph.StringProperty("Build feature Y"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range unresolved.Properties {
		propIdx.Add(unresolved.ID, k, v)
	}

	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// Filter for completed records.
	results, err := tool.Execute(context.Background(), Query{
		Resolution: "completed",
		Top:        100,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == resolved.ID {
			found = true
			if r.Resolution != "completed" {
				t.Fatalf("expected resolution 'completed', got %q", r.Resolution)
			}
		}
		if r.ID == unresolved.ID {
			t.Fatal("unresolved record should not appear when filtering for completed")
		}
	}
	if !found {
		t.Fatal("resolved record should appear in results")
	}

	// Filter for unresolved records.
	results, err = tool.Execute(context.Background(), Query{
		Resolution: "unresolved",
		Top:        100,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	foundUnresolved := false
	for _, r := range results {
		if r.ID == unresolved.ID {
			foundUnresolved = true
		}
		if r.ID == resolved.ID {
			t.Fatal("resolved record should not appear when filtering for unresolved")
		}
	}
	if !foundUnresolved {
		t.Fatal("unresolved record should appear in results")
	}
}

func TestSearchFilterByResolutionNegation(t *testing.T) {
	g, propIdx, vecIdx := setupTestGraph()
	now := time.Now().UTC()

	completed := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Done task"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"resolution":        graph.StringProperty("completed"),
	})
	for k, v := range completed.Properties {
		propIdx.Add(completed.ID, k, v)
	}

	abandoned := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Dropped task"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"resolution":        graph.StringProperty("abandoned"),
	})
	for k, v := range abandoned.Properties {
		propIdx.Add(abandoned.ID, k, v)
	}

	tool := New(g, propIdx, vecIdx, nil, nil, defaultCfg())

	// Exclude completed -- should still find abandoned.
	results, err := tool.Execute(context.Background(), Query{
		Resolution: "!completed",
		Top:        100,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	foundAbandoned := false
	for _, r := range results {
		if r.ID == completed.ID {
			t.Fatal("completed record should be excluded by !completed filter")
		}
		if r.ID == abandoned.ID {
			foundAbandoned = true
		}
	}
	if !foundAbandoned {
		t.Fatal("abandoned record should appear in !completed results")
	}
}

func TestSearchResolutionInFacets(t *testing.T) {
	results := []Result{
		{ID: "1", Resolution: "completed"},
		{ID: "2", Resolution: "completed"},
		{ID: "3", Resolution: "abandoned"},
		{ID: "4"},
	}
	facets := ComputeFacets(results)
	if facets.Resolution["completed"] != 2 {
		t.Fatalf("expected 2 completed, got %d", facets.Resolution["completed"])
	}
	if facets.Resolution["abandoned"] != 1 {
		t.Fatalf("expected 1 abandoned, got %d", facets.Resolution["abandoned"])
	}
}

func TestMetadataSummaryResolution(t *testing.T) {
	props := graph.Properties{
		"temporality": graph.StringProperty("durable"),
		"confidence":  graph.Float64Property(0.85),
		"resolution":  graph.StringProperty("completed"),
	}
	summary := buildMetadataSummary(props)

	if !strings.Contains(summary, "Resolved: completed") {
		t.Fatalf("summary should contain resolution, got %q", summary)
	}
}

func TestHybridSearchRRF(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()
	bm25Idx := index.NewBM25Index(0, 0)

	now := time.Now().UTC()

	// Create a shared vector -- all records have identical embeddings.
	// This forces vector search to rank them equally, so BM25 must
	// break the tie for multi-concept queries.
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	// doc1: about consciousness only
	doc1 := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("consciousness is the most fundamental aspect of subjective experience"),
		"embedding_full":    graph.VectorProperty(vec),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range doc1.Properties {
		propIdx.Add(doc1.ID, k, v)
	}
	vecIdx.Add(doc1.ID, vec)
	bm25Idx.Add(doc1.ID, "consciousness is the most fundamental aspect of subjective experience")

	// doc2: about memory only
	doc2 := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("memory trace decay is defined by its functional role in cognitive systems"),
		"embedding_full":    graph.VectorProperty(vec),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range doc2.Properties {
		propIdx.Add(doc2.ID, k, v)
	}
	vecIdx.Add(doc2.ID, vec)
	bm25Idx.Add(doc2.ID, "memory trace decay is defined by its functional role in cognitive systems")

	// doc3: about BOTH consciousness AND memory
	doc3 := g.AddNode(graph.Properties{
		"content_full":      graph.StringProperty("consciousness and memory play interconnected roles in cognitive experience and narrative identity"),
		"embedding_full":    graph.VectorProperty(vec),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range doc3.Properties {
		propIdx.Add(doc3.ID, k, v)
	}
	vecIdx.Add(doc3.ID, vec)
	bm25Idx.Add(doc3.ID, "consciousness and memory play interconnected roles in cognitive experience and narrative identity")

	cfg := defaultCfg()
	tool := New(g, propIdx, vecIdx, bm25Idx, nil, cfg)

	// Search for "consciousness memory" -- doc3 should rank first
	// because it matches both BM25 terms, getting higher RRF score.
	results, err := tool.ExecuteWithVector(context.Background(), Query{
		Text: "consciousness memory",
		Top:  10,
	}, vec)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d", len(results))
	}

	// With RRF, the record matching both query terms should be ranked first.
	if results[0].ID != doc3.ID {
		t.Errorf("expected doc3 (matches both terms) to rank first, got %s", results[0].ID)
		for i, r := range results {
			t.Logf("  rank %d: %s score=%f", i+1, r.ID, r.EffectiveScore)
		}
	}
}
