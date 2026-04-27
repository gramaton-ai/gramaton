package curation

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
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

	// Capture commit hash after first run. HeadHash takes its own
	// read lock; the *Locked variant requires the caller to already
	// hold one, which we don't here.
	firstHead := eng.HeadHash()

	// Second run: graph unchanged. Pre-fix, this would have re-written
	// the concept's properties anyway because count > 0. Post-fix,
	// no update should fire and the commit chain should not advance.
	enrichConcepts(eng, nil)

	secondHead := eng.HeadHash()
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

// TestEnrichConceptSynthesesEmbedsConcept pins the fix for
// 01KQ60N4ZCCQDKM17XWQMZAX9C: concept nodes were created during
// emergence with nil vectors and only got embeddings when
// `gramaton reembed` caught up. Concept telemetry and PRF were
// blind for any concept the reembed pipeline had not yet processed.
// The synthesis flow now embeds each completed concept inline and
// registers it in the vec index.
func TestEnrichConceptSynthesesEmbedsConcept(t *testing.T) {
	emb := &configurableObsEmbedder{dim: 3}

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithEmbedder(emb),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	now := time.Now().UTC()
	cfg = eng.Config()

	eng.Lock()
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("(template) kafka concept"),
		"content_short":     graph.StringProperty("kafka concept"),
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("kafka"),
		"synthesis_status":  graph.StringProperty("pending"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}
	member := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Kafka member discussing pub-sub semantics"),
		"content_short":     graph.StringProperty("Kafka pub-sub member"),
		"processing_status": graph.StringProperty("processed"),
		"epistemic_status":  graph.StringProperty("well_established"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range member.Properties {
		eng.PropIdx().Add(member.ID, k, v)
	}
	eng.Graph().AddEdge(member.ID, concept.ID, "instance_of", 1.0, nil)
	eng.Save("seed")
	eng.Unlock()

	llm := &mockLLM{responses: []string{
		`[{"keyword":"kafka","synthesis":"Kafka is the backbone of our event pipeline."}]`,
	}}
	result := &AutonomousResult{}
	enrichConceptSyntheses(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)

	if result.ConceptsCreated != 1 {
		t.Fatalf("ConceptsCreated = %d, want 1 (synthesis should have applied)", result.ConceptsCreated)
	}

	eng.RLock()
	defer eng.RUnlock()
	c, _ := eng.Graph().GetNode(concept.ID)
	vec, ok := c.Properties.GetVector("embedding_full")
	if !ok {
		t.Fatal("embedding_full not set on concept after enrichment (the bug)")
	}
	if len(vec) != 3 {
		t.Errorf("embedding_full dim = %d, want 3", len(vec))
	}
	model, _ := c.Properties.GetString("embedding_model")
	if model != "configurable-obs-embedder" {
		t.Errorf("embedding_model = %q, want configurable-obs-embedder", model)
	}
	if eng.VecIdx().Len() == 0 {
		t.Error("vec index empty after enrichment; concept not registered")
	}
}

// TestEnrichConceptsExcludesObservationSources pins the
// observation-vs-parent double-counting fix
// (01KQ62W3EPCRM4ARQG85AQP94S). Pre-fix, evidence_count counted
// every inbound non-structural edge -- including instance_of
// edges sourced from observations (which inherit their parent's
// content_keywords and so get pulled into the same emergence
// cluster). The audit on this store flagged concepts where
// evidence_count = parent_count + observation_count rather than
// parent_count alone. Post-fix, enrichConcepts skips edges whose
// source is an observation node.
func TestEnrichConceptsExcludesObservationSources(t *testing.T) {
	eng := setupEngine(t)

	now := time.Now().UTC()

	eng.Lock()
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Auth concept synthesis"),
		"content_short":     graph.StringProperty("auth concept synthesis content"),
		"processing_status": graph.StringProperty("processed"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("auth"),
		"synthesis_status":  graph.StringProperty("complete"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	parent := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Parent record about auth"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now.Add(-1 * time.Hour)),
	})
	for k, v := range parent.Properties {
		eng.PropIdx().Add(parent.ID, k, v)
	}
	eng.Graph().AddEdge(parent.ID, concept.ID, "instance_of", 1.0, nil)

	for i := 0; i < 2; i++ {
		obs := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty("Observation excerpt about auth"),
			"processing_status": graph.StringProperty("processed"),
			"node_type":         graph.StringProperty("observation"),
			"temporality":       graph.StringProperty("durable"),
			"created_at":        graph.TimestampProperty(now.Add(-30 * time.Minute)),
		})
		for k, v := range obs.Properties {
			eng.PropIdx().Add(obs.ID, k, v)
		}
		eng.Graph().AddEdge(obs.ID, parent.ID, "observation_of", 1.0, nil)
		eng.Graph().AddEdge(obs.ID, concept.ID, "instance_of", 1.0, nil)
	}

	eng.Save("seed")
	eng.Unlock()

	enrichConcepts(eng, nil)

	eng.RLock()
	defer eng.RUnlock()
	c, _ := eng.Graph().GetNode(concept.ID)
	ec, _ := c.Properties.GetInt64("evidence_count")
	if ec != 1 {
		t.Errorf("evidence_count = %d, want 1 (parent only; observations excluded)", ec)
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
