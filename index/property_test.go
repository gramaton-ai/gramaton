package index

import (
	"sort"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- Exact match ---

func TestLookupString(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "temporality", graph.StringProperty("durable"))
	idx.Add("n2", "temporality", graph.StringProperty("durable"))
	idx.Add("n3", "temporality", graph.StringProperty("ephemeral"))

	got := idx.Lookup("temporality", graph.StringProperty("durable"))
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	assertContains(t, got, "n1", "n2")
}

func TestLookupFloat64(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "confidence", graph.Float64Property(0.9))
	idx.Add("n2", "confidence", graph.Float64Property(0.9))
	idx.Add("n3", "confidence", graph.Float64Property(0.5))

	got := idx.Lookup("confidence", graph.Float64Property(0.9))
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	assertContains(t, got, "n1", "n2")
}

func TestLookupInt64(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "access_count", graph.Int64Property(5))
	idx.Add("n2", "access_count", graph.Int64Property(10))

	got := idx.Lookup("access_count", graph.Int64Property(5))
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0] != "n1" {
		t.Fatalf("expected n1, got %s", got[0])
	}
}

func TestLookupBool(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "active", graph.BoolProperty(true))
	idx.Add("n2", "active", graph.BoolProperty(false))

	got := idx.Lookup("active", graph.BoolProperty(true))
	if len(got) != 1 || got[0] != "n1" {
		t.Fatalf("expected [n1], got %v", got)
	}
}

func TestLookupNoMatch(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "x", graph.StringProperty("a"))

	got := idx.Lookup("x", graph.StringProperty("b"))
	if len(got) != 0 {
		t.Fatalf("expected 0 results, got %d", len(got))
	}
}

func TestLookupMissingKey(t *testing.T) {
	idx := NewPropertyIndex()
	got := idx.Lookup("nonexistent", graph.StringProperty("x"))
	if len(got) != 0 {
		t.Fatal("expected empty result for missing key")
	}
}


// --- Substring search ---

func TestContains(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content", graph.StringProperty("We chose Kafka over RabbitMQ"))
	idx.Add("n2", "content", graph.StringProperty("RabbitMQ benchmarked at 12k/sec"))
	idx.Add("n3", "content", graph.StringProperty("Redis caching strategy"))

	got := idx.Contains("content", "RabbitMQ")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	assertContains(t, got, "n1", "n2")
}

func TestContainsCaseSensitive(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content", graph.StringProperty("Kafka"))

	got := idx.Contains("content", "kafka")
	if len(got) != 0 {
		t.Fatal("Contains should be case-sensitive")
	}
}

func TestContainsFold(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content", graph.StringProperty("Kafka"))
	idx.Add("n2", "content", graph.StringProperty("KAFKA cluster"))

	got := idx.ContainsFold("content", "kafka")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestContainsNoMatch(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content", graph.StringProperty("hello world"))

	got := idx.Contains("content", "xyz")
	if len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestContainsMissingKey(t *testing.T) {
	idx := NewPropertyIndex()
	got := idx.Contains("missing", "x")
	if len(got) != 0 {
		t.Fatal("expected empty for missing key")
	}
}

func TestContainsEmptySubstring(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "x", graph.StringProperty("anything"))

	got := idx.Contains("x", "")
	if len(got) != 1 {
		t.Fatal("empty substring should match everything")
	}
}

// --- Remove ---

func TestRemove(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "x", graph.StringProperty("hello"))
	idx.Add("n2", "x", graph.StringProperty("hello"))

	idx.Remove("n1", "x", graph.StringProperty("hello"))

	got := idx.Lookup("x", graph.StringProperty("hello"))
	if len(got) != 1 || got[0] != "n2" {
		t.Fatalf("expected [n2] after remove, got %v", got)
	}
}

func TestRemoveFromSubstring(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content", graph.StringProperty("hello world"))

	idx.Remove("n1", "content", graph.StringProperty("hello world"))

	got := idx.Contains("content", "hello")
	if len(got) != 0 {
		t.Fatal("expected 0 results after remove")
	}
}

func TestRemoveNonexistent(t *testing.T) {
	idx := NewPropertyIndex()
	// Should not panic.
	idx.Remove("n1", "x", graph.StringProperty("y"))
}

// --- RemoveNode ---

