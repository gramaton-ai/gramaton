package index

import "testing"

func TestBloomFilterBasic(t *testing.T) {
	bf := NewBloomFilter(100, 0.01)
	bf.Add("hello")
	bf.Add("world")

	if !bf.Contains("hello") {
		t.Fatal("should contain 'hello'")
	}
	if !bf.Contains("world") {
		t.Fatal("should contain 'world'")
	}
	// "missing" should (almost certainly) not be in the filter.
	// With 100 expected items and 2 actual items, FP rate is negligible.
	if bf.Contains("missing") {
		t.Fatal("should not contain 'missing' (false positive)")
	}
}

func TestBloomFilterRoundTrip(t *testing.T) {
	bf := NewBloomFilter(50, 0.01)
	for _, w := range []string{"kafka", "rabbitmq", "event", "pipeline"} {
		bf.Add(w)
	}

	data, err := bf.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	bf2 := &BloomFilter{}
	if err := bf2.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, w := range []string{"kafka", "rabbitmq", "event", "pipeline"} {
		if !bf2.Contains(w) {
			t.Fatalf("restored filter should contain %q", w)
		}
	}
	if bf2.Contains("missing") {
		t.Fatal("restored filter should not contain 'missing'")
	}
	if bf2.Count() != 4 {
		t.Fatalf("expected count 4, got %d", bf2.Count())
	}
}

func TestBloomIndexMayContainAll(t *testing.T) {
	bi := NewBloomIndex()
	bi.AddTerms("n1", []string{"kafka", "rabbitmq", "event"})
	bi.AddTerms("n2", []string{"postgresql", "database"})

	// n1 should pass for kafka+event.
	if !bi.MayContainAll("n1", []string{"kafka", "event"}) {
		t.Fatal("n1 should pass for kafka+event")
	}
	// n1 should fail for postgresql.
	if bi.MayContainAll("n1", []string{"postgresql"}) {
		t.Fatal("n1 should not pass for postgresql")
	}
	// n2 should pass for postgresql.
	if !bi.MayContainAll("n2", []string{"postgresql"}) {
		t.Fatal("n2 should pass for postgresql")
	}
	// Unknown node should pass (conservative).
	if !bi.MayContainAll("unknown", []string{"anything"}) {
		t.Fatal("unknown node should pass (no filter)")
	}
}
