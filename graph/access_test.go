package graph

import (
	"testing"
	"time"
)

func TestRecordAccessIncrementsCount(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"access_count": Int64Property(0),
	})

	now := time.Now().UTC()
	g.RecordAccess(n.ID, now)

	got, _ := g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 1 {
		t.Fatalf("expected access_count 1, got %d", got.Properties["access_count"].Int64())
	}

	g.RecordAccess(n.ID, now)
	got, _ = g.GetNode(n.ID)
	if got.Properties["access_count"].Int64() != 2 {
		t.Fatalf("expected access_count 2, got %d", got.Properties["access_count"].Int64())
	}
}

func TestRecordAccessUpdatesLastAccessed(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{})

	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	g.RecordAccess(n.ID, now)

	got, _ := g.GetNode(n.ID)
	if !got.Properties["last_accessed"].Timestamp().Equal(now) {
		t.Fatalf("expected last_accessed %v, got %v", now, got.Properties["last_accessed"].Timestamp())
	}
}

// TestRecordAccessTouchesOnlyTheAccessedNode pins the activation
// removal: reading a record must dirty that record alone, never its
// neighbors -- neighbor writes on read were the mechanism behind
// read-driven commit churn.
func TestRecordAccessTouchesOnlyTheAccessedNode(t *testing.T) {
	g := New()
	a := g.AddNode(Properties{})
	b := g.AddNode(Properties{})
	if _, err := g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	g.ClearDirty()

	g.RecordAccess(a.ID, time.Now().UTC())

	neighbor, _ := g.GetNode(b.ID)
	if _, has := neighbor.Properties["activation_boost"]; has {
		t.Fatal("neighbor gained activation_boost; reads must not touch neighbors")
	}
	if _, has := neighbor.Properties["last_accessed"]; has {
		t.Fatal("neighbor gained last_accessed; reads must not touch neighbors")
	}
}

// TestRecordAccessMissingNodeNoOp: accessing a nonexistent node must
// not panic or create state.
func TestRecordAccessMissingNodeNoOp(t *testing.T) {
	g := New()
	g.RecordAccess("01MISSINGNODEXXXXXXXXXXXXX", time.Now().UTC())
	if g.NodeCount() != 0 {
		t.Fatal("RecordAccess on a missing node created state")
	}
}
