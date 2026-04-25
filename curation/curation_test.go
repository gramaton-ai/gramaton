package curation

import (
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

func setupEngine(t *testing.T) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	return eng
}

func addNode(t *testing.T, eng *core.Engine, content, temporality string, conf float64, keywords []string, createdAt time.Time) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"temporality":       graph.StringProperty(temporality),
		"confidence":        graph.Float64Property(conf),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(createdAt),
		"access_count":      graph.Int64Property(0),
	}
	if len(keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(keywords)
	}

	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

func TestDeterministicLifecycleTransitions(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	// Create an ephemeral record from a long time ago (should be stale).
	addNode(t, eng, "Old ephemeral", "ephemeral", 0.9, nil,
		now.Add(-30*24*time.Hour))

	// Create a durable record (should NOT be expired).
	addNode(t, eng, "Durable record", "durable", 0.9, nil, now)

	result := RunDeterministic(eng, cfg, nil)

	if result.LifecycleTransitions < 1 {
		t.Fatalf("expected at least 1 lifecycle transition, got %d", result.LifecycleTransitions)
	}

	// Verify valid_until was set on the ephemeral record.
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		temp, _ := n.Properties.GetString("temporality")
		if temp == "ephemeral" {
			if _, ok := n.Properties.GetTimestamp("valid_until"); !ok {
				t.Fatal("ephemeral record should have valid_until set")
			}
		}
		if temp == "durable" {
			if _, ok := n.Properties.GetTimestamp("valid_until"); ok {
				t.Fatal("durable record should NOT have valid_until set")
			}
		}
	}
}

func TestDeterministicConceptCandidates(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3

	now := time.Now().UTC()

	// Create 3 records sharing the keyword "kafka" (should trigger candidate).
	addNode(t, eng, "Kafka record 1", "durable", 0.9, []string{"kafka", "events"}, now)
	addNode(t, eng, "Kafka record 2", "durable", 0.8, []string{"kafka", "pipeline"}, now)
	addNode(t, eng, "Kafka record 3", "durable", 0.7, []string{"kafka", "streaming"}, now)

	// Create 2 records sharing "redis" (should NOT trigger, below threshold).
	addNode(t, eng, "Redis record 1", "durable", 0.9, []string{"redis", "cache"}, now)
	addNode(t, eng, "Redis record 2", "durable", 0.8, []string{"redis", "session"}, now)

	result := RunDeterministic(eng, cfg, nil)

	// Find kafka candidate.
	found := false
	for _, c := range result.ConceptCandidates {
		if c.Keyword == "kafka" {
			found = true
			if c.Count != 3 {
				t.Fatalf("expected kafka count 3, got %d", c.Count)
			}
			if len(c.NodeIDs) != 3 {
				t.Fatalf("expected 3 node IDs, got %d", len(c.NodeIDs))
			}
		}
		if c.Keyword == "redis" {
			t.Fatal("redis should not be a candidate (only 2 records)")
		}
	}
	if !found {
		t.Fatal("kafka should be a concept candidate")
	}
}

func TestDeterministicManifest(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	addNode(t, eng, "Record 1", "durable", 0.9, nil, now)
	addNode(t, eng, "Record 2", "temporal", 0.7, nil, now)

	// Add a pending record.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Pending record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)
	m := result.Manifest

	if m == nil {
		t.Fatal("manifest should not be nil")
	}
	if m.TotalRecords != 3 {
		t.Fatalf("expected 3 total records, got %d", m.TotalRecords)
	}
	if m.PendingCount != 1 {
		t.Fatalf("expected 1 pending, got %d", m.PendingCount)
	}
}