func TestRemoveNode(t *testing.T) {
	idx := NewPropertyIndex()
	props := graph.Properties{
		"name":       graph.StringProperty("test"),
		"confidence": graph.Float64Property(0.9),
		"count":      graph.Int64Property(5),
	}

	idx.Add("n1", "name", props["name"])
	idx.Add("n1", "confidence", props["confidence"])
	idx.Add("n1", "count", props["count"])

	// Also index another node to verify it survives.
	idx.Add("n2", "name", graph.StringProperty("other"))

	idx.RemoveNode("n1", props)

	if len(idx.Lookup("name", graph.StringProperty("test"))) != 0 {
		t.Fatal("n1 name should be removed")
	}
	if len(idx.Lookup("confidence", graph.Float64Property(0.9))) != 0 {
		t.Fatal("n1 confidence should be removed")
	}
	if len(idx.Lookup("name", graph.StringProperty("other"))) != 1 {
		t.Fatal("n2 should still be indexed")
	}
}

// --- Update (remove old, add new) ---

func TestUpdateProperty(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "confidence", graph.Float64Property(0.9))

	// Simulate property update: remove old, add new.
	idx.Remove("n1", "confidence", graph.Float64Property(0.9))
	idx.Add("n1", "confidence", graph.Float64Property(0.4))

	old := idx.Lookup("confidence", graph.Float64Property(0.9))
	if len(old) != 0 {
		t.Fatal("old value should not be in index")
	}

	cur := idx.Lookup("confidence", graph.Float64Property(0.4))
	if len(cur) != 1 || cur[0] != "n1" {
		t.Fatalf("expected [n1] for new value, got %v", cur)
	}
}

// --- Count ---

func TestCount(t *testing.T) {
	idx := NewPropertyIndex()
	if idx.Count() != 0 {
		t.Fatal("empty index should have count 0")
	}

	idx.Add("n1", "a", graph.StringProperty("x"))
	idx.Add("n2", "b", graph.Int64Property(1))
	if idx.Count() != 2 {
		t.Fatalf("expected 2, got %d", idx.Count())
	}

	idx.Remove("n1", "a", graph.StringProperty("x"))
	if idx.Count() != 1 {
		t.Fatalf("expected 1 after remove, got %d", idx.Count())
	}
}

// --- Non-string types should not appear in substring index ---

func TestNonStringTypesNotInSubstring(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "count", graph.Int64Property(42))

	got := idx.Contains("count", "42")
	if len(got) != 0 {
		t.Fatal("substring search should only work on string properties")
	}
}

// --- Persistence ---

func TestPropertyIndexRoundTrip(t *testing.T) {
	idx := NewPropertyIndex()

	// Add a mix of property types.
	idx.Add("n1", "temporality", graph.StringProperty("durable"))
	idx.Add("n2", "temporality", graph.StringProperty("ephemeral"))
	idx.Add("n1", "confidence", graph.Float64Property(0.9))
	idx.Add("n2", "confidence", graph.Float64Property(0.3))
	idx.Add("n1", "access_count", graph.Int64Property(5))
	idx.Add("n1", "content_keywords", graph.StringListProperty([]string{"kafka", "rabbitmq"}))
	idx.Add("n2", "content_keywords", graph.StringListProperty([]string{"kafka"}))
	idx.Add("n1", "content_full", graph.StringProperty("We chose Kafka for the event pipeline"))
	idx.Add("n1", "created_at", graph.TimestampProperty(time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)))

	// Marshal.
	data, err := idx.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Unmarshal into a fresh index.
	idx2 := NewPropertyIndex()
	if err := idx2.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Exact match.
	assertContains(t, idx2.Lookup("temporality", graph.StringProperty("durable")), "n1")
	assertContains(t, idx2.Lookup("temporality", graph.StringProperty("ephemeral")), "n2")

	// Substring search.
	subResult := idx2.ContainsFold("content_full", "kafka")
	assertContains(t, subResult, "n1")

	// Keyword index.
	kwResult := idx2.LookupKeyword("content_keywords", "kafka")
	assertContains(t, kwResult, "n1", "n2")
	kwResult2 := idx2.LookupKeyword("content_keywords", "rabbitmq")
	assertContains(t, kwResult2, "n1")

	// Count should match.
	if idx.Count() != idx2.Count() {
		t.Fatalf("count mismatch: original %d, restored %d", idx.Count(), idx2.Count())
	}
}

// --- Helpers ---

func assertContains(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}
