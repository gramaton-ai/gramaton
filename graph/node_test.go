package graph

import (
	"errors"
	"testing"
)

func TestAddNode(t *testing.T) {
	g := New()
	props := Properties{
		"content": StringProperty("We chose Kafka"),
		"confidence": Float64Property(0.9),
	}

	n := g.AddNode(props)

	if n.ID == "" {
		t.Fatal("node should have a non-empty ID")
	}
	if n.Properties["content"].String() != "We chose Kafka" {
		t.Fatal("content property mismatch")
	}
	if n.Properties["confidence"].Float64() != 0.9 {
		t.Fatal("confidence property mismatch")
	}
}

func TestAddNodeClonesProperties(t *testing.T) {
	g := New()
	props := Properties{
		"name": StringProperty("original"),
	}

	n := g.AddNode(props)
	props["name"] = StringProperty("mutated")

	if n.Properties["name"].String() != "original" {
		t.Fatal("AddNode did not clone properties")
	}
}

func TestAddNodeNilProperties(t *testing.T) {
	g := New()
	n := g.AddNode(nil)

	if n.Properties == nil {
		t.Fatal("nil properties should be initialized to empty map")
	}
	if len(n.Properties) != 0 {
		t.Fatal("expected 0 properties")
	}
}

func TestAddNodeUniqueIDs(t *testing.T) {
	g := New()
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		n := g.AddNode(nil)
		if ids[n.ID] {
			t.Fatalf("duplicate ID: %s", n.ID)
		}
		ids[n.ID] = true
	}
}

func TestGetNode(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"x": Int64Property(42)})

	got, ok := g.GetNode(n.ID)
	if !ok {
		t.Fatal("GetNode returned false for existing node")
	}
	if got.ID != n.ID {
		t.Fatal("GetNode returned wrong node")
	}
	if got.Properties["x"].Int64() != 42 {
		t.Fatal("property mismatch")
	}
}

func TestGetNodeNotFound(t *testing.T) {
	g := New()
	_, ok := g.GetNode("nonexistent")
	if ok {
		t.Fatal("GetNode should return false for missing node")
	}
}

func TestSetNodeProperty(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{"a": StringProperty("1")})

	// Add new property.
	if err := g.SetNodeProperty(n.ID, "b", StringProperty("2")); err != nil {
		t.Fatalf("SetNodeProperty: %v", err)
	}

	got, _ := g.GetNode(n.ID)
	if got.Properties["b"].String() != "2" {
		t.Fatal("new property not set")
	}

	// Overwrite existing.
	if err := g.SetNodeProperty(n.ID, "a", StringProperty("updated")); err != nil {
		t.Fatalf("SetNodeProperty overwrite: %v", err)
	}

	got, _ = g.GetNode(n.ID)
	if got.Properties["a"].String() != "updated" {
		t.Fatal("existing property not overwritten")
	}
}

func TestSetNodePropertyNotFound(t *testing.T) {
	g := New()
	err := g.SetNodeProperty("nonexistent", "k", StringProperty("v"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveNodeProperty(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"keep":   StringProperty("yes"),
		"remove": StringProperty("no"),
	})

	if err := g.RemoveNodeProperty(n.ID, "remove"); err != nil {
		t.Fatalf("RemoveNodeProperty: %v", err)
	}

	got, _ := g.GetNode(n.ID)
	if _, ok := got.Properties["remove"]; ok {
		t.Fatal("property should have been removed")
	}
	if got.Properties["keep"].String() != "yes" {
		t.Fatal("other property should remain")
	}
}

func TestRemoveNodePropertyMissing(t *testing.T) {
	g := New()
	n := g.AddNode(nil)

	// Removing a non-existent property is not an error.
	if err := g.RemoveNodeProperty(n.ID, "doesnt_exist"); err != nil {
		t.Fatalf("RemoveNodeProperty on missing key should not error: %v", err)
	}
}

func TestRemoveNodePropertyNodeNotFound(t *testing.T) {
	g := New()
	err := g.RemoveNodeProperty("nonexistent", "k")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteNode(t *testing.T) {
	g := New()
	n := g.AddNode(nil)

	if err := g.DeleteNode(n.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if g.NodeCount() != 0 {
		t.Fatal("node count should be 0")
	}
	if _, ok := g.GetNode(n.ID); ok {
		t.Fatal("deleted node should not be retrievable")
	}
}

func TestDeleteNodeNotFound(t *testing.T) {
	g := New()
	err := g.DeleteNode("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNodeCount(t *testing.T) {
	g := New()
	if g.NodeCount() != 0 {
		t.Fatal("empty graph should have 0 nodes")
	}
	g.AddNode(nil)
	g.AddNode(nil)
	if g.NodeCount() != 2 {
		t.Fatal("expected 2 nodes")
	}
}

func TestAllNodeIDs(t *testing.T) {
	g := New()
	n1 := g.AddNode(nil)
	n2 := g.AddNode(nil)

	ids := g.AllNodeIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}

	idSet := map[string]bool{ids[0]: true, ids[1]: true}
	if !idSet[n1.ID] || !idSet[n2.ID] {
		t.Fatal("AllNodeIDs missing expected IDs")
	}
}

func TestNodeIDIsULID(t *testing.T) {
	g := New()
	n := g.AddNode(nil)

	// ULIDs are 26 characters, Crockford Base32.
	if len(n.ID) != 26 {
		t.Fatalf("expected 26-char ULID, got %d chars: %s", len(n.ID), n.ID)
	}
}