func TestConceptEnrichment(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	// Create a concept node.
	eng.Lock()
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Authentication concept"),
		"processing_status": graph.StringProperty("processed"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	// Create two records that link to the concept.
	r1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("JWT token implementation"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now.Add(-48 * time.Hour)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range r1.Properties {
		eng.PropIdx().Add(r1.ID, k, v)
	}

	r2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("OAuth 2.0 flow design"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now.Add(-24 * time.Hour)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range r2.Properties {
		eng.PropIdx().Add(r2.ID, k, v)
	}

	// Link records to concept.
	eng.Graph().AddEdge(r1.ID, concept.ID, "related_to", 0.8, nil)
	eng.Graph().AddEdge(r2.ID, concept.ID, "related_to", 0.7, nil)

	eng.Save("test")
	eng.Unlock()

	// Run deterministic curation (includes concept enrichment).
	RunDeterministic(eng, cfg, nil)

	// Verify concept was enriched.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(concept.ID)
	if !ok {
		t.Fatal("concept node should exist")
	}
	ec, ok := n.Properties.GetInt64("evidence_count")
	if !ok {
		t.Fatal("evidence_count should be set")
	}
	if ec != 2 {
		t.Fatalf("expected evidence_count 2, got %d", ec)
	}
	le, ok := n.Properties.GetTimestamp("last_evidence_at")
	if !ok {
		t.Fatal("last_evidence_at should be set")
	}
	// The most recent evidence is r2, created 24h ago.
	expectedLE := now.Add(-24 * time.Hour)
	if le.Sub(expectedLE).Abs() > time.Second {
		t.Fatalf("last_evidence_at should be ~%v, got %v", expectedLE, le)
	}
}

func TestGarbageCollectionDryRun(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = true
	cfg.GC.MinAgeDays = 1 // 1 day, records are 48h old

	// Create a record that meets ALL GC criteria.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Observed junk"),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty("ephemeral"),
		"confidence":        graph.Float64Property(0.2),
		"importance":        graph.Float64Property(0),
		"created_at":        graph.TimestampProperty(time.Now().UTC().Add(-48 * time.Hour)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.GCCollected != 1 {
		t.Fatalf("expected 1 GC candidate in dry-run, got %d", result.GCCollected)
	}
	if !result.GCDryRun {
		t.Fatal("expected GCDryRun to be true")
	}

	// Verify record still exists (dry-run doesn't delete).
	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(n.ID); !ok {
		t.Fatal("record should still exist in dry-run mode")
	}
}

func TestGarbageCollectionActive(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 1

	// GC-eligible record.
	eng.Lock()
	junk := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Junk to delete"),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty("ephemeral"),
		"confidence":        graph.Float64Property(0.1),
		"importance":        graph.Float64Property(0),
		"created_at":        graph.TimestampProperty(time.Now().UTC().Add(-48 * time.Hour)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range junk.Properties {
		eng.PropIdx().Add(junk.ID, k, v)
	}

	// Non-eligible record (classified as processed).
	keeper := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Important knowledge"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"importance":        graph.Float64Property(0.5),
		"created_at":        graph.TimestampProperty(time.Now().UTC().Add(-48 * time.Hour)),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range keeper.Properties {
		eng.PropIdx().Add(keeper.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.GCCollected != 1 {
		t.Fatalf("expected 1 GC deletion, got %d", result.GCCollected)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(junk.ID); ok {
		t.Fatal("junk record should have been deleted")
	}
	if _, ok := eng.Graph().GetNode(keeper.ID); !ok {
		t.Fatal("keeper record should still exist")
	}
}

func TestGarbageCollectionSkipsAccessedRecords(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 1

	// Record with access_count > 0 should survive.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Accessed once"),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty("ephemeral"),
		"confidence":        graph.Float64Property(0.2),
		"importance":        graph.Float64Property(0),
		"created_at":        graph.TimestampProperty(time.Now().UTC().Add(-48 * time.Hour)),
		"access_count":      graph.Int64Property(1), // accessed once
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.GCCollected != 0 {
		t.Fatalf("expected 0 GC deletions (record was accessed), got %d", result.GCCollected)
	}
}

func TestGarbageCollectionDisabled(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = false // disabled

	result := RunDeterministic(eng, cfg, nil)

	if result.GCCollected != 0 {
		t.Fatalf("GC should not run when disabled, got %d", result.GCCollected)
	}
}

func TestCrossSectionLinking(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Curation.SectionLinkMin = 0.5 // low threshold for test

	// Same embedding vector for all sections -- simulates high cosine similarity.
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	eng.Lock()

	// Create two parent articles.
	parentA := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Article about consciousness"),
		"processing_status": graph.StringProperty("processed"),
	})
	for k, v := range parentA.Properties {
		eng.PropIdx().Add(parentA.ID, k, v)
	}

	parentB := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Article about functionalism"),
		"processing_status": graph.StringProperty("processed"),
	})
	for k, v := range parentB.Properties {
		eng.PropIdx().Add(parentB.ID, k, v)
	}

	// Section from article A about memory.
	secA := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("memory plays a role in consciousness"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.Graph().AddEdge(secA.ID, parentA.ID, "section_of", 1.0, nil)
	for k, v := range secA.Properties {
		eng.PropIdx().Add(secA.ID, k, v)
	}
	eng.VecIdx().Add(secA.ID, vec)

	// Section from article B about memory (similar topic, different parent).
	secB := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("memory trace decay in cognitive systems"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.Graph().AddEdge(secB.ID, parentB.ID, "section_of", 1.0, nil)
	for k, v := range secB.Properties {
		eng.PropIdx().Add(secB.ID, k, v)
	}
	eng.VecIdx().Add(secB.ID, vec)

	// Sibling section from article A (should NOT be linked to secA).
	secA2 := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("qualia and phenomenal experience"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.Graph().AddEdge(secA2.ID, parentA.ID, "section_of", 1.0, nil)
	for k, v := range secA2.Properties {
		eng.PropIdx().Add(secA2.ID, k, v)
	}
	eng.VecIdx().Add(secA2.ID, vec)

	eng.Save("test")
	eng.Unlock()

	// Run curation.
	result := RunDeterministic(eng, cfg, nil)

	if result.SectionsLinked == 0 {
		t.Fatal("expected cross-section links to be created")
	}

	// Verify that secA and secB are linked (different parents).
	eng.RLock()
	defer eng.RUnlock()

	linked := false
	for _, e := range eng.Graph().EdgesFrom(secA.ID) {
		if e.Type == "related_to" && e.TargetID == secB.ID {
			linked = true
			break
		}
	}
	// Check reverse direction too.
	if !linked {
		for _, e := range eng.Graph().EdgesFrom(secB.ID) {
			if e.Type == "related_to" && e.TargetID == secA.ID {
				linked = true
				break
			}
		}
	}
	if !linked {
		t.Fatal("secA and secB should be linked (different parents, similar content)")
	}

	// Verify that secA and secA2 are NOT linked (same parent).
	for _, e := range eng.Graph().EdgesFrom(secA.ID) {
		if e.Type == "related_to" && e.TargetID == secA2.ID {
			t.Fatal("secA and secA2 should NOT be linked (same parent)")
		}
	}
	for _, e := range eng.Graph().EdgesFrom(secA2.ID) {
		if e.Type == "related_to" && e.TargetID == secA.ID {
			t.Fatal("secA2 and secA should NOT be linked (same parent)")
		}
	}
}

func TestCrossSectionLinkingIdempotent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Curation.SectionLinkMin = 0.5

	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

	eng.Lock()
	parentA := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Article A"),
		"processing_status": graph.StringProperty("processed"),
	})
	parentB := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Article B"),
		"processing_status": graph.StringProperty("processed"),
	})

	secA := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("shared topic section"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.Graph().AddEdge(secA.ID, parentA.ID, "section_of", 1.0, nil)
	eng.PropIdx().Add(secA.ID, "content_full", secA.Properties["content_full"])
	eng.VecIdx().Add(secA.ID, vec)

	secB := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("shared topic section"),
		"embedding_full": graph.VectorProperty(vec),
	})
	eng.Graph().AddEdge(secB.ID, parentB.ID, "section_of", 1.0, nil)
	eng.PropIdx().Add(secB.ID, "content_full", secB.Properties["content_full"])
	eng.VecIdx().Add(secB.ID, vec)

	eng.Save("test")
	eng.Unlock()

	// First run creates links.
	r1 := RunDeterministic(eng, cfg, nil)
	if r1.SectionsLinked == 0 {
		t.Fatal("first run should create links")
	}

	// Second run should not create duplicate links.
	r2 := RunDeterministic(eng, cfg, nil)
	if r2.SectionsLinked != 0 {
		t.Fatalf("second run should not create duplicate links, got %d", r2.SectionsLinked)
	}
}

func TestParseClassification(t *testing.T) {
	input := `{
		"temporality": "durable",
		"confidence": 0.85,
		"knowledge_type": "semantic",
		"epistemic_status": "probable",
		"keywords": ["auth", "security"],
		"summary_short": "Authentication security practices"
	}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Temporality != "durable" {
		t.Fatalf("expected durable, got %q", result.Temporality)
	}
	if result.Confidence != 0.85 {
		t.Fatalf("expected 0.85, got %f", result.Confidence)
	}
	if len(result.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(result.Keywords))
	}
}

func TestParseClassificationWithCodeFences(t *testing.T) {
	input := "Here's the classification:\n```json\n" +
		`{"temporality":"temporal","confidence":0.7,"knowledge_type":"episodic","keywords":["test"],"summary_short":"Test"}` +
		"\n```\n"

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Temporality != "temporal" {
		t.Fatalf("expected temporal, got %q", result.Temporality)
	}
}

func TestParseClassificationInvalidEnum(t *testing.T) {
	input := `{"temporality":"invalid_value","confidence":0.5,"knowledge_type":"wrong"}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	// Invalid enums should be empty-stringed.
	if result.Temporality != "" {
		t.Fatalf("invalid temporality should be empty, got %q", result.Temporality)
	}
	if result.KnowledgeType != "" {
		t.Fatalf("invalid knowledge_type should be empty, got %q", result.KnowledgeType)
	}
}

func TestParseClassificationConfidenceClamp(t *testing.T) {
	input := `{"temporality":"durable","confidence":5.0}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if result.Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should be clamped to 0.5, got %f", result.Confidence)
	}
}

