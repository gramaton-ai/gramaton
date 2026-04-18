package curation

import (
	"strings"
	"testing"
)

func TestExtractObservationsShortContent(t *testing.T) {
	// <500 chars: no extraction, record IS the observation.
	obs := ExtractObservations("Short content under 500 chars.", 20)
	if obs != nil {
		t.Fatalf("expected nil for short content, got %d observations", len(obs))
	}
}

func TestExtractObservationsSlidingScale(t *testing.T) {
	tests := []struct {
		contentLen int
		wantMax    int
	}{
		{400, 0},    // <500: no extraction
		{1000, 1},   // 1000/1000 = 1
		{5000, 5},   // 5000/1000 = 5
		{10000, 10}, // 10000/1000 = 10
		{25000, 20}, // 25000/1000 = 25, capped at 20
	}

	for _, tt := range tests {
		got := maxObservations(tt.contentLen, 20)
		if got != tt.wantMax {
			t.Errorf("maxObservations(%d, 20) = %d, want %d", tt.contentLen, got, tt.wantMax)
		}
	}
}

func TestExtractObservationsBasic(t *testing.T) {
	// Build a ~2000 char document with distinct sentences.
	sentences := []string{
		"The engineering team decided to migrate from PostgreSQL to CockroachDB for horizontal scaling.",
		"This decision was driven by increasing read latency during peak hours.",
		"The migration will take approximately three months to complete.",
		"Sarah will lead the migration effort with support from the platform team.",
		"We need to maintain backward compatibility with the existing API.",
		"The new database supports automatic sharding across multiple regions.",
		"Performance testing showed a 40% improvement in read latency under load.",
		"The rollback plan involves switching the connection string back to PostgreSQL.",
		"All data will be migrated using a custom ETL pipeline built with Apache Beam.",
		"The estimated cost increase is $2,000 per month for the additional infrastructure.",
		"Documentation for the new setup will be published on the internal wiki.",
		"The team agreed to start with a shadow deployment running both databases in parallel.",
	}

	content := strings.Join(sentences, " ")
	if len(content) < 500 {
		t.Fatalf("test content too short: %d chars", len(content))
	}

	obs := ExtractObservations(content, 20)
	if obs == nil {
		t.Fatal("expected observations, got nil")
	}
	if len(obs) < 1 {
		t.Fatal("expected at least 1 observation")
	}

	// Observations should be substrings of the original content.
	for i, o := range obs {
		if !strings.Contains(content, o.Text) {
			t.Errorf("observation %d not found in content: %q", i, o.Text[:50])
		}
	}
}

func TestExtractObservationsPreservesOrder(t *testing.T) {
	// Build a ~3000 char document with filler and two distinctive sentences.
	filler := "The project team continued their regular work throughout the week and things proceeded normally as expected. "
	var parts []string
	for i := 0; i < 30; i++ {
		parts = append(parts, filler)
	}
	// Insert two distinctive sentences at known positions.
	parts[8] = "CockroachDB horizontal sharding eliminates single-node bottlenecks with automatic rebalancing across regions. "
	parts[22] = "Apache Kafka provides exactly-once delivery semantics for distributed event streaming pipelines at scale. "
	content := strings.Join(parts, "")

	obs := ExtractObservations(content, 5)
	if len(obs) < 2 {
		t.Fatalf("expected at least 2 observations, got %d (content len=%d)", len(obs), len(content))
	}

	// The CockroachDB sentence should come before the Kafka sentence
	// (preserving document order even though both score high).
	cockroachIdx := -1
	kafkaIdx := -1
	for i, o := range obs {
		if strings.Contains(o.Text, "CockroachDB") {
			cockroachIdx = i
		}
		if strings.Contains(o.Text, "Kafka") {
			kafkaIdx = i
		}
	}

	if cockroachIdx < 0 || kafkaIdx < 0 {
		t.Fatalf("expected both CockroachDB and Kafka observations to be selected, got: %v", obs)
	}
	if cockroachIdx >= kafkaIdx {
		t.Error("observations should preserve document order")
	}
}

func TestExtractObservationsCap(t *testing.T) {
	// Build a very long document (30K chars).
	var parts []string
	for i := 0; i < 100; i++ {
		parts = append(parts, "This is a moderately long sentence about topic number one hundred.")
		parts = append(parts, "Another sentence with different unique technical terminology here.")
		parts = append(parts, "The distributed systems architecture uses consensus protocols extensively.")
	}
	content := strings.Join(parts, " ")

	obs := ExtractObservations(content, 20)
	if len(obs) > 20 {
		t.Fatalf("expected at most 20 observations (cap), got %d", len(obs))
	}
}

func TestExtractObservationsCustomCap(t *testing.T) {
	var parts []string
	for i := 0; i < 50; i++ {
		parts = append(parts, "This is a long enough sentence to pass the minimum length filter here.")
	}
	content := strings.Join(parts, " ")

	obs := ExtractObservations(content, 3)
	if len(obs) > 3 {
		t.Fatalf("expected at most 3 observations (custom cap), got %d", len(obs))
	}
}

func TestExtractObservationsScoresPositive(t *testing.T) {
	content := strings.Repeat("The system uses distributed consensus for fault tolerance. ", 20)
	content += "PostgreSQL MVCC implementation provides snapshot isolation guarantees."

	obs := ExtractObservations(content, 20)
	for _, o := range obs {
		if o.Score <= 0 {
			t.Errorf("observation should have positive score: %q (score=%.4f)", o.Text[:30], o.Score)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	text := "This is the first complete sentence with enough words. " +
		"Here comes the second sentence that is also long enough to pass the filter! " +
		"And the third sentence asks a question with sufficient length? " +
		"Tiny. " +
		"The fifth and final sentence wraps everything up nicely here."
	sentences := splitSentences(text)

	// "Tiny." is <20 chars, should be filtered out.
	for _, s := range sentences {
		if strings.Contains(s.text, "Tiny") {
			t.Error("short sentence should be filtered")
		}
	}

	if len(sentences) < 3 {
		t.Fatalf("expected at least 3 sentences, got %d", len(sentences))
	}
}

func TestMaxObservationsZeroCap(t *testing.T) {
	// Zero cap should default to 20.
	got := maxObservations(50000, 0)
	if got != 20 {
		t.Fatalf("maxObservations with zero cap: got %d, want 20", got)
	}
}
