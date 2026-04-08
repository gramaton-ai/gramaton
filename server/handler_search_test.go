package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

func addRecordWithProps(t *testing.T, eng *core.Engine, content, temporality, knowledgeType string, confidence float64, keywords []string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty(temporality),
		"knowledge_type":    graph.StringProperty(knowledgeType),
		"confidence":        graph.Float64Property(confidence),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
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

func TestSearchFilterByTemporality(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "Durable one", "durable", "semantic", 0.9, nil)
	addRecordWithProps(t, eng, "Temporal one", "temporal", "semantic", 0.7, nil)
	addRecordWithProps(t, eng, "Ephemeral one", "ephemeral", "semantic", 0.5, nil)

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"temporality": "durable",
		"top":         10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 durable result, got %d", len(results))
	}
	rec := results[0].(map[string]any)
	if rec["temporality"] != "durable" {
		t.Fatalf("expected durable, got %v", rec["temporality"])
	}
}

func TestSearchFilterByKnowledgeType(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "Procedural", "durable", "procedural", 0.9, nil)
	addRecordWithProps(t, eng, "Semantic", "durable", "semantic", 0.9, nil)

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"knowledge_type": "procedural",
		"top":            10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 procedural, got %d", len(results))
	}
}

func TestSearchFilterByConfidenceRange(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "High conf", "durable", "semantic", 0.95, nil)
	addRecordWithProps(t, eng, "Low conf", "durable", "semantic", 0.3, nil)

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"confidence_min": 0.8,
		"top":            10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 high-confidence result, got %d", len(results))
	}
}

func TestSearchFilterByKeywords(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "About Kafka", "durable", "semantic", 0.9, []string{"kafka", "events"})
	addRecordWithProps(t, eng, "About Redis", "durable", "semantic", 0.9, []string{"redis", "cache"})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"keywords": []string{"kafka"},
		"top":      10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 kafka result, got %d", len(results))
	}
}

func TestSearchMatch(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "RWMutex is not reentrant", "durable", "semantic", 0.9, nil)
	addRecordWithProps(t, eng, "Something else entirely", "durable", "semantic", 0.9, nil)

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"match": "RWMutex",
		"top":   10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 match result, got %d", len(results))
	}
}

func TestSearchSortCreatedAt(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create records with staggered timestamps.
	eng.Lock()
	now := time.Now().UTC()
	for i, content := range []string{"Oldest", "Middle", "Newest"} {
		n := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty(content),
			"processing_status": graph.StringProperty("processed"),
			"temporality":       graph.StringProperty("durable"),
			"created_at":        graph.TimestampProperty(now.Add(time.Duration(i) * time.Hour)),
			"access_count":      graph.Int64Property(0),
		})
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
	}
	eng.Save("test")
	eng.Unlock()

	// Sort ascending.
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"sort":  "created_at",
		"order": "asc",
		"top":   10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// First should be oldest.
	first := results[0].(map[string]any)
	last := results[2].(map[string]any)
	if first["created_at"].(string) > last["created_at"].(string) {
		t.Fatal("expected ascending created_at order")
	}
}

func TestSearchSortConfidence(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecordWithProps(t, eng, "Low", "durable", "semantic", 0.3, nil)
	addRecordWithProps(t, eng, "High", "durable", "semantic", 0.95, nil)
	addRecordWithProps(t, eng, "Mid", "durable", "semantic", 0.6, nil)

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"sort":  "confidence",
		"order": "desc",
		"top":   10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d", len(results))
	}
	first := results[0].(map[string]any)
	if first["confidence"].(float64) != 0.95 {
		t.Fatalf("expected highest confidence first, got %v", first["confidence"])
	}
}

func TestSearchInvalidOrder(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"order": "invalid",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid order, got %d", w.Code)
	}
}

func TestSearchAccessCountFilter(t *testing.T) {
	srv, eng := setupTestServer(t)

	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Popular"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(10),
	})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Unpopular"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	min := int64(5)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"access_count_min": min,
		"top":              10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result with access_count >= 5, got %d", len(results))
	}
}

func TestSearchImportanceFilter(t *testing.T) {
	srv, eng := setupTestServer(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Important"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"importance":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Not important"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"importance":        graph.Float64Property(0.1),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"importance_min": 0.5,
		"top":            10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 important result, got %d", len(results))
	}
}

func TestSearchSince(t *testing.T) {
	srv, eng := setupTestServer(t)

	eng.Lock()
	// Old record.
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Old"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}
	// New record.
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("New"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"since": "2025-01-01",
		"top":   10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result since 2025, got %d", len(results))
	}
}

func TestSearchInvalidSinceDate(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"since": "not-a-date",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSearchMinMaxEdges(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecordWithProps(t, eng, "Connected", "durable", "semantic", 0.9, nil)
	addRecordWithProps(t, eng, "Orphan", "durable", "semantic", 0.9, nil)
	id3 := addRecordWithProps(t, eng, "Other", "durable", "semantic", 0.9, nil)

	// Create edge so id1 has 1 edge.
	eng.Lock()
	eng.Graph().AddEdge(id1, id3, "related_to", 0.5, nil)
	eng.Save("link")
	eng.Unlock()

	// Find orphans (max_edges=0).
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"max_edges": 0,
		"top":       10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	// Only the orphan should be returned.
	if len(results) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(results))
	}
}

func TestSearchSortEdgeCount(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecordWithProps(t, eng, "Many edges", "durable", "semantic", 0.9, nil)
	addRecordWithProps(t, eng, "No edges", "durable", "semantic", 0.9, nil)
	id3 := addRecordWithProps(t, eng, "Target", "durable", "semantic", 0.9, nil)

	eng.Lock()
	eng.Graph().AddEdge(id1, id3, "related_to", 0.5, nil)
	eng.Save("link")
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"sort":  "edge_count",
		"order": "desc",
		"top":   10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	first := results[0].(map[string]any)
	if first["edge_count"].(float64) < 1 {
		t.Fatal("highest edge_count should be first")
	}
}

func TestSearchTooManyMissing(t *testing.T) {
	srv, _ := setupTestServer(t)
	missing := make([]string, 100)
	for i := range missing {
		missing[i] = "field"
	}
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"missing": missing,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for too many missing fields, got %d", w.Code)
	}
}

func TestSearchNegativeAccessCount(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"access_count_min": -1,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative access_count, got %d", w.Code)
	}
}

func TestSearchNegativeEdges(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"min_edges": -5,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative min_edges, got %d", w.Code)
	}
}

func TestSearchImportanceOutOfRange(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"importance_min": 5.0,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for importance > 1, got %d", w.Code)
	}
}
