package curation

import (
	"sort"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestEmergeOverlapGateMergesAliasesIntoNewConcept pins Phase F's
// behavior on the duplicate-burst case. Three records share three
// content keywords; without the overlap gate three concepts emerged
// on the same evidence set ("TZ-fragile tests", "parseDateArg UTC
// midnight", "test timezone bugs"-style fragmentation). Post-fix
// only one concept emits, with the peer keywords folded into its
// content_keywords.
func TestEmergeOverlapGateMergesAliasesIntoNewConcept(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	keywords := []string{"alphapattern", "betaconcept", "gammaidea"}

	// Three records, each carrying all three keywords. With the
	// emergence threshold of 3 each keyword crosses on the same
	// evidence set, producing three candidates whose member sets
	// are identical.
	for i := 0; i < 3; i++ {
		addNode(t, eng, "record content for emerge gate test", "durable",
			0.9, keywords, now.Add(-time.Duration(i)*time.Hour))
	}

	result := RunDeterministic(eng, cfg, nil)

	if result.ConceptsCreated != 1 {
		t.Errorf("ConceptsCreated: got %d, want 1 (overlap gate should fold peers into one concept)", result.ConceptsCreated)
	}

	// Find the created concept and verify it carries every alias.
	eng.RLock()
	defer eng.RUnlock()
	var concept *graph.Node
	it := eng.Graph().NodeIterator()
	for it.Next() {
		n := it.Node()
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			concept = n
			break
		}
	}
	it.Close()
	if concept == nil {
		t.Fatal("expected one concept node, got none")
	}

	// System-created node: carries the curation author constant, never
	// the operator's configured identity.
	if author, ok := concept.Properties.GetString("author"); !ok || author != nodeAuthorCuration {
		t.Errorf("concept author = %q (present=%v), want %q", author, ok, nodeAuthorCuration)
	}

	got, _ := concept.Properties.GetStringList("content_keywords")
	gotSet := make(map[string]struct{}, len(got))
	for _, k := range got {
		gotSet[k] = struct{}{}
	}
	for _, want := range keywords {
		if _, ok := gotSet[want]; !ok {
			sort.Strings(got)
			t.Errorf("concept content_keywords missing %q; got %v", want, got)
		}
	}
}

// TestEmergeOverlapGateMergesAliasIntoExistingConcept verifies the
// alias path against an already-stored concept. A concept with primary
// keyword "primarykw" and three members exists. New evidence puts a
// peer keyword "aliaskw" on the same three records; the gate must
// append "aliaskw" to the existing concept's content_keywords rather
// than spawn a new concept node.
func TestEmergeOverlapGateMergesAliasIntoExistingConcept(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	// Three records carrying both keywords.
	var memberIDs []string
	for i := 0; i < 3; i++ {
		id := addNode(t, eng, "record content for alias merge", "durable", 0.9,
			[]string{"primarykw", "aliaskw"}, now.Add(-time.Duration(i)*time.Hour))
		memberIDs = append(memberIDs, id)
	}

	// Pre-existing concept with primary "primarykw" and only "primarykw"
	// in content_keywords (so "aliaskw" is genuinely new from the
	// gate's perspective).
	eng.Lock()
	conceptNode := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Concept: primarykw."),
		"content_short":     graph.StringProperty("primarykw cluster"),
		"content_keywords":  graph.StringListProperty([]string{"primarykw"}),
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

	result := RunDeterministic(eng, cfg, nil)

	if result.ConceptsCreated != 0 {
		t.Errorf("ConceptsCreated: got %d, want 0 (alias should fold into existing)", result.ConceptsCreated)
	}
	if result.ConceptsAliased != 1 {
		t.Errorf("ConceptsAliased: got %d, want 1", result.ConceptsAliased)
	}

	eng.RLock()
	defer eng.RUnlock()
	updated, _ := eng.Graph().GetNode(conceptNode.ID)
	got, _ := updated.Properties.GetStringList("content_keywords")
	hasAlias := false
	for _, k := range got {
		if k == "aliaskw" {
			hasAlias = true
			break
		}
	}
	if !hasAlias {
		t.Errorf("existing concept content_keywords missing aliaskw; got %v", got)
	}
}

// TestEmergeOverlapGateZeroDisablesGate is the legacy escape hatch:
// MemberOverlapThreshold=0 reverts to pre-Phase-F behavior where every
// candidate keyword emits its own concept regardless of member overlap.
// Operators who were relying on the eager emission can opt out.
func TestEmergeOverlapGateZeroDisablesGate(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.MemberOverlapThreshold = 0

	now := time.Now().UTC()
	keywords := []string{"alphapattern", "betaconcept"}

	for i := 0; i < 3; i++ {
		addNode(t, eng, "record content for legacy mode", "durable",
			0.9, keywords, now.Add(-time.Duration(i)*time.Hour))
	}

	result := RunDeterministic(eng, cfg, nil)

	if result.ConceptsCreated != 2 {
		t.Errorf("ConceptsCreated: got %d, want 2 (gate disabled, both keywords emit)", result.ConceptsCreated)
	}
	if result.ConceptsAliased != 0 {
		t.Errorf("ConceptsAliased: got %d, want 0", result.ConceptsAliased)
	}
}
