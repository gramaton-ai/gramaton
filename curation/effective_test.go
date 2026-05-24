package curation

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// addCollection creates a collection-typed node with the given knob
// values. Empty strings leave the property unset (exercising the
// collection default fallback inside the readers).
func addCollection(t *testing.T, g *graph.Graph, curation, supersession, contradictions string) string {
	t.Helper()
	props := graph.Properties{
		propKnowledgeType: graph.StringProperty("collection"),
	}
	if curation != "" {
		props[propCuration] = graph.StringProperty(curation)
	}
	if supersession != "" {
		props[propSupersession] = graph.StringProperty(supersession)
	}
	if contradictions != "" {
		props[propContradictions] = graph.StringProperty(contradictions)
	}
	return g.AddNode(props).ID
}

// addRecord creates a record node and joins it to each collection
// via a member_of edge.
func addRecord(t *testing.T, g *graph.Graph, collectionIDs ...string) string {
	t.Helper()
	rec := g.AddNode(graph.Properties{}).ID
	for _, cID := range collectionIDs {
		if _, err := g.AddEdge(rec, cID, "member_of", 1.0, nil); err != nil {
			t.Fatalf("AddEdge member_of: %v", err)
		}
	}
	return rec
}

// TestIsSupersessionOptOut: the helper is true only when effective
// supersession resolves to "none". Orphans and records in
// collections with supersession != none must NOT be flagged as
// opt-out.
func TestIsSupersessionOptOut(t *testing.T) {
	g := graph.New()

	orphan := g.AddNode(graph.Properties{}).ID
	if IsSupersessionOptOut(g, orphan) {
		t.Error("orphan: got opt-out, want false (memory orphan default = store)")
	}

	collNone := addCollection(t, g, "", "none", "")
	recNone := addRecord(t, g, collNone)
	if !IsSupersessionOptOut(g, recNone) {
		t.Error("record in supersession=none collection: got false, want opt-out")
	}

	collColl := addCollection(t, g, "", "collection", "")
	recColl := addRecord(t, g, collColl)
	if IsSupersessionOptOut(g, recColl) {
		t.Error("record in supersession=collection: got opt-out, want false")
	}

	collStore := addCollection(t, g, "", "store", "")
	recStore := addRecord(t, g, collStore)
	if IsSupersessionOptOut(g, recStore) {
		t.Error("record in supersession=store: got opt-out, want false")
	}

	// Mixed: one collection at none + another at store. Most-
	// restrictive wins for the destructive knob -> effective none.
	recMixed := addRecord(t, g, collNone, collStore)
	if !IsSupersessionOptOut(g, recMixed) {
		t.Error("mixed none+store membership: got false, want opt-out (most-restrictive wins)")
	}
}

// TestEffectiveCurationOrphan: a record with no member_of edges
// gets the memory-orphan defaults. This is today's Memory record
// behaviour and the no-regression contract.
func TestEffectiveCurationOrphan(t *testing.T) {
	g := graph.New()
	rec := g.AddNode(graph.Properties{}).ID

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       MemoryOrphanCuration,
		Supersession:   MemoryOrphanSupersession,
		Contradictions: MemoryOrphanContradictions,
	}
	if got != want {
		t.Errorf("orphan: got %+v, want %+v", got, want)
	}
}

// TestEffectiveCurationSingleMembership: a record in exactly one
// collection inherits that collection's three knobs verbatim. No
// resolution rule is exercised.
func TestEffectiveCurationSingleMembership(t *testing.T) {
	g := graph.New()
	cID := addCollection(t, g, "none", "collection", "off")
	rec := addRecord(t, g, cID)

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       "none",
		Supersession:   "collection",
		Contradictions: "off",
	}
	if got != want {
		t.Errorf("single membership: got %+v, want %+v", got, want)
	}
}

// TestEffectiveCurationCollectionDefaults: a record in a collection
// with no explicit knobs stored gets the collection-level defaults.
// (Collection default for supersession is "collection", which differs
// from the orphan default "store" -- this tests we don't conflate
// the two.)
func TestEffectiveCurationCollectionDefaults(t *testing.T) {
	g := graph.New()
	cID := addCollection(t, g, "", "", "")
	rec := addRecord(t, g, cID)

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       collectionDefaultCuration,
		Supersession:   collectionDefaultSupersession,
		Contradictions: collectionDefaultContradictions,
	}
	if got != want {
		t.Errorf("collection defaults: got %+v, want %+v", got, want)
	}
}

// TestEffectiveCurationLegacyValuesNormalize: a collection with the
// dropped 4-level curation values stored on disk reads back as the
// new 2-level form. minimal -> none, full -> standard. This is the
// no-migration-sweep contract for stores that pre-date the redesign.
func TestEffectiveCurationLegacyValuesNormalize(t *testing.T) {
	cases := []struct {
		stored string
		want   string
	}{
		{"minimal", "none"},
		{"full", "standard"},
		{"standard", "standard"},
		{"none", "none"},
	}
	for _, tc := range cases {
		g := graph.New()
		cID := addCollection(t, g, tc.stored, "collection", "on")
		rec := addRecord(t, g, cID)
		got := EffectiveCurationFor(g, rec)
		if got.Curation != tc.want {
			t.Errorf("stored=%q: Curation=%q, want %q", tc.stored, got.Curation, tc.want)
		}
	}
}

