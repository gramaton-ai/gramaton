package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

func TestGuideReturnsTopicList(t *testing.T) {
	srv, _ := setupTestServer(t)

	resp, apiErr := srv.api.Guide(context.Background(), api.GuideRequest{})
	if apiErr != nil {
		t.Fatalf("guide no topic: %v", apiErr)
	}
	if len(resp.Topics) == 0 {
		t.Fatal("expected topics list")
	}
	// Verify the core topics are present.
	expected := map[string]bool{
		"metadata": false, "save": false, "search": false,
		"sessions": false, "collections": false, "curation": false,
		"temporal-queries": false,
	}
	for _, topic := range resp.Topics {
		expected[topic] = true
	}
	for topic, found := range expected {
		if !found {
			t.Errorf("missing topic: %s", topic)
		}
	}
}

func TestGuideReturnsContentForEachTopic(t *testing.T) {
	srv, _ := setupTestServer(t)

	list, apiErr := srv.api.Guide(context.Background(), api.GuideRequest{})
	if apiErr != nil {
		t.Fatalf("guide no topic: %v", apiErr)
	}
	for _, topic := range list.Topics {
		resp, apiErr := srv.api.Guide(context.Background(), api.GuideRequest{Topic: topic})
		if apiErr != nil {
			t.Errorf("guide(%s): %v", topic, apiErr)
			continue
		}
		if resp.Content == "" {
			t.Errorf("guide(%s): expected non-empty content", topic)
			continue
		}
		// Verify it's valid markdown (starts with # header).
		if !strings.HasPrefix(resp.Content, "#") {
			t.Errorf("guide(%s): content doesn't start with markdown header", topic)
		}
	}
}

func TestGuideInvalidTopicError(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, apiErr := srv.api.Guide(context.Background(), api.GuideRequest{Topic: "nonexistent"})
	if apiErr == nil {
		t.Fatal("expected error for invalid topic")
	}
	if apiErr.Code != "not_found" {
		t.Errorf("expected not_found code, got %s", apiErr.Code)
	}
	// The message must name the available topics so an agent can
	// self-correct without another round-trip.
	if !strings.Contains(apiErr.Message, "Available topics") {
		t.Errorf("expected message to list available topics, got %q", apiErr.Message)
	}
}

func TestGuideCaseInsensitive(t *testing.T) {
	srv, _ := setupTestServer(t)

	resp, apiErr := srv.api.Guide(context.Background(), api.GuideRequest{Topic: "METADATA"})
	if apiErr != nil {
		t.Fatalf("guide(METADATA): %v", apiErr)
	}
	if resp.Topic != "metadata" {
		t.Errorf("expected normalized topic name, got %v", resp.Topic)
	}
}

func TestGuideHTTPEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Topic list on the bare endpoint.
	w := doRequest(t, srv, "GET", "/v1/guide", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/guide: status %d (body: %s)", w.Code, w.Body.String())
	}
	result := parseResponse(t, w)
	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data envelope, got %v", result)
	}
	topics, ok := data["topics"].([]any)
	if !ok || len(topics) == 0 {
		t.Fatalf("expected non-empty topics list, got %v", data["topics"])
	}

	// Topic content via query param.
	w = doRequest(t, srv, "GET", "/v1/guide?topic=search", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/guide?topic=search: status %d (body: %s)", w.Code, w.Body.String())
	}
	result = parseResponse(t, w)
	data, ok = result["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data envelope, got %v", result)
	}
	if data["topic"] != "search" {
		t.Errorf("expected topic=search, got %v", data["topic"])
	}
	if content, _ := data["content"].(string); content == "" {
		t.Error("expected non-empty content")
	}

	// Unknown topic surfaces the api error at 404.
	w = doRequest(t, srv, "GET", "/v1/guide?topic=nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/guide?topic=nonexistent: status %d, want 404", w.Code)
	}
}