func TestParseClassificationKeywordLimits(t *testing.T) {
	// Test that keywords are capped at 100.
	parts := make([]string, 150)
	for i := range parts {
		parts[i] = `"keyword"`
	}
	jsonStr := `{"keywords":[` + strings.Join(parts, ",") + `]}`

	result, err := parseClassification(jsonStr)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if len(result.Keywords) > 100 {
		t.Fatalf("keywords should be capped at 100, got %d", len(result.Keywords))
	}
}

func TestParseClassificationSummaryTruncation(t *testing.T) {
	longSummary := ""
	for i := 0; i < 300; i++ {
		longSummary += "x"
	}
	input := `{"summary_short":"` + longSummary + `"}`

	result, err := parseClassification(input)
	if err != nil {
		t.Fatalf("parseClassification: %v", err)
	}
	if len([]rune(result.SummaryShort)) > 200 {
		t.Fatalf("summary should be truncated to 200 runes, got %d", len([]rune(result.SummaryShort)))
	}
}

func TestQualityAuditConceptSummary(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	// Create a concept node with label-as-summary (the bug).
	eng.Lock()
	synthesis := "Kafka is a distributed event streaming platform used across microservices. It provides pub-sub messaging with durable log semantics."
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(synthesis),
		"content_short":     graph.StringProperty("kafka"), // label, not summary
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("kafka"),
		"knowledge_type":    graph.StringProperty("conceptual"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	eng.IndexNode(n.ID, synthesis, nil)
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.QualityRepairs != 1 {
		t.Fatalf("expected 1 quality repair, got %d", result.QualityRepairs)
	}

	// Verify content_short was updated.
	eng.RLock()
	defer eng.RUnlock()
	updated, _ := eng.Graph().GetNode(n.ID)
	cs, _ := updated.Properties.GetString("content_short")
	if cs == "kafka" {
		t.Fatal("content_short should have been repaired from 'kafka' to a summary")
	}
	if len(cs) < 20 {
		t.Fatalf("repaired content_short too short: %q", cs)
	}
}

func TestQualityAuditShortSummary(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	// Create a record with a suspiciously short content_short.
	eng.Lock()
	fullContent := "This is a comprehensive document about Go error handling patterns including wrapping, sentinel errors, and custom error types with stack traces."
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(fullContent),
		"content_short":     graph.StringProperty("Go"), // too short
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	eng.IndexNode(n.ID, fullContent, nil)
	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.QualityRepairs != 1 {
		t.Fatalf("expected 1 quality repair, got %d", result.QualityRepairs)
	}

	eng.RLock()
	defer eng.RUnlock()
	updated, _ := eng.Graph().GetNode(n.ID)
	cs, _ := updated.Properties.GetString("content_short")
	if cs == "Go" {
		t.Fatal("content_short should have been repaired")
	}
	if len(cs) < 20 {
		t.Fatalf("repaired content_short too short: %q", cs)
	}
}

// TestRunDeterministicMergedLoopBranchesByNodeType pins the
// behavioral invariants the P2-07 iterator merge had to preserve:
//
//   - regular records contribute to manifest.TotalRecords +
//     non-concept lifecycle / orphan checks + quality Rules 2/3
//   - concept nodes do NOT contribute to TotalRecords but DO populate
//     existingConcepts (suppressing duplicate concept creation)
//     AND fall through to Rules 2/3 when Rule 1 doesn't fire
//   - deleted records are skipped from every phase
//
// The pre-merge code split these three concerns across separate
// iterators (`it`, `it2`, `cnIt`), each with its own filter. The
// merged loop has to keep all three filters correct in one pass.
// Two regression-grade specific assertions (beyond the original
// stability check):
//
//   1. A SECOND RunDeterministic over the same store proposes ZERO
//      new concepts for the existing kafka keyword, proving
//      existingConcepts was populated correctly.
//   2. A FRESH concept node with template-style content_short and
//      missing embedding_short triggers Rule 3 (flag_embed),
//      bumping QualityFlags. The pre-fix merged loop's
//      unconditional `continue` after the concept branch dropped
//      this — concept embeddings drifted silently.
func TestRunDeterministicMergedLoopBranchesByNodeType(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	eng.Lock()

	// Regular processed record — should count in manifest.
	regular := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("regular processed record content for the manifest"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range regular.Properties {
		eng.PropIdx().Add(regular.ID, k, v)
	}

	// Existing concept node with kafka keyword — must populate
	// existingConcepts so a duplicate concept isn't proposed.
	// Note: content_short = "kafka stream patterns" (≠ keyword), so
	// Rule 1 does NOT fire on this node — leaving the path open for
	// Rules 2/3 to evaluate (Rule 2 is gated on len < 20 / len > 100,
	// Rule 3 on hasEmbedShort). This concept has neither short<20 nor
	// missing embedding_short (we set neither), so it produces no
	// quality issue. The freshConcept below is the one that exercises
	// the Rule 3 fall-through.
	conceptKW := "kafka"
	concept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Concept synthesis: kafka stream processing patterns and operational lessons."),
		"content_short":     graph.StringProperty("kafka stream patterns and operational lessons synthesis"),
		"embedding_short":   graph.VectorProperty([]float32{0.1, 0.2, 0.3}),
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty(conceptKW),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range concept.Properties {
		eng.PropIdx().Add(concept.ID, k, v)
	}

	// Fresh concept node — content_short is template-style ("redis"
	// itself, NOT "redis (5 records)" — that would match concept_keyword
	// and fire Rule 1). With contentShort != keyword + no
	// embedding_short, this pin ONLY catches when concept-Rule-3 fall-
	// through is preserved.
	freshConcept := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Concept page for redis covering caching patterns and replication strategies and lots of detail to push past the 100 char floor of Rule 2."),
		"content_short":     graph.StringProperty("Cache + replication"), // 19 chars; ≠ keyword; would also trip Rule 2
		// No embedding_short — Rule 3 candidate.
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("concept"),
		"concept_keyword":   graph.StringProperty("redis"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range freshConcept.Properties {
		eng.PropIdx().Add(freshConcept.ID, k, v)
	}

	// Deleted record — must be skipped entirely.
	del := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("deleted record body"),
		"processing_status": graph.StringProperty("deleted"),
		"created_at":        graph.TimestampProperty(now),
	})
	for k, v := range del.Properties {
		eng.PropIdx().Add(del.ID, k, v)
	}

	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)
	m := result.Manifest
	if m == nil {
		t.Fatal("manifest should not be nil")
	}
	// Manifest counts regular records only — not concept, not deleted.
	if m.TotalRecords != 1 {
		t.Errorf("manifest.TotalRecords = %d, want 1 (regular only; concept + freshConcept + deleted excluded)", m.TotalRecords)
	}

	// Rule 2 fires on the fresh concept (short=19, full>100). Rule 3
	// would also fire if Rule 2 didn't (no embedding_short). Either
	// way, the merged loop must produce a quality issue for the
	// fresh concept; pre-fix it produced ZERO because the concept
	// branch unconditionally continue'd.
	if result.QualityRepairs+result.QualityFlags == 0 {
		t.Errorf("expected at least one quality issue from the fresh concept node (Rules 2/3 should fall through for concept nodes when Rule 1 doesn't fire); got %d repairs + %d flags",
			result.QualityRepairs, result.QualityFlags)
	}

	// existingConcepts must have been populated. Pin specifically
	// that no duplicate "kafka" concept candidate is proposed on a
	// second run — the populated existingConcepts should suppress
	// it.
	r2 := RunDeterministic(eng, cfg, nil)
	for _, c := range r2.ConceptCandidates {
		if c.Keyword == conceptKW {
			t.Errorf("existingConcepts didn't suppress duplicate concept proposal for %q; got candidate %+v",
				conceptKW, c)
		}
	}
}

func TestQualityAuditNoFalsePositive(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	// Create a record with a proper summary -- should NOT be flagged.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Detailed content about authentication patterns."),
		"content_short":     graph.StringProperty("Overview of authentication patterns including OAuth2, JWT, and session-based auth."),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	eng.IndexNode(n.ID, "auth patterns", nil)
	eng.Save("test")
	eng.Unlock()

	_ = n // used above

	result := RunDeterministic(eng, cfg, nil)

	if result.QualityRepairs != 0 {
		t.Fatalf("expected 0 quality repairs (no issues), got %d", result.QualityRepairs)
	}
}
