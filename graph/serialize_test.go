package graph

import (
	"testing"
	"time"
)

func TestMarshalUnmarshalNode(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 30, 0, 0, time.UTC)
	n := &Node{
		ID: "01H5K9E2GJ7A8NQXR5VT3M4BCW",
		Properties: Properties{
			"content":    StringProperty("We chose Kafka"),
			"confidence": Float64Property(0.9),
			"count":      Int64Property(5),
			"active":     BoolProperty(true),
			"created_at": TimestampProperty(ts),
			"keywords":   StringListProperty([]string{"kafka", "rabbitmq"}),
			"embedding":  VectorProperty([]float32{0.1, 0.2, 0.3}),
			"bloom":      BytesProperty([]byte{0xFF, 0x00}),
		},
	}

	data, err := MarshalNode(n)
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}

	got, err := UnmarshalNode(data)
	if err != nil {
		t.Fatalf("UnmarshalNode: %v", err)
	}

	if got.ID != n.ID {
		t.Fatalf("ID: expected %q, got %q", n.ID, got.ID)
	}
	if len(got.Properties) != len(n.Properties) {
		t.Fatalf("expected %d properties, got %d", len(n.Properties), len(got.Properties))
	}
	for k, v := range n.Properties {
		if !got.Properties[k].Equal(v) {
			t.Fatalf("property %q mismatch", k)
		}
	}
}

func TestMarshalUnmarshalNodeEmpty(t *testing.T) {
	n := &Node{
		ID:         "01H5K9E2GJ7A8NQXR5VT3M4BCW",
		Properties: Properties{},
	}

	data, err := MarshalNode(n)
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}
	got, err := UnmarshalNode(data)
	if err != nil {
		t.Fatalf("UnmarshalNode: %v", err)
	}
	if got.ID != n.ID {
		t.Fatalf("ID mismatch")
	}
	if len(got.Properties) != 0 {
		t.Fatalf("expected 0 properties, got %d", len(got.Properties))
	}
}

func TestMarshalUnmarshalEdge(t *testing.T) {
	e := &Edge{
		ID:       "01H5K9F3NK8B9PQYR6WT4N5CDX",
		SourceID: "01H5K9E2GJ7A8NQXR5VT3M4BCW",
		TargetID: "01H5K9G4PL9C0RSZT7XU5O6DEY",
		Type:     "justifies",
		Weight:   0.9,
		Properties: Properties{
			"reason": StringProperty("load testing results"),
		},
	}

	data, err := MarshalEdge(e)
	if err != nil {
		t.Fatalf("MarshalEdge: %v", err)
	}
	got, err := UnmarshalEdge(data)
	if err != nil {
		t.Fatalf("UnmarshalEdge: %v", err)
	}

	if got.ID != e.ID {
		t.Fatalf("ID: expected %q, got %q", e.ID, got.ID)
	}
	if got.SourceID != e.SourceID {
		t.Fatalf("SourceID mismatch")
	}
	if got.TargetID != e.TargetID {
		t.Fatalf("TargetID mismatch")
	}
	if got.Type != e.Type {
		t.Fatalf("Type mismatch")
	}
	if got.Weight != e.Weight {
		t.Fatalf("Weight: expected %f, got %f", e.Weight, got.Weight)
	}
	if !got.Properties["reason"].Equal(e.Properties["reason"]) {
		t.Fatalf("property mismatch")
	}
}

func TestMarshalUnmarshalEdgeNoProperties(t *testing.T) {
	e := &Edge{
		ID:         "EDGE01",
		SourceID:   "NODE01",
		TargetID:   "NODE02",
		Type:       "related_to",
		Weight:     0.5,
		Properties: Properties{},
	}

	data, err := MarshalEdge(e)
	if err != nil {
		t.Fatalf("MarshalEdge: %v", err)
	}
	got, err := UnmarshalEdge(data)
	if err != nil {
		t.Fatalf("UnmarshalEdge: %v", err)
	}
	if got.Type != "related_to" {
		t.Fatalf("Type mismatch")
	}
}

func TestMarshalNodeDeterministic(t *testing.T) {
	n := &Node{
		ID: "TEST",
		Properties: Properties{
			"z": StringProperty("last"),
			"a": StringProperty("first"),
			"m": StringProperty("middle"),
		},
	}
	d1, _ := MarshalNode(n)
	d2, _ := MarshalNode(n)
	if len(d1) != len(d2) {
		t.Fatal("non-deterministic node serialization: different lengths")
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("non-deterministic at byte %d", i)
		}
	}
}
