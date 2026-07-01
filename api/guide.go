package api

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed guide
var embeddedGuide embed.FS

// validGuideTopics is the single source of truth for which guide
// topics exist. GuideDescription is generated from it, so the topic
// list advertised to agents cannot drift from what Guide accepts.
// Adding a topic: append here AND add api/guide/<topic>.md.
var validGuideTopics = []string{
	"metadata", "save", "search", "sessions", "collections", "curation", "temporal-queries",
}

// GuideRequest selects a guide topic. An empty topic returns the
// list of available topics.
type GuideRequest struct {
	Topic string `json:"topic,omitempty" jsonschema:"guide topic name; omit to list available topics"`
}

// GuideResponse carries either the topic list (empty-topic call:
// Topics + Message) or one topic's content (Topic + Content).
type GuideResponse struct {
	Topics  []string `json:"topics,omitempty"`
	Message string   `json:"message,omitempty"`
	Topic   string   `json:"topic,omitempty"`
	Content string   `json:"content,omitempty"`
}

// GuideDescription is the MCP tool description shared by the
// in-process MCP registration and the CLI MCP proxy. A var rather
// than a const because the topic list is generated from
// validGuideTopics -- the advertised topics stay in lockstep with
// what the server accepts.
var GuideDescription = "Get help on how to use Gramaton effectively. Returns topical guidance for agents. Call with no topic to see available topics. Topics: " + strings.Join(validGuideTopics, ", ") + "."

// Guide returns guide content for a topic, or the topic list when no
// topic is given. Loads from the config directory first (user
// overrides), falling back to embedded files. Reads no engine state,
// so it takes no engine lock. The topic is validated against
// validGuideTopics before any path is built, so user input never
// reaches the filesystem unchecked.
func (a *API) Guide(ctx context.Context, req GuideRequest) (GuideResponse, *APIError) {
	if req.Topic == "" {
		a.log.Debug("guide: listing topics", "component", "guide")
		return GuideResponse{
			Topics:  validGuideTopics,
			Message: "Call gramaton_guide with a topic name to get help. Available topics: " + strings.Join(validGuideTopics, ", "),
		}, nil
	}

	topic := strings.ToLower(strings.TrimSpace(req.Topic))

	// Validate topic.
	valid := false
	for _, t := range validGuideTopics {
		if t == topic {
			valid = true
			break
		}
	}
	if !valid {
		a.log.Warn("guide: topic not found", "component", "guide",
			"topic", topic, "available", strings.Join(validGuideTopics, ", "))
		return GuideResponse{}, ErrNotFound(fmt.Sprintf("Unknown topic %q. Available topics: %s", topic, strings.Join(validGuideTopics, ", ")))
	}

	filename := topic + ".md"

	// Try loading from the config directory first (allows user overrides).
	if a.configDir != "" {
		filePath := filepath.Join(a.configDir, "guide", filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content := string(data)
			a.log.Debug("guide: loaded from file", "component", "guide",
				"topic", topic, "path", filePath, "size", len(content))
			return GuideResponse{Topic: topic, Content: content}, nil
		}
	}

	// Fall back to embedded files.
	data, err := embeddedGuide.ReadFile("guide/" + filename)
	if err != nil {
		a.log.Warn("guide: content load failed", "component", "guide",
			"topic", topic, "err", err)
		return GuideResponse{}, ErrInternal(fmt.Sprintf("failed to load guide content for %q", topic))
	}

	content := string(data)
	a.log.Debug("guide: loaded from embedded", "component", "guide",
		"topic", topic, "size", len(content))

	return GuideResponse{Topic: topic, Content: content}, nil
}
