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
// path: the new record's keyword matches an entry in the concept's
// content_keywords rather than its primary concept_keyword. Because
// content_keywords stores Phase F aliases and co-occurring "related
// terms" indistinguishably, non-primary attachment is gated by the
// member-overlap (Jaccard) threshold -- so the fixture mirrors a
// genuine alias's shape: the members themselves carry the alias
// keyword (the population overlap is what admitted the alias in
// Phase F to begin with).
func TestConceptAttachesMembersMatchingAliasKeyword(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3
	cfg.Concepts.MemberOverlapThreshold = 0.6

	now := time.Now().UTC()

	// Filler records with unshared keywords (see store-shape note).
	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)

	// Three linked members carrying the primary keyword AND the alias
	// (a merged alias's population overlaps the concept's members).
	var memberIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "Primary keyword member", "durable", 0.9,
			[]string{"primarykw", "aliaskw"}, now.Add(-time.Duration(i)*time.Hour))
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

	// One new record carrying ONLY the alias keyword. The alias
	// candidate's live population is then {3 members, this record}:
	// Jaccard vs the member set = 3/4 = 0.75 > 0.6, so the gate
	// admits the attachment.
	aliasID := addNode(t, eng, "Alias keyword record", "durable", 0.9,
		[]string{"aliaskw"}, now)

	result := RunDeterministic(eng, cfg, nil)
	if result.ConceptsCreated != 0 {
		t.Errorf("ConceptsCreated = %d, want 0 (alias resolves to existing concept)", result.ConceptsCreated)
	}
	if result.ConceptMembersAttached != 1 {
		t.Errorf("ConceptMembersAttached = %d, want 1", result.ConceptMembersAttached)
	}

	sources := inboundInstanceOfSources(t, eng, conceptNode.ID)
	if len(sources) != 4 {
		t.Fatalf("inbound instance_of edges = %d, want 4 (3 original + 1 alias-matched)", len(sources))
	}
	sourceSet := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		sourceSet[s] = struct{}{}
	}
	if _, ok := sourceSet[aliasID]; !ok {
		t.Errorf("alias-matched record %s has no instance_of edge to the concept", aliasID)
	}

	eng.RLock()
	c, _ := eng.Graph().GetNode(conceptNode.ID)
	ec, _ := c.Properties.GetInt64("evidence_count")
	eng.RUnlock()
	if ec != 4 {
		t.Errorf("evidence_count = %d, want 4 after attachment + enrichment", ec)
	}
}

