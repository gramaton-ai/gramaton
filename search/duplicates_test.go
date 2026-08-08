package search

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// Every pair of nodes here has identical embeddings so cosine
// exceeds any reasonable threshold. The structural edges are the
// only variable under test. Keep this graph small enough that each
// node sees every other in FindDuplicates' top-6 neighbor search.
func setupDupGraph(t *testing.T) (*graph.Graph, index.VectorIndex, map[string]*graph.Node) {
	t.Helper()
	g := graph.New()
	vec := index.NewFlatIndex()

	v := []float32{0.9, 0.1, 0.0}
	mk := func(label string) *graph.Node {
		return g.AddNode(graph.Properties{
			"content_full":   graph.StringProperty(label),
			"content_short":  graph.StringProperty(label),
			"embedding_full": graph.VectorProperty(v),
		})
	}

	nodes := map[string]*graph.Node{
		"parent":      mk("parent record"),
		"observation": mk("observation excerpt"),
		"segment":     mk("session segment"),
		"memrecord":   mk("extracted memory record"),
		"unrelatedX":  mk("unrelated record X"),
		"unrelatedY":  mk("unrelated record Y"),
	}

	for _, n := range nodes {
		vec.Add(n.ID, v)
	}

	g.AddEdge(nodes["observation"].ID, nodes["parent"].ID, "observation_of", 1.0, nil)
	g.AddEdge(nodes["segment"].ID, nodes["memrecord"].ID, "extracted_as", 1.0, nil)

	return g, vec, nodes
}

func TestFindDuplicatesSkipsObservationOfParent(t *testing.T) {
	g, vec, nodes := setupDupGraph(t)
	pairs := FindDuplicates(g, vec, 0.5, 100)

	for _, p := range pairs {
		if (p.IDA == nodes["parent"].ID && p.IDB == nodes["observation"].ID) ||
			(p.IDB == nodes["parent"].ID && p.IDA == nodes["observation"].ID) {
			t.Fatalf("observation/parent pair returned: %+v", p)
		}
	}
}

func TestFindDuplicatesSkipsExtractedAs(t *testing.T) {
	g, vec, nodes := setupDupGraph(t)
	pairs := FindDuplicates(g, vec, 0.5, 100)

	for _, p := range pairs {
		if (p.IDA == nodes["segment"].ID && p.IDB == nodes["memrecord"].ID) ||
			(p.IDB == nodes["segment"].ID && p.IDA == nodes["memrecord"].ID) {
			t.Fatalf("segment/memrecord pair returned: %+v", p)
		}
	}
}

func TestFindDuplicatesKeepsUnrelated(t *testing.T) {
	// Two unrelated high-similarity records are real duplicates and
	// must still surface.
	g, vec, nodes := setupDupGraph(t)
	pairs := FindDuplicates(g, vec, 0.5, 100)

	found := false
	for _, p := range pairs {
		if (p.IDA == nodes["unrelatedX"].ID && p.IDB == nodes["unrelatedY"].ID) ||
			(p.IDB == nodes["unrelatedX"].ID && p.IDA == nodes["unrelatedY"].ID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("unrelated pair with high similarity should surface as duplicate")
	}
}

func TestStructurallyRelatedMatrix(t *testing.T) {
	g := graph.New()
	a := g.AddNode(graph.Properties{"content_full": graph.StringProperty("a")})
	b := g.AddNode(graph.Properties{"content_full": graph.StringProperty("b")})
	c := g.AddNode(graph.Properties{"content_full": graph.StringProperty("c")})

	g.AddEdge(a.ID, b.ID, "observation_of", 1.0, nil)
	g.AddEdge(a.ID, c.ID, "related_to", 0.8, nil)

	if !structurallyRelated(g, a.ID, b.ID) {
		t.Fatal("observation_of should count as structural")
	}
	if !structurallyRelated(g, b.ID, a.ID) {
		t.Fatal("structurallyRelated should be direction-agnostic")
	}
	if structurallyRelated(g, a.ID, c.ID) {
		t.Fatal("related_to must NOT count as structural (semantic edge)")
	}
}

// TestFindDuplicatesSkipsSectionChildren pins the cross-record gap:
// a section child of doc A is cosine-identical to unrelated records,
// and structurallyRelated only spares the child-vs-own-parent pair.
// The node_type-keyed skip must keep sections out of EVERY pair, on
// both the scanning and the found side.
func TestFindDuplicatesSkipsSectionChildren(t *testing.T) {
	g, vec, nodes := setupDupGraph(t)
	section := g.AddNode(graph.Properties{
		"content_full":   graph.StringProperty("section fragment"),
		"content_short":  graph.StringProperty("section fragment"),
		"embedding_full": graph.VectorProperty([]float32{0.9, 0.1, 0.0}),
		"node_type":      graph.StringProperty("section"),
	})
	vec.Add(section.ID, []float32{0.9, 0.1, 0.0})
	g.AddEdge(section.ID, nodes["parent"].ID, "section_of", 1.0, nil)

	for _, p := range FindDuplicates(g, vec, 0.5, 100) {
		if p.IDA == section.ID || p.IDB == section.ID {
			t.Fatalf("section child appeared in a duplicate pair: %+v", p)
		}
	}
}
