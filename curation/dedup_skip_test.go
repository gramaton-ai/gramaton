package curation

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// TestDedupSkipsObservationNodes asserts that a record marked
// node_type=observation cannot supersede another record via auto-
// supersession, even when cosine + Jaccard agree.
func TestDedupSkipsObservationNodes(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	now := time.Now().UTC()
	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	parent := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Every call site attaches a task label via telemetry"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now.Add(-10 * time.Minute)),
		"embedding_full":    graph.VectorProperty(vec),
	})
	for k, v := range parent.Properties {
		eng.PropIdx().Add(parent.ID, k, v)
	}
	eng.VecIdx().Add(parent.ID, vec)

	obs := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Every call site attaches a task label via telemetry"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"embedding_full":    graph.VectorProperty(vec),
		"node_type":         graph.StringProperty("observation"),
	})
	for k, v := range obs.Properties {
		eng.PropIdx().Add(obs.ID, k, v)
	}
	eng.VecIdx().Add(obs.ID, vec)

	// Deliberately omit the observation_of edge so we verify the
	// defense-in-depth node_type check, independent of FindDuplicates'
	// edge filter.
	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	p, _ := eng.Graph().GetNode(parent.ID)
	if _, ok := p.Properties.GetTimestamp("valid_until"); ok {
		t.Fatal("parent record was superseded by an observation -- dedup must skip observation nodes")
	}
}

// dedupTestRecord seeds a record with the given content + vector
// and registers it on the engine's prop and vector indexes so the
// dedup pass can find it. Returns the record's ID.
func dedupTestRecord(t *testing.T, eng *core.Engine, content string, ageMin int, vec []float32, collectionIDs ...string) string {
	t.Helper()
	now := time.Now().UTC()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now.Add(-time.Duration(ageMin) * time.Minute)),
		"embedding_full":    graph.VectorProperty(vec),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.VecIdx().Add(n.ID, vec)
	for _, cID := range collectionIDs {
		if _, err := eng.Graph().AddEdge(n.ID, cID, "member_of", 1.0, nil); err != nil {
			t.Fatalf("AddEdge member_of: %v", err)
		}
	}
	return n.ID
}

// dedupTestCollection creates a collection node with the given
// supersession knob. Empty supersession leaves the property unset
// (collection-default applies).
func dedupTestCollection(t *testing.T, eng *core.Engine, supersession string) string {
	t.Helper()
	props := graph.Properties{
		"knowledge_type": graph.StringProperty("collection"),
	}
	if supersession != "" {
		props["collection_supersession"] = graph.StringProperty(supersession)
	}
	return eng.Graph().AddNode(props).ID
}

// TestDedupSameCollectionSupersedes pins the within-collection
// dedup contract. Two records sharing a collection (default
// supersession=collection scope) at high cosine + Jaccard agreement
// should consolidate. This is the shopping-list-shape behaviour --
// adding "milk" twice marks the older entry historical.
func TestDedupSameCollectionSupersedes(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	coll := dedupTestCollection(t, eng, "")
	olderID := dedupTestRecord(t, eng, "buy milk for the week", 60, vec, coll)
	newerID := dedupTestRecord(t, eng, "buy milk for the week", 5, vec, coll)
	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	older, _ := eng.Graph().GetNode(olderID)
	if _, ok := older.Properties.GetTimestamp("valid_until"); !ok {
		t.Errorf("same-collection older record should have been superseded; got valid_until unset")
	}
	newer, _ := eng.Graph().GetNode(newerID)
	if _, ok := newer.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("newer record should NOT be superseded; got valid_until set")
	}
}

// TestDedupCrossCollectionDoesNotSupersede pins the original Phase
// 5 bug fix. Two records in DIFFERENT collections, both at the
// collection-default supersession=collection scope, must not
// supersede each other even when cosine + Jaccard agree. "eggs"
// in Grocery does not destroy "eggs" in Recipe Book.
func TestDedupCrossCollectionDoesNotSupersede(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	groceries := dedupTestCollection(t, eng, "")
	recipes := dedupTestCollection(t, eng, "")
	olderID := dedupTestRecord(t, eng, "eggs", 60, vec, groceries)
	newerID := dedupTestRecord(t, eng, "eggs", 5, vec, recipes)
	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	older, _ := eng.Graph().GetNode(olderID)
	if _, ok := older.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("cross-collection record was superseded across collections; valid_until should be unset")
	}
	newer, _ := eng.Graph().GetNode(newerID)
	if _, ok := newer.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("cross-collection newer record was superseded; valid_until should be unset")
	}
}

// TestDedupOrphanRecordsSupersede pins that records with no
// member_of edges (Memory orphans) still consolidate via the
// global-store path. supersession=store on both, no scoping
// constraint. Preserves today's Memory record dedup behaviour.
func TestDedupOrphanRecordsSupersede(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	olderID := dedupTestRecord(t, eng, "team chose Postgres on April 14", 60, vec)
	newerID := dedupTestRecord(t, eng, "team chose Postgres on April 14", 5, vec)
	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	older, _ := eng.Graph().GetNode(olderID)
	if _, ok := older.Properties.GetTimestamp("valid_until"); !ok {
		t.Errorf("memory orphan older record should have been superseded; got valid_until unset")
	}
	newer, _ := eng.Graph().GetNode(newerID)
	if _, ok := newer.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("memory orphan newer record should NOT be superseded; got valid_until set")
	}
}

// TestDedupSupersessionNoneSkips pins the opt-out contract. A
// collection with supersession=none disables auto-supersession for
// its members regardless of similarity. Use case: journal entries,
// observation logs.
func TestDedupSupersessionNoneSkips(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	journal := dedupTestCollection(t, eng, "none")
	olderID := dedupTestRecord(t, eng, "felt anxious about work", 60, vec, journal)
	newerID := dedupTestRecord(t, eng, "felt anxious about work", 5, vec, journal)
	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	older, _ := eng.Graph().GetNode(olderID)
	if _, ok := older.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("supersession=none collection: older record was superseded; valid_until should be unset")
	}
	newer, _ := eng.Graph().GetNode(newerID)
	if _, ok := newer.Properties.GetTimestamp("valid_until"); ok {
		t.Errorf("supersession=none collection: newer record was superseded; valid_until should be unset")
	}
}
