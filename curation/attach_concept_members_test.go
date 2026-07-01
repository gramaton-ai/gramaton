package curation

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// The tests below pin the fix for issue #99: concept nodes never
// accumulated evidence after creation. The candidate-emission loop
// skipped any keyword that already had a concept node, so records
// captured after emergence never got an instance_of edge and
// evidence_count could only hold or shrink.
//
// Store-shape note: the candidate pre-filter compares the RAW keyword
// count (which includes the concept node itself -- concepts carry
// their keyword in content_keywords) against maxCount, and in tiny
// stores maxCount collapses to TotalRecords (concepts excluded). A
// store where every record shares the keyword therefore trips the
// corpus-vocabulary ceiling once the concept exists. The filler
// records in these tests keep TotalRecords above the raw count, which
// matches real stores where no single keyword covers every record.

// inboundInstanceOfSources returns the source IDs of all inbound
// instance_of edges on a node.
func inboundInstanceOfSources(t *testing.T, eng *core.Engine, conceptID string) []string {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	var sources []string
	for _, edge := range eng.Graph().EdgesTo(conceptID) {
		if edge.Type == "instance_of" {
			sources = append(sources, edge.SourceID)
		}
	}
	return sources
}

// findConceptByKeyword returns the ID of the concept node whose
// concept_keyword matches, or "" if none exists.
func findConceptByKeyword(t *testing.T, eng *core.Engine, keyword string) string {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	it := eng.Graph().NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		if nt, _ := n.Properties.GetString("node_type"); nt != "concept" {
			continue
		}
		if kw, _ := n.Properties.GetString("concept_keyword"); kw == keyword {
			return n.ID
		}
	}
	return ""
}

// TestConceptAttachesNewMembersAfterEmergence is the core regression
// for issue #99: a concept emerges at the threshold, later records
// share the keyword, and the next cycle must link them via
// instance_of edges and grow evidence_count.
func TestConceptAttachesNewMembersAfterEmergence(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()

	// Filler records with unshared keywords (see store-shape note).
	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)

	// Three records at the emergence threshold.
	for i := 0; i < 3; i++ {
		addNode(t, eng, "Kafka seed record", "durable", 0.9,
			[]string{"kafka"}, now.Add(-time.Duration(i)*time.Hour))
	}

	first := RunDeterministic(eng, cfg, nil)
	if first.ConceptsCreated != 1 {
		t.Fatalf("cycle 1 ConceptsCreated = %d, want 1", first.ConceptsCreated)
	}
	if first.ConceptMembersAttached != 0 {
		t.Errorf("cycle 1 ConceptMembersAttached = %d, want 0 (emergence links members itself)", first.ConceptMembersAttached)
	}
	conceptID := findConceptByKeyword(t, eng, "kafka")
	if conceptID == "" {
		t.Fatal("no concept node with concept_keyword=kafka after cycle 1")
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptID)); got != 3 {
		t.Fatalf("cycle 1 inbound instance_of edges = %d, want 3", got)
	}

	// Two more records sharing the keyword, captured after emergence.
	// Pre-fix these never got instance_of edges.
	var lateIDs []string
	for i := 0; i < 2; i++ {
		id := addNode(t, eng, "Kafka late record", "durable", 0.9,
			[]string{"kafka"}, now)
		lateIDs = append(lateIDs, id)
	}

	second := RunDeterministic(eng, cfg, nil)
	if second.ConceptsCreated != 0 {
		t.Errorf("cycle 2 ConceptsCreated = %d, want 0", second.ConceptsCreated)
	}
	if second.ConceptMembersAttached != 2 {
		t.Errorf("cycle 2 ConceptMembersAttached = %d, want 2", second.ConceptMembersAttached)
	}

	sources := inboundInstanceOfSources(t, eng, conceptID)
	if len(sources) != 5 {
		t.Fatalf("cycle 2 inbound instance_of edges = %d, want 5", len(sources))
	}
	sourceSet := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		sourceSet[s] = struct{}{}
	}
	for _, id := range lateIDs {
		if _, ok := sourceSet[id]; !ok {
			t.Errorf("late record %s has no instance_of edge to the concept", id)
		}
	}

	// enrichConcepts runs after the write batch in the same cycle, so
	// evidence_count reflects the new edges immediately.
	eng.RLock()
	c, _ := eng.Graph().GetNode(conceptID)
	ec, _ := c.Properties.GetInt64("evidence_count")
	eng.RUnlock()
	if ec != 5 {
		t.Errorf("evidence_count = %d, want 5 after attachment + enrichment", ec)
	}
}

