package index

import (
	"testing"

	"github.com/brandonlattin/gramaton/graph"
)

func TestKeywordAddRemoveReadd(t *testing.T) {
	idx := NewPropertyIndex()

	kw := graph.StringListProperty([]string{"auth", "security"})
	idx.Add("n1", "content_keywords", kw)

	// Verify lookup works.
	ids := idx.LookupKeyword("content_keywords", "auth")
	if len(ids) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ids))
	}

	// Remove.
	idx.Remove("n1", "content_keywords", kw)

	ids = idx.LookupKeyword("content_keywords", "auth")
	if len(ids) != 0 {
		t.Fatalf("expected 0 after remove, got %d", len(ids))
	}

	// Re-add.
	idx.Add("n1", "content_keywords", kw)
	ids = idx.LookupKeyword("content_keywords", "auth")
	if len(ids) != 1 {
		t.Fatalf("expected 1 after re-add, got %d", len(ids))
	}
}

func TestKeywordMultipleNodes(t *testing.T) {
	idx := NewPropertyIndex()

	idx.Add("n1", "content_keywords", graph.StringListProperty([]string{"kafka", "events"}))
	idx.Add("n2", "content_keywords", graph.StringListProperty([]string{"kafka", "streaming"}))
	idx.Add("n3", "content_keywords", graph.StringListProperty([]string{"redis", "cache"}))

	ids := idx.LookupKeyword("content_keywords", "kafka")
	if len(ids) != 2 {
		t.Fatalf("expected 2 kafka nodes, got %d", len(ids))
	}

	ids = idx.LookupKeyword("content_keywords", "redis")
	if len(ids) != 1 {
		t.Fatalf("expected 1 redis node, got %d", len(ids))
	}
}

func TestKeywordCountsEmpty(t *testing.T) {
	idx := NewPropertyIndex()
	counts := idx.KeywordCounts("content_keywords")
	if counts != nil {
		t.Fatalf("expected nil for empty index, got %v", counts)
	}
}

func TestKeywordCounts(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content_keywords", graph.StringListProperty([]string{"auth", "security"}))
	idx.Add("n2", "content_keywords", graph.StringListProperty([]string{"auth", "oauth"}))
	idx.Add("n3", "content_keywords", graph.StringListProperty([]string{"auth", "jwt"}))

	counts := idx.KeywordCounts("content_keywords")
	if counts["auth"] != 3 {
		t.Fatalf("expected auth=3, got %d", counts["auth"])
	}
	if counts["security"] != 1 {
		t.Fatalf("expected security=1, got %d", counts["security"])
	}
}

func TestLookupKeywordNonexistent(t *testing.T) {
	idx := NewPropertyIndex()
	ids := idx.LookupKeyword("content_keywords", "nonexistent")
	if len(ids) != 0 {
		t.Fatalf("expected 0 for nonexistent keyword, got %d", len(ids))
	}
}

func TestLookupKeywordWrongKey(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content_keywords", graph.StringListProperty([]string{"test"}))

	ids := idx.LookupKeyword("wrong_key", "test")
	if len(ids) != 0 {
		t.Fatalf("expected 0 for wrong key, got %d", len(ids))
	}
}

func TestNodesWithKeyEmpty(t *testing.T) {
	idx := NewPropertyIndex()
	nodes := idx.NodesWithKey("nonexistent")
	if nodes != nil {
		t.Fatalf("expected nil, got %v", nodes)
	}
}

func TestNodesWithKey(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "temporality", graph.StringProperty("durable"))
	idx.Add("n2", "temporality", graph.StringProperty("temporal"))

	nodes := idx.NodesWithKey("temporality")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes with temporality, got %d", len(nodes))
	}
}