// TestConceptDoesNotAttachViaCoOccurringKeyword pins the guard on the
// non-primary attachment path: content_keywords also carries the
// co-occurring "related terms" stamped at emergence, and records
// sharing only such a term must NOT become instances of the concept.
// Shape: a "kafka" concept emerges with "messaging" recorded as a
// co-occurring keyword (2 of 3 seeds carry it); unrelated records
// tagged only "messaging" then cross the candidate threshold. Their
// population barely overlaps the concept's members (Jaccard 2/5 =
// 0.4 <= 0.6), so the gate must reject the attachment -- pre-gate,
// they would have been permanently linked as false evidence.
func TestConceptDoesNotAttachViaCoOccurringKeyword(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3
	cfg.Concepts.MemberOverlapThreshold = 0.6

	now := time.Now().UTC()

	// Filler records with unshared keywords (see store-shape note).
	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)

	// Three kafka seeds; two also carry "messaging" so emergence
	// records it as a co-occurring keyword (below the emergence
	// threshold itself at cycle 1).
	addNode(t, eng, "Kafka seed record", "durable", 0.9, []string{"kafka"}, now.Add(-3*time.Hour))
	addNode(t, eng, "Kafka seed record two", "durable", 0.9, []string{"kafka", "messaging"}, now.Add(-2*time.Hour))
	addNode(t, eng, "Kafka seed record three", "durable", 0.9, []string{"kafka", "messaging"}, now.Add(-time.Hour))

	first := RunDeterministic(eng, cfg, nil)
	if first.ConceptsCreated != 1 {
		t.Fatalf("cycle 1 ConceptsCreated = %d, want 1", first.ConceptsCreated)
	}
	conceptID := findConceptByKeyword(t, eng, "kafka")
	if conceptID == "" {
		t.Fatal("no concept node with concept_keyword=kafka after cycle 1")
	}
	eng.RLock()
	cn, _ := eng.Graph().GetNode(conceptID)
	kws, _ := cn.Properties.GetStringList("content_keywords")
	eng.RUnlock()
	hasCoKW := false
	for _, kw := range kws {
		if kw == "messaging" {
			hasCoKW = true
		}
	}
	if !hasCoKW {
		t.Fatalf("fixture broke: concept content_keywords %v missing co-occurring %q", kws, "messaging")
	}

	// Two unrelated records tagged only with the co-occurring keyword,
	// pushing it over the candidate threshold (2 seeds + 2 new = 4).
	addNode(t, eng, "Unrelated messaging record", "durable", 0.9, []string{"messaging"}, now)
	addNode(t, eng, "Unrelated messaging record two", "durable", 0.9, []string{"messaging"}, now)

	second := RunDeterministic(eng, cfg, nil)
	if second.ConceptMembersAttached != 0 {
		t.Errorf("cycle 2 ConceptMembersAttached = %d, want 0 (co-occurring keyword must not attach)", second.ConceptMembersAttached)
	}
	if second.ConceptsCreated != 0 {
		t.Errorf("cycle 2 ConceptsCreated = %d, want 0 (co-occurring keyword is claimed by the existing concept)", second.ConceptsCreated)
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptID)); got != 3 {
		t.Fatalf("cycle 2 inbound instance_of edges = %d, want 3 (unchanged)", got)
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

// TestConceptStampedCoKeywordNeverAttaches pins the provenance fix
// for the trickle-ratchet residual: a co-occurring keyword stamped at
// emergence (below the candidate threshold then, so never Phase-F
// evaluated) must never attach evidence, regardless of what the
// live-population Jaccard says. The Jaccard gate alone is
// arrival-timing sensitive: under a permissive threshold, or after
// membership decay (superseded/GC'd members shrink the denominator),
// trickle arrivals pass it one record per cycle and each false attach
// raises J for the next. The fixture uses a permissive threshold
// (0.3) under which the stamped co-term's first arrival would pass
// the gate (J = 2/4 = 0.5 > 0.3) if provenance didn't skip it
// structurally.
//
// (A co-term covering ALL members at emergence can't be stamped: its
// count matches the primary's, so it reaches candidacy and is folded
// as a Phase F alias at full overlap instead -- alias semantics own
// that case by construction.)
func TestConceptStampedCoKeywordNeverAttaches(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3
	cfg.Concepts.MemberOverlapThreshold = 0.3

	now := time.Now().UTC()

	// Filler records with unshared keywords (see store-shape note).
	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)
	addNode(t, eng, "Filler record three", "durable", 0.9, []string{"fillerthree"}, now)

	// Three kafka seeds; two carry "messaging" (count 2 < threshold 3
	// at cycle 1, so it is stamped as a co-occurring keyword, not
	// Phase-F folded).
	addNode(t, eng, "Kafka seed record", "durable", 0.9, []string{"kafka"}, now.Add(-3*time.Hour))
	addNode(t, eng, "Kafka seed record two", "durable", 0.9, []string{"kafka", "messaging"}, now.Add(-2*time.Hour))
	addNode(t, eng, "Kafka seed record three", "durable", 0.9, []string{"kafka", "messaging"}, now.Add(-time.Hour))

	first := RunDeterministic(eng, cfg, nil)
	if first.ConceptsCreated != 1 {
		t.Fatalf("cycle 1 ConceptsCreated = %d, want 1", first.ConceptsCreated)
	}
	conceptID := findConceptByKeyword(t, eng, "kafka")
	if conceptID == "" {
		t.Fatal("no concept node with concept_keyword=kafka after cycle 1")
	}

	// Emergence must have persisted the co-term provenance.
	eng.RLock()
	cn, _ := eng.Graph().GetNode(conceptID)
	coKws, _ := cn.Properties.GetStringList("cooccurring_keywords")
	eng.RUnlock()
	found := false
	for _, kw := range coKws {
		if kw == "messaging" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cooccurring_keywords = %v, want to contain %q", coKws, "messaging")
	}

	// Trickle: one unrelated messaging-only record per cycle. First
	// arrival makes "messaging" a candidate (2 seeds + 1 new = 3) with
	// J = 2/4 = 0.5 > 0.3 -- the Jaccard gate alone would admit it;
	// provenance must block it every cycle.
	for cycle := 2; cycle <= 4; cycle++ {
		addNode(t, eng, "Unrelated messaging record", "durable", 0.9, []string{"messaging"}, now)
		res := RunDeterministic(eng, cfg, nil)
		if res.ConceptMembersAttached != 0 {
			t.Fatalf("cycle %d ConceptMembersAttached = %d, want 0 (co-term provenance must block attachment)", cycle, res.ConceptMembersAttached)
		}
		if res.ConceptsCreated != 0 {
			t.Errorf("cycle %d ConceptsCreated = %d, want 0 (co-term still suppresses emergence)", cycle, res.ConceptsCreated)
		}
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptID)); got != 3 {
		t.Fatalf("inbound instance_of edges = %d, want 3 (unchanged after trickle)", got)
	}
}

