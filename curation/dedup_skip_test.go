package curation

import (
	"testing"
	"time"

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

// TestDedupSkipsCollectionMembers asserts that collection items
// (records with a member_of edge) are not auto-consolidated.
// Different bugs / tasks in the same collection often embed
// similarly but represent distinct tracked work.
func TestDedupSkipsCollectionMembers(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Dedup.SimilarityThreshold = 0.5

	now := time.Now().UTC()
	vec := []float32{0.9, 0.1, 0.0}

	eng.Lock()
	coll := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("Bug collection"),
		"node_type":    graph.StringProperty("collection"),
	})

	mk := func(title string, ageMin int) string {
		n := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty("Wire limit from config to validation path"),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(now.Add(-time.Duration(ageMin) * time.Minute)),
			"embedding_full":    graph.VectorProperty(vec),
			"field.title":       graph.StringProperty(title),
		})
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
		eng.VecIdx().Add(n.ID, vec)
		eng.Graph().AddEdge(n.ID, coll.ID, "member_of", 1.0, nil)
		return n.ID
	}

	olderID := mk("Wire LimitsConfig through to validation", 60)
	newerID := mk("Wire MaxJSONSize from LimitsConfig", 10)

	eng.Save("test")
	eng.Unlock()

	RunDeterministic(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	older, _ := eng.Graph().GetNode(olderID)
	if _, ok := older.Properties.GetTimestamp("valid_until"); ok {
		t.Fatal("older collection item was superseded -- collection members must not auto-consolidate")
	}
	newer, _ := eng.Graph().GetNode(newerID)
	if _, ok := newer.Properties.GetTimestamp("valid_until"); ok {
		t.Fatal("newer collection item was superseded -- collection members must not auto-consolidate")
	}
}