func TestRemoveNodeCleansAllIndexes(t *testing.T) {
	idx := NewPropertyIndex()

	props := graph.Properties{
		"temporality":      graph.StringProperty("durable"),
		"confidence":       graph.Float64Property(0.9),
		"content_keywords": graph.StringListProperty([]string{"test"}),
		"content_full":     graph.StringProperty("searchable text"),
	}

	for k, v := range props {
		idx.Add("n1", k, v)
	}

	// Verify everything is indexed.
	if len(idx.Lookup("temporality", graph.StringProperty("durable"))) != 1 {
		t.Fatal("should find by temporality before remove")
	}
	if len(idx.LookupKeyword("content_keywords", "test")) != 1 {
		t.Fatal("should find by keyword before remove")
	}
	if len(idx.ContainsFold("content_full", "searchable")) != 1 {
		t.Fatal("should find by substring before remove")
	}

	// Remove node.
	idx.RemoveNode("n1", props)

	// Verify everything is cleaned.
	if len(idx.Lookup("temporality", graph.StringProperty("durable"))) != 0 {
		t.Fatal("should not find by temporality after remove")
	}
	if len(idx.LookupKeyword("content_keywords", "test")) != 0 {
		t.Fatal("should not find by keyword after remove")
	}
	if len(idx.ContainsFold("content_full", "searchable")) != 0 {
		t.Fatal("should not find by substring after remove")
	}
}

func TestRangeQueryBoundaries(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "confidence", graph.Float64Property(0.0))
	idx.Add("n2", "confidence", graph.Float64Property(0.5))
	idx.Add("n3", "confidence", graph.Float64Property(1.0))

	// Exact boundary match.
	ids := idx.Range("confidence", graph.Float64Property(0.5), graph.Float64Property(0.5))
	if len(ids) != 1 {
		t.Fatalf("expected 1 at exact boundary, got %d", len(ids))
	}

	// Full range.
	ids = idx.Range("confidence", graph.Float64Property(0.0), graph.Float64Property(1.0))
	if len(ids) != 3 {
		t.Fatalf("expected 3 for full range, got %d", len(ids))
	}

	// Empty range.
	ids = idx.Range("confidence", graph.Float64Property(0.6), graph.Float64Property(0.9))
	if len(ids) != 0 {
		t.Fatalf("expected 0 for empty range, got %d", len(ids))
	}
}

func TestContainsFoldCaseInsensitive(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content_full", graph.StringProperty("RWMutex Deadlock"))

	ids := idx.ContainsFold("content_full", "rwmutex")
	if len(ids) != 1 {
		t.Fatalf("expected 1 for case-insensitive match, got %d", len(ids))
	}

	ids = idx.ContainsFold("content_full", "DEADLOCK")
	if len(ids) != 1 {
		t.Fatalf("expected 1 for uppercase match, got %d", len(ids))
	}
}

func TestContainsCaseSensitiveExact(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Add("n1", "content_full", graph.StringProperty("RWMutex"))

	ids := idx.Contains("content_full", "RWMutex")
	if len(ids) != 1 {
		t.Fatalf("expected 1 for exact case match, got %d", len(ids))
	}

	ids = idx.Contains("content_full", "rwmutex")
	if len(ids) != 0 {
		t.Fatalf("expected 0 for wrong case, got %d", len(ids))
	}
}

func TestKeywordRemovePartial(t *testing.T) {
	idx := NewPropertyIndex()

	// Two nodes share "auth" keyword.
	idx.Add("n1", "content_keywords", graph.StringListProperty([]string{"auth", "security"}))
	idx.Add("n2", "content_keywords", graph.StringListProperty([]string{"auth", "oauth"}))

	// Remove only n1.
	idx.Remove("n1", "content_keywords", graph.StringListProperty([]string{"auth", "security"}))

	// n2 should still be findable by "auth".
	ids := idx.LookupKeyword("content_keywords", "auth")
	if len(ids) != 1 {
		t.Fatalf("expected 1 after partial remove, got %d", len(ids))
	}

	// "security" should have no results.
	ids = idx.LookupKeyword("content_keywords", "security")
	if len(ids) != 0 {
		t.Fatalf("expected 0 for removed keyword, got %d", len(ids))
	}
}
