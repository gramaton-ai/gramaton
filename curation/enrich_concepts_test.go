package curation

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestEnrichConceptsSkipsRedundantUpdates is the load-bearing
// regression for P2-07 fix #3. Pre-fix, the update gate was
// `count != existingCount || count > 0`, which always fired once a
// concept had any inbound edge — so every concept with evidence got
// re-written every cycle even when nothing had changed. Post-fix:
// only update when evidence_count changed OR last_evidence_at
// drifted forward.
//
// This test seeds a concept with one inbound edge, runs enrichment
// once to set the baseline metadata, then runs enrichment again
// without modifying the graph — the second run should produce zero
// updates.
func TestEnrichConceptsSkipsRedundantUpdates(t *testing.T) {
	eng := setupEngine(t)

	now := time.Now().UTC()

	eng.Lock()
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Authentication concept"),
		"content_short":     graph.StringProperty("auth"),
		"processing_status": graph.StringProperty("processed"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("auth"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	member := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("JWT details"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now.Add(-1 * time.Hour)),
	})
	for k, v := range member.Properties {
		eng.PropIdx().Add(member.ID, k, v)
	}
	eng.Graph().AddEdge(member.ID, concept.ID, "instance_of", 1.0, nil)
	eng.Save("seed")
	eng.Unlock()

	// First run: writes evidence_count=1, last_evidence_at=member.created_at.
	enrichConcepts(eng, nil)

	// Capture commit hash after first run.
	firstHead := eng.HeadHashLocked()

	// Second run: graph unchanged. Pre-fix, this would have re-written
	// the concept's properties anyway because count > 0. Post-fix,
	// no update should fire and the commit chain should not advance.
	enrichConcepts(eng, nil)

	secondHead := eng.HeadHashLocked()
	if firstHead != secondHead {
		t.Errorf("redundant enrichConcepts re-wrote the concept; expected commit chain stable\n  first=%s\n  second=%s",
			firstHead, secondHead)
	}

	// Sanity: the concept does have evidence_count=1 from the first run.
	eng.RLock()
	defer eng.RUnlock()
	c, _ := eng.Graph().GetNode(concept.ID)
	ec, _ := c.Properties.GetInt64("evidence_count")
	if ec != 1 {
		t.Errorf("evidence_count = %d, want 1", ec)
	}
}

// TestEnrichConceptsUpdatesWhenLatestEvidenceDrifts pins the
// secondary trigger: even if count is unchanged, a new edge from a
// source with a created_at later than last_evidence_at should still
// fire an update so the timestamp reflects the latest signal.
func TestEnrichConceptsUpdatesWhenLatestEvidenceDrifts(t *testing.T) {
	eng := setupEngine(t)

	now := time.Now().UTC()

	eng.Lock()
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Auth concept"),
		"processing_status": graph.StringProperty("processed"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("auth"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"evidence_count":    graph.Int64Property(1),
		"last_evidence_at":  graph.TimestampProperty(now.Add(-2 * time.Hour)),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	// Seed an inbound edge from a record dated AFTER the stored
	// last_evidence_at. count stays 1 (matches existingCount), but
	// latestEvidence drifts forward — should trigger update.
	source := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("recent auth note"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now.Add(-30 * time.Minute)), // newer than last_evidence_at
	})
	for k, v := range source.Properties {
		eng.PropIdx().Add(source.ID, k, v)
	}
	eng.Graph().AddEdge(source.ID, concept.ID, "instance_of", 1.0, nil)
	eng.Save("seed")
	eng.Unlock()

	enrichConcepts(eng, nil)

	eng.RLock()
	defer eng.RUnlock()
	c, _ := eng.Graph().GetNode(concept.ID)
	gotLatest, _ := c.Properties.GetTimestamp("last_evidence_at")
	if !gotLatest.After(now.Add(-1 * time.Hour)) {
		t.Errorf("last_evidence_at should have advanced past now-1h on update; got %v", gotLatest)
	}
}
