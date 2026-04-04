package graph

import (
	"testing"
	"time"
)

func defaultActivationCfg() ActivationConfig {
	return ActivationConfig{
		BaseAmount:        1.0,
		AttenuationFactor: 0.5,
	}
}

func TestRecordAccessIncrementsCount(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"access_count": Int64Property(0),
	})

	now := time.Now().UTC()
	g.RecordAccess(n.ID, now, defaultActivationCfg())

	got, _ := g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 1 {
		t.Fatalf("expected access_count 1, got %d", got.Properties["access_count"].Int64())
	}

	g.RecordAccess(n.ID, now, defaultActivationCfg())
	got, _ = g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 2 {
		t.Fatalf("expected access_count 2, got %d", got.Properties["access_count"].Int64())
	}
}

func TestRecordAccessUpdatesLastAccessed(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{})

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	g.RecordAccess(n.ID, now, defaultActivationCfg())

	got, _ := g.GetNode(n.ID)
	if !got.Properties["last_accessed"].Timestamp().Equal(now) {
		t.Fatalf("expected last_accessed %v, got %v", now, got.Properties["last_accessed"].Timestamp())
	}
}

func TestRecordAccessBoostsOutboundNeighbors(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{})
	b := g.AddNode(Properties{})

	g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil)

	g.RecordAccess(a.ID, time.Now().UTC(), defaultActivationCfg())

	got, _ := g.GetNode(b.ID)
	boost := got.Properties["activation_boost"].Float64()
	// Expected: 1.0 * 0.8 * 0.5 = 0.4
	if !floatApprox(boost, 0.4, 0.001) {
		t.Fatalf("expected activation_boost ~0.4, got %f", boost)
	}
}

func TestRecordAccessBoostsInboundNeighbors(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{})
	b := g.AddNode(Properties{})

	// Edge from b -> a. Accessing a should boost b.
	g.AddEdge(b.ID, a.ID, "justifies", 0.9, nil)

	g.RecordAccess(a.ID, time.Now().UTC(), defaultActivationCfg())

	got, _ := g.GetNode(b.ID)
	boost := got.Properties["activation_boost"].Float64()
	// Expected: 1.0 * 0.9 * 0.5 = 0.45
	if !floatApprox(boost, 0.45, 0.001) {
		t.Fatalf("expected activation_boost ~0.45, got %f", boost)
	}
}

func TestRecordAccessAccumulates(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{})
	b := g.AddNode(Properties{})

	g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil)

	now := time.Now().UTC()
	g.RecordAccess(a.ID, now, defaultActivationCfg())
	g.RecordAccess(a.ID, now, defaultActivationCfg())
	g.RecordAccess(a.ID, now, defaultActivationCfg())

	got, _ := g.GetNode(b.ID)
	boost := got.Properties["activation_boost"].Float64()
	// Expected: 3 * (1.0 * 0.8 * 0.5) = 1.2
	if !floatApprox(boost, 1.2, 0.001) {
		t.Fatalf("expected activation_boost ~1.2, got %f", boost)
	}
}

func TestRecordAccessEdgeWeightScales(t *testing.T) {
	g := New()
	center := g.AddNode(Properties{})
	strong := g.AddNode(Properties{})
	weak := g.AddNode(Properties{})

	g.AddEdge(center.ID, strong.ID, "x", 1.0, nil)
	g.AddEdge(center.ID, weak.ID, "x", 0.2, nil)

	g.RecordAccess(center.ID, time.Now().UTC(), defaultActivationCfg())

	strongNode, _ := g.GetNode(strong.ID)
	weakNode, _ := g.GetNode(weak.ID)

	strongBoost := strongNode.Properties["activation_boost"].Float64()
	weakBoost := weakNode.Properties["activation_boost"].Float64()

	// Strong: 1.0 * 1.0 * 0.5 = 0.5
	// Weak:   1.0 * 0.2 * 0.5 = 0.1
	if !floatApprox(strongBoost, 0.5, 0.001) {
		t.Fatalf("strong boost: expected ~0.5, got %f", strongBoost)
	}
	if !floatApprox(weakBoost, 0.1, 0.001) {
		t.Fatalf("weak boost: expected ~0.1, got %f", weakBoost)
	}
}

func TestRecordAccessNoNeighbors(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{})

	// Should not panic with isolated node.
	g.RecordAccess(n.ID, time.Now().UTC(), defaultActivationCfg())

	got, _ := g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 1 {
		t.Fatal("access_count should still be updated")
	}
}

func TestRecordAccessMissingNode(t *testing.T) {
	g := New()
	// Should not panic.
	g.RecordAccess("nonexistent", time.Now().UTC(), defaultActivationCfg())
}

func TestRecordAccessNoInitialProperties(t *testing.T) {
	g := New()
	n := g.AddNode(nil) // No properties at all.

	g.RecordAccess(n.ID, time.Now().UTC(), defaultActivationCfg())

	got, _ := g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 1 {
		t.Fatal("should initialize access_count from zero")
	}
}

func floatApprox(a, b, tolerance float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < tolerance
}
