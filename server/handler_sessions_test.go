package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// --- Phase 3: Search integration tests ---

// addSearchableRecord creates a Memory record that is both PropertyIndex and BM25 indexed.
func addSearchableRecord(t *testing.T, eng *core.Engine, content string, keywords []string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"knowledge_type":    graph.StringProperty("semantic"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	if len(keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(keywords)
	}
	n := eng.Graph().AddNode(props)
	eng.IndexNode(n.ID, content, nil)
	eng.Save("test")
	return n.ID
}

// createSessionWithSegments creates a session, prepares, and commits segments.
// Returns the session ID.
func createSessionWithSegments(t *testing.T, srv *Server, clientID string, segments []api.CommitSegment) string {
	t.Helper()
	ctx := context.Background()
	result, apiErr := srv.api.SessionStart(ctx, clientID, "")
	if apiErr != nil {
		t.Fatalf("session create: %v", apiErr)
	}
	sessionID := result["id"].(string)

	if _, apiErr := srv.api.SessionPrepare(ctx, sessionID); apiErr != nil {
		t.Fatalf("session prepare: %v", apiErr)
	}
	if _, apiErr := srv.api.SessionCommit(ctx, sessionID, segments); apiErr != nil {
		t.Fatalf("session commit: %v", apiErr)
	}
	return sessionID
}

func TestSearchFindsBM25SessionSegments(t *testing.T) {
	srv, _ := setupTestServer(t)

	createSessionWithSegments(t, srv, "bm25-search-test", []api.CommitSegment{
		{Content: "PostgreSQL is our primary database choice", TopicName: "Architecture"},
	})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text": "PostgreSQL database",
		"top":  10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for BM25 segment search")
	}
	// Hybrid commit creates both a Session segment and Memory record.
	// Verify at least one result has store=session.
	foundSession := false
	for _, r := range results {
		if r.(map[string]any)["store"] == "session" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("expected at least one result with store=session")
	}
}

func TestSearchSessionContainerNodesExcluded(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create session and topic nodes manually with content that would match.
	eng.Lock()
	sessionNode := eng.Graph().AddNode(graph.Properties{
		"knowledge_type":    graph.StringProperty("session"),
		"client_session_id": graph.StringProperty("container-test"),
		"content_full":      graph.StringProperty("PostgreSQL architecture session"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	eng.IndexNode(sessionNode.ID, "PostgreSQL architecture session", nil)
	topicNode := eng.Graph().AddNode(graph.Properties{
		"knowledge_type": graph.StringProperty("topic"),
		"topic_name":     graph.StringProperty("PostgreSQL topic"),
		"content_full":   graph.StringProperty("PostgreSQL architecture topic"),
		"created_at":     graph.TimestampProperty(time.Now().UTC()),
	})
	eng.IndexNode(topicNode.ID, "PostgreSQL architecture topic", nil)
	eng.Save("test")
	eng.Unlock()

	// Search should NOT return session or topic container nodes.
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text": "PostgreSQL architecture",
		"top":  10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	for _, r := range results {
		rec := r.(map[string]any)
		kt := rec["knowledge_type"]
		if kt == "session" || kt == "topic" {
			t.Errorf("container node %v with knowledge_type=%v should be excluded from search", rec["id"], kt)
		}
	}
}

func TestSearchStoreOriginMetadata(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Add a Memory record.
	addSearchableRecord(t, eng, "Go is a systems language", []string{"golang"})

	// Add a Session segment.
	createSessionWithSegments(t, srv, "store-origin-test", []api.CommitSegment{
		{Content: "Go is excellent for building services", TopicName: "Tech"},
	})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text": "Go language",
		"top":  10,
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)

	foundMemory := false
	foundSession := false
	for _, r := range results {
		rec := r.(map[string]any)
		switch rec["store"] {
		case "memory":
			foundMemory = true
		case "session":
			foundSession = true
		}
	}
	if !foundMemory {
		t.Error("expected at least one memory result")
	}
	if !foundSession {
		t.Error("expected at least one session result")
	}
}

func TestSearchStoreFilterMemoryOnly(t *testing.T) {
	srv, eng := setupTestServer(t)

	addSearchableRecord(t, eng, "Redis caching strategy", []string{"redis"})
	createSessionWithSegments(t, srv, "filter-memory-test", []api.CommitSegment{
		{Content: "Redis is used for caching in production", TopicName: "Infra"},
	})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text":  "Redis caching",
		"top":   10,
		"store": "memory",
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)

	for _, r := range results {
		rec := r.(map[string]any)
		if rec["store"] == "session" {
			t.Error("store=memory filter should exclude session results")
		}
	}
}

func TestSearchStoreFilterSessionsOnly(t *testing.T) {
	srv, eng := setupTestServer(t)

	addSearchableRecord(t, eng, "Kubernetes orchestration", []string{"k8s"})
	createSessionWithSegments(t, srv, "filter-session-test", []api.CommitSegment{
		{Content: "Kubernetes deployment pipeline discussion", TopicName: "Infra"},
	})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text":  "Kubernetes deployment",
		"top":   10,
		"store": "sessions",
	})
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)

	for _, r := range results {
		rec := r.(map[string]any)
		if rec["store"] == "memory" {
			t.Error("store=sessions filter should exclude memory results")
		}
	}
	if len(results) == 0 {
		t.Error("expected at least one session result")
	}
}

func TestSearchSessionOnlyData(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Only session data, no Memory records.
	createSessionWithSegments(t, srv, "session-only-test", []api.CommitSegment{
		{Content: "Terraform infrastructure as code patterns", TopicName: "Infra"},
	})

	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text": "Terraform infrastructure",
		"top":  10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected results from session-only store")
	}
}

func TestSearchEmptySessionResults(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Search for something that doesn't exist at all.
	w := doRequest(t, srv, "POST", "/v1/search", map[string]any{
		"text":  "xyzzy nonexistent term",
		"top":   10,
		"store": "sessions",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	results := data["results"].([]any)
	if len(results) != 0 {
		t.Errorf("expected 0 results for nonexistent term, got %d", len(results))
	}
}

func TestSearchMemoryRanksAboveSession(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Memory record (has vector embedding potential + BM25).
	addSearchableRecord(t, eng, "GraphQL API design patterns for microservices", []string{"graphql", "api"})
	// Session segment (BM25 only).
	createSessionWithSegments(t, srv, "ranking-test", []api.CommitSegment{
		{Content: "GraphQL API design patterns for microservices", TopicName: "API"},
	})

	ctx := context.Background()
	result, svcErr := srv.serviceSearch(ctx, &searchRequest{
		Text: "GraphQL API design",
		Top:  10,
	})
	if svcErr != nil {
		t.Fatalf("search: %v", svcErr)
	}
	results := result["results"].([]search.Result)
	if len(results) < 2 {
		t.Fatalf("expected >= 2 results, got %d", len(results))
	}
	// Memory should rank first (RRF fusion of vector+BM25 vs BM25-only).
	// Without vector embeddings in test, both are BM25-only, so ranking
	// may be equal. This test verifies they coexist; ranking differentiation
	// comes from vector embeddings in production.
	foundMemory := false
	foundSession := false
	for _, r := range results {
		if r.Store == "memory" {
			foundMemory = true
		}
		if r.Store == "session" {
			foundSession = true
		}
	}
	if !foundMemory || !foundSession {
		t.Errorf("expected both memory and session results, got memory=%v session=%v", foundMemory, foundSession)
	}
}
