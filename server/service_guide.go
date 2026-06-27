package server

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed guide
var embeddedGuide embed.FS

var validGuideTopics = []string{
	"metadata", "save", "search", "sessions", "collections", "curation", "temporal-queries",
}

// serviceGuide returns guide content for a topic. Loads from the data
// directory first, falling back to embedded files.
func (s *Server) serviceGuide(topic string) (map[string]any, *serviceError) {
	if topic == "" {
		s.log.Debug("guide: listing topics", "component", "guide")
		return map[string]any{
			"topics":  validGuideTopics,
			"message": "Call gramaton_guide with a topic name to get help. Available topics: " + strings.Join(validGuideTopics, ", "),
		}, nil
	}

	topic = strings.ToLower(strings.TrimSpace(topic))

	// Validate topic.
	valid := false
	for _, t := range validGuideTopics {
		if t == topic {
			valid = true
			break
		}
	}
	if !valid {
		s.log.Warn("guide: topic not found", "component", "guide",
			"topic", topic, "available", strings.Join(validGuideTopics, ", "))
		return nil, &serviceError{
			Status:  404,
			Code:    "topic_not_found",
			Message: fmt.Sprintf("Unknown topic %q. Available topics: %s", topic, strings.Join(validGuideTopics, ", ")),
		}
	}

	filename := topic + ".md"

	// Try loading from data directory first (allows user overrides).
	if s.cfg.ConfigDir != "" {
		filePath := filepath.Join(s.cfg.ConfigDir, "guide", filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content := string(data)
			s.log.Debug("guide: loaded from file", "component", "guide",
				"topic", topic, "path", filePath, "size", len(content))
			return map[string]any{
				"topic":   topic,
				"content": content,
			}, nil
		}
	}

	// Fall back to embedded files.
	data, err := embeddedGuide.ReadFile("guide/" + filename)
	if err != nil {
		s.log.Warn("guide: content load failed", "component", "guide",
			"topic", topic, "err", err)
		return nil, errInternal(fmt.Sprintf("failed to load guide content for %q", topic))
	}

	content := string(data)
	s.log.Debug("guide: loaded from embedded", "component", "guide",
		"topic", topic, "size", len(content))

	return map[string]any{
		"topic":   topic,
		"content": content,
	}, nil
}
