package api

import (
	"context"
	"strings"
	"testing"
)

// TestGuideDescriptionListsAllTopics pins the anti-drift property the
// generated description exists for: every valid topic appears in the
// text agents read from the tool list. If GuideDescription is ever
// rewritten as a hand-maintained literal, this test catches the first
// topic it forgets.
func TestGuideDescriptionListsAllTopics(t *testing.T) {
	for _, topic := range validGuideTopics {
		if !strings.Contains(GuideDescription, topic) {
			t.Errorf("GuideDescription does not mention topic %q", topic)
		}
	}
}

// TestGuideEveryTopicHasEmbeddedContent asserts validGuideTopics and
// the embedded guide/ directory stay in sync in both directions: a
// topic without a file would 500 at runtime, and a file without a
// topic is unreachable dead weight.
func TestGuideEveryTopicHasEmbeddedContent(t *testing.T) {
	for _, topic := range validGuideTopics {
		if _, err := embeddedGuide.ReadFile("guide/" + topic + ".md"); err != nil {
			t.Errorf("topic %q has no embedded guide file: %v", topic, err)
		}
	}

	entries, err := embeddedGuide.ReadDir("guide")
	if err != nil {
		t.Fatalf("read embedded guide dir: %v", err)
	}
	valid := make(map[string]bool, len(validGuideTopics))
	for _, topic := range validGuideTopics {
		valid[topic] = true
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".md")
		if !valid[name] {
			t.Errorf("embedded guide file %q has no entry in validGuideTopics", entry.Name())
		}
	}
}

// TestGuideRejectsOverlongTopic pins the input cap: an invalid topic
// is echoed into the log and the not-found message, so topic-shaped
// input is bounded before use like api/diff.go's.
func TestGuideRejectsOverlongTopic(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.Guide(context.Background(), GuideRequest{Topic: strings.Repeat("x", MaxTopicLength+1)})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for overlong topic, got %+v", apiErr)
	}
}
