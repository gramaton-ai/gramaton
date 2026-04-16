package server

import (
	"strings"
	"testing"
)

func TestGuideReturnsTopicList(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, svcErr := srv.serviceGuide("")
	if svcErr != nil {
		t.Fatalf("guide no topic: %v", svcErr)
	}
	topics, ok := result["topics"].([]string)
	if !ok || len(topics) == 0 {
		t.Fatal("expected topics list")
	}
	// Verify all 6 topics present.
	expected := map[string]bool{
		"metadata": false, "capture": false, "search": false,
		"sessions": false, "collections": false, "curation": false,
	}
	for _, topic := range topics {
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

	for _, topic := range []string{"metadata", "capture", "search", "sessions", "collections", "curation"} {
		result, svcErr := srv.serviceGuide(topic)
		if svcErr != nil {
			t.Errorf("guide(%s): %v", topic, svcErr)
			continue
		}
		content, ok := result["content"].(string)
		if !ok || content == "" {
			t.Errorf("guide(%s): expected non-empty content", topic)
			continue
		}
		// Verify it's valid markdown (starts with # header).
		if !strings.HasPrefix(content, "#") {
			t.Errorf("guide(%s): content doesn't start with markdown header", topic)
		}
	}
}

func TestGuideInvalidTopicError(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceGuide("nonexistent")
	if svcErr == nil {
		t.Fatal("expected error for invalid topic")
	}
	if svcErr.Code != "topic_not_found" {
		t.Errorf("expected topic_not_found code, got %s", svcErr.Code)
	}
}

func TestGuideCaseInsensitive(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, svcErr := srv.serviceGuide("METADATA")
	if svcErr != nil {
		t.Fatalf("guide(METADATA): %v", svcErr)
	}
	if result["topic"] != "metadata" {
		t.Errorf("expected normalized topic name, got %v", result["topic"])
	}
}
