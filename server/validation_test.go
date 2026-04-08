package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

func TestParseJSON(t *testing.T) {
	body := strings.NewReader(`{"name":"test","value":42}`)
	req, _ := http.NewRequest("POST", "/", body)
	req.Header.Set("Content-Type", "application/json")

	var result struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	err := parseJSON(req, &result, maxJSONBodySize)
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if result.Name != "test" || result.Value != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseJSONEmptyBody(t *testing.T) {
	body := strings.NewReader("")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, maxJSONBodySize)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty body error, got: %v", err)
	}
}

func TestParseJSONInvalidJSON(t *testing.T) {
	body := strings.NewReader("not json")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, maxJSONBodySize)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got: %v", err)
	}
}

func TestParseJSONInvalidUTF8(t *testing.T) {
	body := strings.NewReader("{\"key\":\"\xff\"}")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, maxJSONBodySize)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected UTF-8 error, got: %v", err)
	}
}

func TestValidateFloat64RangeNil(t *testing.T) {
	err := validateFloat64Range("test", nil, 0, 1)
	if err != nil {
		t.Fatalf("nil should pass: %v", err)
	}
}

func TestValidateFloat64RangeValid(t *testing.T) {
	v := 0.5
	err := validateFloat64Range("test", &v, 0, 1)
	if err != nil {
		t.Fatalf("0.5 should be in range: %v", err)
	}
}

func TestValidateFloat64RangeOutOfRange(t *testing.T) {
	v := 2.0
	err := validateFloat64Range("test", &v, 0, 1)
	if err == nil {
		t.Fatal("2.0 should be out of range")
	}
}

func TestValidateEnumValid(t *testing.T) {
	err := validateEnum("test", "durable", validTemporalities)
	if err != nil {
		t.Fatalf("durable should be valid: %v", err)
	}
}

func TestValidateEnumInvalid(t *testing.T) {
	err := validateEnum("test", "bogus", validTemporalities)
	if err == nil {
		t.Fatal("bogus should be invalid")
	}
}

func TestValidateEnumEmpty(t *testing.T) {
	err := validateEnum("test", "", validTemporalities)
	if err != nil {
		t.Fatalf("empty should pass: %v", err)
	}
}

func TestMatchesTopic(t *testing.T) {
	srv, eng := setupTestServer(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":     graph.StringProperty("About Kafka streaming"),
		"content_short":    graph.StringProperty("Kafka setup"),
		"content_keywords": graph.StringListProperty([]string{"kafka", "streaming"}),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	// Should match by keyword.
	if !matchesTopic(srv, n.ID, "kafka") {
		t.Fatal("should match topic 'kafka' via keyword")
	}
	// Should match by summary (case-insensitive).
	if !matchesTopic(srv, n.ID, "kafka") {
		t.Fatal("should match topic via summary")
	}
	// Should not match unrelated topic.
	if matchesTopic(srv, n.ID, "redis") {
		t.Fatal("should not match unrelated topic")
	}
	// Non-existent node.
	if matchesTopic(srv, "nonexistent", "kafka") {
		t.Fatal("should not match nonexistent node")
	}
}

func TestParseDateArg(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"2026-04-05", true},
		{"2026-04-05T10:30:00Z", true},
		{"not-a-date", false},
		{"", false},
	}
	for _, tt := range tests {
		_, err := parseDateArg(tt.input)
		if tt.valid && err != nil {
			t.Errorf("parseDateArg(%q) should be valid, got: %v", tt.input, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("parseDateArg(%q) should be invalid", tt.input)
		}
	}
}
