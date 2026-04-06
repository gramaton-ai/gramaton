package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/graph"
)

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		input  []string
		expect string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b", "c"}, "a b c"},
	}
	for _, tt := range tests {
		got := joinStrings(tt.input)
		if got != tt.expect {
			t.Errorf("joinStrings(%v) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestInspectMetadataSummary(t *testing.T) {
	props := graph.Properties{
		"temporality":     graph.StringProperty("durable"),
		"confidence":      graph.Float64Property(0.9),
		"epistemic_status": graph.StringProperty("well_established"),
	}

	summary := inspectMetadataSummary(props)
	if summary == "" {
		t.Fatal("summary should not be empty")
	}
	if !containsStr(summary, "Current") {
		t.Fatal("should contain 'Current'")
	}
	if !containsStr(summary, "durable") {
		t.Fatal("should contain temporality")
	}
	if !containsStr(summary, "0.90") {
		t.Fatal("should contain confidence")
	}
}

func TestInspectMetadataSummaryHistorical(t *testing.T) {
	props := graph.Properties{
		"valid_until": graph.TimestampProperty(time.Now().UTC().Add(-24 * time.Hour)),
	}

	summary := inspectMetadataSummary(props)
	if !containsStr(summary, "Historical") {
		t.Fatal("should contain 'Historical' for expired record")
	}
}

func TestSetOptionalProps(t *testing.T) {
	req := &captureRequest{
		Content:         "Test",
		Temporality:     "durable",
		Confidence:      ptrFloat64(0.9),
		KnowledgeType:   "semantic",
		EpistemicStatus: "probable",
		Keywords:        []string{"test", "props"},
		SummaryShort:    "Short summary",
		SourceRef:       "http://example.com",
	}

	props := graph.Properties{}
	setOptionalProps(props, req)

	if v, ok := props["temporality"]; !ok || v.String() != "durable" {
		t.Fatal("temporality not set")
	}
	if v, ok := props["confidence"]; !ok || v.Float64() != 0.9 {
		t.Fatal("confidence not set")
	}
	if v, ok := props["knowledge_type"]; !ok || v.String() != "semantic" {
		t.Fatal("knowledge_type not set")
	}
	if _, ok := props["content_keywords"]; !ok {
		t.Fatal("keywords not set")
	}
	if v, ok := props["content_short"]; !ok || v.String() != "Short summary" {
		t.Fatal("content_short not set")
	}
	if v, ok := props["source_ref"]; !ok || v.String() != "http://example.com" {
		t.Fatal("source_ref not set")
	}
}

func TestSetOptionalPropsEmpty(t *testing.T) {
	req := &captureRequest{Content: "Only content"}
	props := graph.Properties{}
	setOptionalProps(props, req)

	// Only content-related fields should NOT be set since they're empty.
	if _, ok := props["temporality"]; ok {
		t.Fatal("empty temporality should not be set")
	}
	if _, ok := props["confidence"]; ok {
		t.Fatal("nil confidence should not be set")
	}
}

func TestSetOptionalPropsValidDates(t *testing.T) {
	req := &captureRequest{
		Content:    "Dated",
		ValidFrom:  "2026-01-01T00:00:00Z",
		ValidUntil: "2026-12-31T00:00:00Z",
	}
	props := graph.Properties{}
	setOptionalProps(props, req)

	if _, ok := props["valid_from"]; !ok {
		t.Fatal("valid_from should be set for valid RFC3339")
	}
	if _, ok := props["valid_until"]; !ok {
		t.Fatal("valid_until should be set for valid RFC3339")
	}
}

func TestSetOptionalPropsAssertedAsOf(t *testing.T) {
	req := &captureRequest{
		Content:      "Historical claim",
		AssertedAsOf: "2025-06-15T10:00:00Z",
	}
	props := graph.Properties{}
	setOptionalProps(props, req)

	if _, ok := props["asserted_as_of"]; !ok {
		t.Fatal("asserted_as_of should be set for valid RFC3339")
	}
}

func TestSetOptionalPropsAssertedAsOfInvalid(t *testing.T) {
	req := &captureRequest{
		Content:      "Bad date",
		AssertedAsOf: "not-a-date",
	}
	props := graph.Properties{}
	setOptionalProps(props, req)

	if _, ok := props["asserted_as_of"]; ok {
		t.Fatal("asserted_as_of should not be set for invalid date")
	}
}

func TestValidateCaptureRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     captureRequest
		wantErr bool
	}{
		{"valid", captureRequest{Content: "test", Temporality: "durable", Confidence: ptrFloat64(0.9)}, false},
		{"invalid temporality", captureRequest{Content: "test", Temporality: "bad"}, true},
		{"invalid knowledge_type", captureRequest{Content: "test", KnowledgeType: "bad"}, true},
		{"invalid epistemic", captureRequest{Content: "test", EpistemicStatus: "bad"}, true},
		{"confidence too high", captureRequest{Content: "test", Confidence: ptrFloat64(2.0)}, true},
		{"confidence too low", captureRequest{Content: "test", Confidence: ptrFloat64(-1.0)}, true},
	}

	for _, tt := range tests {
		err := validateCaptureRequest(&tt.req)
		if tt.wantErr && err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tt.name, err)
		}
	}
}

func TestValidateUpdateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     updateRequest
		wantErr bool
	}{
		{"valid", updateRequest{Temporality: "temporal"}, false},
		{"invalid temporality", updateRequest{Temporality: "bad"}, true},
		{"confidence out of range", updateRequest{Confidence: ptrFloat64(5.0)}, true},
		{"empty is valid", updateRequest{}, false},
	}

	for _, tt := range tests {
		err := validateUpdateRequest(&tt.req)
		if tt.wantErr && err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tt.name, err)
		}
	}
}

func TestValidateClassifyRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     classifyRequest
		wantErr bool
	}{
		{"valid", classifyRequest{Temporality: "immutable", Confidence: ptrFloat64(0.99)}, false},
		{"invalid temporality", classifyRequest{Temporality: "bad"}, true},
		{"all valid enums", classifyRequest{
			Temporality: "ephemeral", KnowledgeType: "reference", EpistemicStatus: "contested",
		}, false},
	}

	for _, tt := range tests {
		err := validateClassifyRequest(&tt.req)
		if tt.wantErr && err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tt.name, err)
		}
	}
}

// Test that creating a record with all optional fields works.
func TestCreateRecordFullProps(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/records", map[string]any{
		"content":           "Full record with all fields",
		"temporality":       "durable",
		"confidence":        0.95,
		"knowledge_type":    "semantic",
		"epistemic_status":  "well_established",
		"importance":        0.8,
		"keywords":          []string{"full", "test"},
		"summary_short":     "Full record test",
		"source_ref":        "http://example.com/doc",
		"source_credibility": 0.9,
		"context_about":     "testing",
		"valid_from":        "2026-01-01T00:00:00Z",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// --- helpers ---

func ptrFloat64(v float64) *float64 {
	return &v
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