// TestEffectiveCurationSupersessionMostRestrictive verifies that
// when a record sits in two collections with conflicting
// supersession settings, the most-restrictive value wins.
//
// Rule: none > collection > store. supersession is destructive
// (sets valid_until on records); never destroy state any membership
// wants preserved.
func TestEffectiveCurationSupersessionMostRestrictive(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"none", "collection", "none"},
		{"none", "store", "none"},
		{"collection", "store", "collection"},
		{"collection", "collection", "collection"},
		{"store", "store", "store"},
	}
	for _, tc := range cases {
		g := graph.New()
		cA := addCollection(t, g, "standard", tc.a, "on")
		cB := addCollection(t, g, "standard", tc.b, "on")
		rec := addRecord(t, g, cA, cB)
		got := EffectiveCurationFor(g, rec)
		if got.Supersession != tc.want {
			t.Errorf("supersession(%q, %q) = %q, want %q", tc.a, tc.b, got.Supersession, tc.want)
		}
	}
}

// TestEffectiveCurationCurationMostPermissive verifies that when a
// record sits in two collections with conflicting curation
// settings, the most-permissive value wins.
//
// Rule: standard > none. curation is additive (LLM stages add
// metadata fields); always enrich when any membership wants it.
func TestEffectiveCurationCurationMostPermissive(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"standard", "none", "standard"},
		{"none", "standard", "standard"},
		{"none", "none", "none"},
		{"standard", "standard", "standard"},
	}
	for _, tc := range cases {
		g := graph.New()
		cA := addCollection(t, g, tc.a, "collection", "off")
		cB := addCollection(t, g, tc.b, "collection", "off")
		rec := addRecord(t, g, cA, cB)
		got := EffectiveCurationFor(g, rec)
		if got.Curation != tc.want {
			t.Errorf("curation(%q, %q) = %q, want %q", tc.a, tc.b, got.Curation, tc.want)
		}
	}
}

// TestEffectiveCurationContradictionsMostPermissive verifies that
// when a record sits in two collections with conflicting
// contradictions settings, the most-permissive value wins.
//
// Rule: on > off. contradictions is additive (creates contradicts
// edges); always enrich when any membership wants it.
func TestEffectiveCurationContradictionsMostPermissive(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"on", "off", "on"},
		{"off", "on", "on"},
		{"off", "off", "off"},
		{"on", "on", "on"},
	}
	for _, tc := range cases {
		g := graph.New()
		cA := addCollection(t, g, "standard", "collection", tc.a)
		cB := addCollection(t, g, "standard", "collection", tc.b)
		rec := addRecord(t, g, cA, cB)
		got := EffectiveCurationFor(g, rec)
		if got.Contradictions != tc.want {
			t.Errorf("contradictions(%q, %q) = %q, want %q", tc.a, tc.b, got.Contradictions, tc.want)
		}
	}
}

// TestEffectiveCurationStaleEdgeIgnored: a member_of edge to a
// deleted collection node should be ignored, not crash. With one
// stale edge and no other memberships, the record falls back to
// orphan defaults.
func TestEffectiveCurationStaleEdgeIgnored(t *testing.T) {
	g := graph.New()
	cID := addCollection(t, g, "none", "collection", "off")
	rec := addRecord(t, g, cID)

	// Delete the collection node, leaving the member_of edge
	// pointing to nowhere.
	g.DeleteNode(cID)

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       MemoryOrphanCuration,
		Supersession:   MemoryOrphanSupersession,
		Contradictions: MemoryOrphanContradictions,
	}
	if got != want {
		t.Errorf("stale edge: got %+v, want %+v", got, want)
	}
}

// TestEffectiveCurationNonCollectionTargetIgnored: a member_of edge
// pointing at a non-collection node (e.g. another record, or a
// concept) is ignored. With one such bad edge and no real
// memberships, the record falls back to orphan defaults.
func TestEffectiveCurationNonCollectionTargetIgnored(t *testing.T) {
	g := graph.New()
	other := g.AddNode(graph.Properties{
		propKnowledgeType: graph.StringProperty("conceptual"),
	}).ID
	rec := addRecord(t, g, other)

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       MemoryOrphanCuration,
		Supersession:   MemoryOrphanSupersession,
		Contradictions: MemoryOrphanContradictions,
	}
	if got != want {
		t.Errorf("non-collection target: got %+v, want %+v", got, want)
	}
}

// TestEffectiveCurationCombinedScenario walks the canonical
// "journal + knowledge bucket" example from the design doc:
// supersession=none wins (preserves journal), curation=standard
// wins (knowledge wants analysis), contradictions=on wins (knowledge
// wants fact-checking). Pinning the principle in one shot.
func TestEffectiveCurationCombinedScenario(t *testing.T) {
	g := graph.New()
	journal := addCollection(t, g, "standard", "none", "off")
	knowledge := addCollection(t, g, "standard", "collection", "on")
	rec := addRecord(t, g, journal, knowledge)

	got := EffectiveCurationFor(g, rec)
	want := EffectiveConfig{
		Curation:       "standard",
		Supersession:   "none",
		Contradictions: "on",
	}
	if got != want {
		t.Errorf("journal+knowledge: got %+v, want %+v", got, want)
	}
}