// TestConceptAttachesMembersMatchingAliasKeyword covers the alias
// path: the new records' keyword matches an entry in the concept's
// content_keywords rather than its primary concept_keyword. The
// attachment must resolve through the alias to the same concept.
func TestConceptAttachesMembersMatchingAliasKeyword(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()

	// Three linked members carrying the primary keyword.
	var memberIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "Primary keyword member", "durable", 0.9,
			[]string{"primarykw"}, now.Add(-time.Duration(i)*time.Hour))
		memberIDs = append(memberIDs, id)
	}

	// Pre-existing concept whose content_keywords carries "aliaskw"
	// alongside the primary (as the Phase F alias-merge path writes).
	eng.Lock()
	conceptNode := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Concept: primarykw."),
		"content_short":     graph.StringProperty("primarykw cluster"),
		"content_keywords":  graph.StringListProperty([]string{"primarykw", "aliaskw"}),
		"processing_status": graph.StringProperty("processed"),
		"synthesis_status":  graph.StringProperty("complete"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("primarykw"),
		"temporality":       graph.StringProperty("durable"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range conceptNode.Properties {
		eng.PropIdx().Add(conceptNode.ID, k, v)
	}
	for _, mid := range memberIDs {
		eng.Graph().AddEdge(mid, conceptNode.ID, "instance_of", 0.8, nil)
	}
	eng.Save("seed concept")
	eng.Unlock()

	// Three new records carrying ONLY the alias keyword.
	var aliasIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "Alias keyword record", "durable", 0.9,
			[]string{"aliaskw"}, now)
		aliasIDs = append(aliasIDs, id)
	}

	result := RunDeterministic(eng, cfg, nil)
	if result.ConceptsCreated != 0 {
		t.Errorf("ConceptsCreated = %d, want 0 (alias resolves to existing concept)", result.ConceptsCreated)
	}
	if result.ConceptMembersAttached != 3 {
		t.Errorf("ConceptMembersAttached = %d, want 3", result.ConceptMembersAttached)
	}

	sources := inboundInstanceOfSources(t, eng, conceptNode.ID)
	if len(sources) != 6 {
		t.Fatalf("inbound instance_of edges = %d, want 6 (3 original + 3 alias-matched)", len(sources))
	}
	sourceSet := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		sourceSet[s] = struct{}{}
	}
	for _, id := range aliasIDs {
		if _, ok := sourceSet[id]; !ok {
			t.Errorf("alias-matched record %s has no instance_of edge to the concept", id)
		}
	}

	eng.RLock()
	c, _ := eng.Graph().GetNode(conceptNode.ID)
	ec, _ := c.Properties.GetInt64("evidence_count")
	eng.RUnlock()
	if ec != 6 {
		t.Errorf("evidence_count = %d, want 6 after attachment + enrichment", ec)
	}
}

// TestConceptAttachmentIsIdempotent verifies that re-running the
// cycle after an attachment adds no duplicate instance_of edges: the
// read phase diffs candidate members against already-linked members,
// so an unchanged store yields zero attachments.
func TestConceptAttachmentIsIdempotent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()

	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)
	for i := 0; i < 3; i++ {
		addNode(t, eng, "Kafka seed record", "durable", 0.9,
			[]string{"kafka"}, now.Add(-time.Duration(i)*time.Hour))
	}

	first := RunDeterministic(eng, cfg, nil)
	if first.ConceptsCreated != 1 {
		t.Fatalf("cycle 1 ConceptsCreated = %d, want 1", first.ConceptsCreated)
	}
	conceptID := findConceptByKeyword(t, eng, "kafka")
	if conceptID == "" {
		t.Fatal("no concept node with concept_keyword=kafka after cycle 1")
	}

	addNode(t, eng, "Kafka late record", "durable", 0.9, []string{"kafka"}, now)

	second := RunDeterministic(eng, cfg, nil)
	if second.ConceptMembersAttached != 1 {
		t.Fatalf("cycle 2 ConceptMembersAttached = %d, want 1", second.ConceptMembersAttached)
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptID)); got != 4 {
		t.Fatalf("cycle 2 inbound instance_of edges = %d, want 4", got)
	}

	// Third cycle on an unchanged store: no new edges.
	third := RunDeterministic(eng, cfg, nil)
	if third.ConceptMembersAttached != 0 {
		t.Errorf("cycle 3 ConceptMembersAttached = %d, want 0", third.ConceptMembersAttached)
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptID)); got != 4 {
		t.Errorf("cycle 3 inbound instance_of edges = %d, want 4 (no duplicates)", got)
	}
}
