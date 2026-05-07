package kirocli

import "testing"

func TestExtractResponseClean(t *testing.T) {
	// Simulate typical kiro-cli output with ANSI codes.
	raw := "\x1b[38;5;252m\x1b[0m\x1b[?25l\x1b[38;5;141m> \x1b[0m{\"temporality\":\"durable\"}\x1b[0m\x1b[0m\n\x1b[38;5;8m\n \u25b8 Credits: 0.03 \u2022 Time: 1s\n\n\x1b[0m\x1b[1G\x1b[0m\x1b[0m\x1b[?25h"

	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	if result != `{"temporality":"durable"}` {
		t.Fatalf("expected clean JSON, got %q", result)
	}
}

func TestExtractResponseWithCodeFences(t *testing.T) {
	raw := "> ```json\n{\"temporality\":\"durable\"}\n```\n\n Credits: 0.01\n"

	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	if result != `{"temporality":"durable"}` {
		t.Fatalf("expected JSON without fences, got %q", result)
	}
}

func TestExtractResponseMultiLine(t *testing.T) {
	raw := "> {\n>   \"temporality\": \"durable\",\n>   \"confidence\": 0.8\n> }\n\n Credits: 0.02\n"

	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	// Should join lines into a valid JSON block.
	if result != "{\n\"temporality\": \"durable\",\n\"confidence\": 0.8\n}" {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestExtractResponseEmpty(t *testing.T) {
	raw := "\x1b[38;5;252m\x1b[0m\n\x1b[38;5;8m\n Credits: 0.00\n\x1b[0m"

	_, err := extractResponse(raw)
	if err == nil {
		t.Fatal("expected error for empty response")
	}
}

func TestExtractResponseSkipsTrustWarning(t *testing.T) {
	raw := "All tools are now trusted (!). Kiro will execute tools without asking for confirmation.\nAgents can sometimes do unexpected things so understand the risks.\nLearn more at https://kiro.dev/docs/cli/chat/security/#using-tools-trust-all-safely\n> {\"answer\":\"pong\"}\n\n Credits: 0.01\n"

	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	if result != `{"answer":"pong"}` {
		t.Fatalf("expected pong JSON, got %q", result)
	}
}

func TestStripCodeFences(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}", `{"a":1}`}, // no closing fence
	}
	for _, tt := range tests {
		got := stripCodeFences(tt.input)
		if got != tt.want {
			t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestModelAliases(t *testing.T) {
	if modelAliases["haiku"] != "claude-haiku-4.5" {
		t.Fatal("haiku alias wrong")
	}
	if modelAliases["sonnet"] != "claude-sonnet-4" {
		t.Fatal("sonnet alias wrong")
	}
}

// TestExtractResponsePreservesCreditsInMidLine confirms the
// tightened filter: a response sentence containing "Credits:" mid-
// line survives, because the footer filter requires the chrome
// pattern at line start.
func TestExtractResponsePreservesCreditsInMidLine(t *testing.T) {
	raw := "> The bank statement shows Credits: 4 and Debits: 2.\n\n Credits: 0.01\n"
	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	want := "The bank statement shows Credits: 4 and Debits: 2."
	if result != want {
		t.Fatalf("expected %q, got %q", want, result)
	}
}

// TestExtractResponsePreservesTimeInMidLine: same guard for "Time:"
// appearing mid-line in a legitimate response.
func TestExtractResponsePreservesTimeInMidLine(t *testing.T) {
	raw := "> The race ran Time: trial results at 42 seconds.\n\n Time: 1s\n"
	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	want := "The race ran Time: trial results at 42 seconds."
	if result != want {
		t.Fatalf("expected %q, got %q", want, result)
	}
}

// TestExtractResponsePreservesKiroDocsInMidLine: a model response
// mentioning kiro.dev/docs mid-sentence is kept, whereas a leading
// "Learn more at https://kiro.dev/docs/..." line is dropped.
func TestExtractResponsePreservesKiroDocsInMidLine(t *testing.T) {
	raw := "Learn more at https://kiro.dev/docs/cli/chat\n> The site kiro.dev/docs has tutorials.\n\n Credits: 0.01\n"
	result, err := extractResponse(raw)
	if err != nil {
		t.Fatalf("extractResponse: %v", err)
	}
	want := "The site kiro.dev/docs has tutorials."
	if result != want {
		t.Fatalf("expected %q, got %q", want, result)
	}
}
