package server

import (
	"fmt"
	"testing"
)

func TestParseExtractedFacts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantErr  bool
	}{
		{
			name:    "valid JSON",
			input:   `{"facts": ["User prefers dark mode", "API uses JWT auth"]}`,
			wantLen: 2,
		},
		{
			name:    "empty facts",
			input:   `{"facts": []}`,
			wantLen: 0,
		},
		{
			name:    "with code fences",
			input:   "```json\n{\"facts\": [\"one fact\"]}\n```",
			wantLen: 1,
		},
		{
			name:    "with preamble text",
			input:   "Here are the facts:\n{\"facts\": [\"extracted\"]}",
			wantLen: 1,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts, err := parseExtractedFacts(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(facts) != tt.wantLen {
				t.Fatalf("expected %d facts, got %d", tt.wantLen, len(facts))
			}
		})
	}
}

func TestObserveExtractionPromptFormat(t *testing.T) {
	// Verify the prompt template can be formatted without panicking.
	conversation := "user: Hello\n\nassistant: Hi there\n\n"
	result := fmt.Sprintf(observeExtractionPrompt, conversation)
	if len(result) < 100 {
		t.Fatal("prompt should be substantial")
	}
}
