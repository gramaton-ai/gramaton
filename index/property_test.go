package index

import (
	"fmt"
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

// --- Range queries ---

func TestRangeFloat64(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "confidence", graph.Float64Property(0.3))
	idx.Add("n2", "confidence", graph.Float64Property(0.5))
	idx.Add("n3", "confidence", graph.Float64Property(0.7))
	idx.Add("n4", "confidence", graph.Float64Property(0.9))
	idx.Add("n5", "confidence", graph.Float64Property(0.1))

	got := idx.Range("confidence",
		graph.Float64Property(0.4),
		graph.Float64Property(0.8))

	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(got), got)
	}
	assertContains(t, got, "n2", "n3")
}

func TestRangeInt64(t *testing.T) {
	idx := NewPropertyIndex()
	for i := int64(0); i < 10; i++ {
		idx.Add(fmt.Sprintf("n%d", i), "count", graph.Int64Property(i))
	}

	got := idx.Range("count",
		graph.Int64Property(3),
		graph.Int64Property(6))

	if len(got) != 4 {
		t.Fatalf("expected 4 results, got %d: %v", len(got), got)
	}
	assertContains(t, got, "n3", "n4", "n5", "n6")
}

func TestRangeString(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "name", graph.StringProperty("alpha"))
	idx.Add("n2", "name", graph.StringProperty("beta"))
	idx.Add("n3", "name", graph.StringProperty("gamma"))
	idx.Add("n4", "name", graph.StringProperty("delta"))

	got := idx.Range("name",
		graph.StringProperty("beta"),
		graph.StringProperty("gamma"))

	if len(got) != 3 {
		t.Fatalf("expected 3 results (beta, delta, gamma), got %d: %v", len(got), got)
	}
	assertContains(t, got, "n2", "n3", "n4")
}

func TestRangeTimestamp(t *testing.T) {
	idx := NewPropertyIndex()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	idx.Add("n1", "created", graph.TimestampProperty(t1))
	idx.Add("n2", "created", graph.TimestampProperty(t2))
	idx.Add("n3", "created", graph.TimestampProperty(t3))
	idx.Add("n4", "created", graph.TimestampProperty(t4))

	got := idx.Range("created",
		graph.TimestampProperty(t2),
		graph.TimestampProperty(t3))

	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), got)
	}
	assertContains(t, got, "n2", "n3")
}

func TestRangeInclusive(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "x", graph.Int64Property(5))
	idx.Add("n2", "x", graph.Int64Property(10))

	got := idx.Range("x",
		graph.Int64Property(5),
		graph.Int64Property(10))

	if len(got) != 2 {
		t.Fatalf("expected 2 (inclusive bounds), got %d", len(got))
	}
}

func TestRangeNoMatch(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "x", graph.Int64Property(1))
	idx.Add("n2", "x", graph.Int64Property(10))

	got := idx.Range("x",
		graph.Int64Property(3),
		graph.Int64Property(5))

	if len(got) != 0 {
		t.Fatalf("expected 0 results, got %d", len(got))
	}
}

func TestRangeMissingKey(t *testing.T) {
	idx := NewPropertyIndex()
	got := idx.Range("missing",
		graph.Int64Property(0),
		graph.Int64Property(100))
	if len(got) != 0 {
		t.Fatal("expected empty for missing key")
	}
}

func TestRangeDuplicateValues(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "score", graph.Float64Property(0.5))
	idx.Add("n2", "score", graph.Float64Property(0.5))
	idx.Add("n3", "score", graph.Float64Property(0.5))

	got := idx.Range("score",
		graph.Float64Property(0.5),
		graph.Float64Property(0.5))

	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
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

func TestRemoveFromRange(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "score", graph.Float64Property(0.5))
	idx.Add("n2", "score", graph.Float64Property(0.7))

	idx.Remove("n1", "score", graph.Float64Property(0.5))

	got := idx.Range("score",
		graph.Float64Property(0.0),
		graph.Float64Property(1.0))

	if len(got) != 1 || got[0] != "n2" {
		t.Fatalf("expected [n2], got %v", got)
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

// --- Non-ordered types should not appear in range index ---

func TestNonOrderedTypesNotInRange(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "active", graph.BoolProperty(true))
	idx.Add("n2", "vec", graph.VectorProperty([]float32{1, 2}))
	idx.Add("n3", "tags", graph.StringListProperty([]string{"a"}))
	idx.Add("n4", "raw", graph.BytesProperty([]byte{1}))

	// These keys should not have sorted entries.
	if len(idx.sorted) != 0 {
		t.Fatalf("expected 0 sorted keys, got %d", len(idx.sorted))
	}

	// But exact lookup should still work.
	got := idx.Lookup("active", graph.BoolProperty(true))
	if len(got) != 1 {
		t.Fatal("exact lookup on bool should work")
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
