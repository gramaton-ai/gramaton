package graph

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestEdgeAdjacencyRoundTrip(t *testing.T) {
	g := New()

	// Build a graph with multiple edge types and directions.
	//
	//   A --related_to--> B --section_of--> C
	//   A --supersedes--> D
	//   B --related_to--> A  (bidirectional)
	//
	a := g.AddNode(Properties{"x": StringProperty("a")})
	b := g.AddNode(Properties{"x": StringProperty("b")})
	c := g.AddNode(Properties{"x": StringProperty("c")})
	d := g.AddNode(Properties{"x": StringProperty("d")})

	eAB, _ := g.AddEdge(a.ID, b.ID, "related_to", 0.8, nil)
	eBC, _ := g.AddEdge(b.ID, c.ID, "section_of", 1.0, nil)
	eAD, _ := g.AddEdge(a.ID, d.ID, "supersedes", 0.95, nil)
	eBA, _ := g.AddEdge(b.ID, a.ID, "related_to", 0.8, nil)

	// Marshal.
	data, err := g.MarshalEdgeAdjacency()
	if err != nil {
		t.Fatalf("MarshalEdgeAdjacency: %v", err)
	}

	// Unmarshal into a fresh graph.
	g2 := New()
	if err := g2.UnmarshalEdgeAdjacency(data); err != nil {
		t.Fatalf("UnmarshalEdgeAdjacency: %v", err)
	}

	// Verify outEdges: A should have 2 outbound edges.
	outA := g2.outEdges[a.ID]
	if len(outA) != 2 {
		t.Fatalf("expected 2 outbound edges from A, got %d", len(outA))
	}
	if _, ok := outA[eAB.ID]; !ok {
		t.Fatal("outEdges[A] missing edge A->B")
	}
	if _, ok := outA[eAD.ID]; !ok {
		t.Fatal("outEdges[A] missing edge A->D")
	}

	// Verify outEdges: B should have 2 outbound edges.
	outB := g2.outEdges[b.ID]
	if len(outB) != 2 {
		t.Fatalf("expected 2 outbound edges from B, got %d", len(outB))
	}

	// Verify inEdges: B should have 1 inbound edge (from A).
	inB := g2.inEdges[b.ID]
	if len(inB) != 1 {
		t.Fatalf("expected 1 inbound edge to B, got %d", len(inB))
	}
	if _, ok := inB[eAB.ID]; !ok {
		t.Fatal("inEdges[B] missing edge A->B")
	}

	// Verify inEdges: A should have 1 inbound edge (from B).
	inA := g2.inEdges[a.ID]
	if len(inA) != 1 {
		t.Fatalf("expected 1 inbound edge to A, got %d", len(inA))
	}
	if _, ok := inA[eBA.ID]; !ok {
		t.Fatal("inEdges[A] missing edge B->A")
	}

	// Verify typeEdges: related_to should have 2 edges.
	relatedEdges := g2.typeEdges["related_to"]
	if len(relatedEdges) != 2 {
		t.Fatalf("expected 2 related_to edges, got %d", len(relatedEdges))
	}

	// Verify typeEdges: section_of should have 1 edge.
	sectionEdges := g2.typeEdges["section_of"]
	if len(sectionEdges) != 1 {
		t.Fatalf("expected 1 section_of edge, got %d", len(sectionEdges))
	}
	if _, ok := sectionEdges[eBC.ID]; !ok {
		t.Fatal("typeEdges[section_of] missing edge B->C")
	}

	// Verify typeEdges: supersedes should have 1 edge.
	supersEdges := g2.typeEdges["supersedes"]
	if len(supersEdges) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(supersEdges))
	}
}

func TestEdgeAdjacencyEmpty(t *testing.T) {
	g := New()

	data, err := g.MarshalEdgeAdjacency()
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}

	g2 := New()
	if err := g2.UnmarshalEdgeAdjacency(data); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}

	if len(g2.outEdges) != 0 {
		t.Fatalf("expected 0 outEdges, got %d", len(g2.outEdges))
	}
	if len(g2.inEdges) != 0 {
		t.Fatalf("expected 0 inEdges, got %d", len(g2.inEdges))
	}
	if len(g2.typeEdges) != 0 {
		t.Fatalf("expected 0 typeEdges, got %d", len(g2.typeEdges))
	}
}

func TestEdgeAdjacencyInvalidData(t *testing.T) {
	g := New()

	if err := g.UnmarshalEdgeAdjacency(nil); err == nil {
		t.Fatal("expected error for nil data")
	}
	if err := g.UnmarshalEdgeAdjacency([]byte("NOPE")); err == nil {
		t.Fatal("expected error for invalid magic")
	}
	if err := g.UnmarshalEdgeAdjacency([]byte("EADJ\x01\x00")); err == nil {
		t.Fatal("expected error for truncated data after header")
	}
}

func TestEdgeAdjacencyExcessiveKeys(t *testing.T) {
	// Craft binary data with numKeys exceeding the safety cap.
	buf := []byte("EADJ")
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	// outEdges map with numKeys = 2,000,000 (exceeds maxAdjKeys = 1,000,000)
	buf = binary.LittleEndian.AppendUint32(buf, 2_000_000)

	g := New()
	err := g.UnmarshalEdgeAdjacency(buf)
	if err == nil {
		t.Fatal("expected error for excessive numKeys")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEdgeAdjacencyExcessiveVals(t *testing.T) {
	// Craft binary data with one key whose numVals exceeds the cap.
	buf := []byte("EADJ")
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	// outEdges: 1 key
	buf = binary.LittleEndian.AppendUint32(buf, 1)
	// key: "k"
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = append(buf, 'k')
	// numVals = 2,000,000 (exceeds maxAdjVals)
	buf = binary.LittleEndian.AppendUint32(buf, 2_000_000)

	g := New()
	err := g.UnmarshalEdgeAdjacency(buf)
	if err == nil {
		t.Fatal("expected error for excessive numVals")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEdgeAdjacencyDeterministic(t *testing.T) {
	// Serialization should be deterministic (sorted keys and values).
	g := New()
	a := g.AddNode(Properties{})
	b := g.AddNode(Properties{})
	c := g.AddNode(Properties{})
	g.AddEdge(c.ID, a.ID, "z_type", 1.0, nil)
	g.AddEdge(a.ID, b.ID, "a_type", 1.0, nil)
	g.AddEdge(b.ID, c.ID, "m_type", 1.0, nil)

	d1, _ := g.MarshalEdgeAdjacency()
	d2, _ := g.MarshalEdgeAdjacency()

	if len(d1) != len(d2) {
		t.Fatal("serialization should be deterministic")
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("byte %d differs between marshals", i)
		}
	}
}