// TestConceptAttachRejectsAtExactThreshold pins the gate boundary on
// the legacy path (no cooccurring_keywords marker): attachment
// requires the Jaccard to strictly EXCEED member_overlap_threshold,
// matching Phase F admission semantics. Fixture arithmetic: 3 members
// all carrying the alias + 2 new alias-only records gives J = 3/5 =
// 0.6, exactly the threshold, which must be rejected.
func TestConceptAttachRejectsAtExactThreshold(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3
	cfg.Concepts.MemberOverlapThreshold = 0.6

	now := time.Now().UTC()

	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)

	var memberIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "Primary keyword member", "durable", 0.9,
			[]string{"primarykw", "aliaskw"}, now.Add(-time.Duration(i)*time.Hour))
		memberIDs = append(memberIDs, id)
	}

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

	// Two alias-only records: J = |{3 members}| / |{3 members + 2 new}|
	// = 0.6 exactly.
	addNode(t, eng, "Alias keyword record", "durable", 0.9, []string{"aliaskw"}, now)
	addNode(t, eng, "Alias keyword record two", "durable", 0.9, []string{"aliaskw"}, now)

	result := RunDeterministic(eng, cfg, nil)
	if result.ConceptMembersAttached != 0 {
		t.Errorf("ConceptMembersAttached = %d, want 0 (J == threshold must be rejected)", result.ConceptMembersAttached)
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptNode.ID)); got != 3 {
		t.Fatalf("inbound instance_of edges = %d, want 3 (unchanged)", got)
	}
}

// TestConceptAttachDisabledThresholdTurnsAliasPathOff pins the
// disable branch: member_overlap_threshold = 0 turns non-primary
// attachment off entirely (closed, not open). The fixture would
// attach at the default threshold (J = 3/4 = 0.75).
func TestConceptAttachDisabledThresholdTurnsAliasPathOff(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3
	cfg.Concepts.MemberOverlapThreshold = 0

	now := time.Now().UTC()

	addNode(t, eng, "Filler record one", "durable", 0.9, []string{"fillerone"}, now)
	addNode(t, eng, "Filler record two", "durable", 0.9, []string{"fillertwo"}, now)

	var memberIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "Primary keyword member", "durable", 0.9,
			[]string{"primarykw", "aliaskw"}, now.Add(-time.Duration(i)*time.Hour))
		memberIDs = append(memberIDs, id)
	}

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

	addNode(t, eng, "Alias keyword record", "durable", 0.9, []string{"aliaskw"}, now)

	result := RunDeterministic(eng, cfg, nil)
	if result.ConceptMembersAttached != 0 {
		t.Errorf("ConceptMembersAttached = %d, want 0 (disabled gate must turn alias attachment off)", result.ConceptMembersAttached)
	}
	if got := len(inboundInstanceOfSources(t, eng, conceptNode.ID)); got != 3 {
		t.Fatalf("inbound instance_of edges = %d, want 3 (unchanged)", got)
	}
}
