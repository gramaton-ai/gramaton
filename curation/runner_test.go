package curation

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

func testLogger() *slog.Logger {
	return slog.Default()
}

func TestRunnerStatus(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	runner := NewRunner(eng, nil, cfg, nil)

	status := runner.Status()
	if status.Autonomous {
		t.Fatal("autonomous should be false without LLM")
	}
	if status.LastCurated != nil {
		t.Fatal("last_curated should be nil before first run")
	}
}

func TestRunnerTrigger(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	addNode(t, eng, "Test record", "durable", 0.9, []string{"test"}, now)

	runner := NewRunner(eng, nil, cfg, nil)
	ok := runner.Trigger(context.Background())
	if !ok {
		t.Fatal("Trigger should return true")
	}

	status := runner.Status()
	if status.LastCurated == nil {
		t.Fatal("last_curated should be set after trigger")
	}
}

func TestRunnerTriggerPreventsConcurrent(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	runner := NewRunner(eng, nil, cfg, nil)

	// Simulate in-progress by setting the flag.
	runner.state.mu.Lock()
	runner.state.inProgress = true
	runner.state.mu.Unlock()

	ok := runner.Trigger(context.Background())
	if ok {
		t.Fatal("Trigger should return false when already in progress")
	}
}

func TestRunnerConceptCandidates(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 2

	now := time.Now().UTC()
	addNode(t, eng, "A about auth", "durable", 0.9, []string{"auth"}, now)
	addNode(t, eng, "B about auth", "durable", 0.8, []string{"auth"}, now)

	runner := NewRunner(eng, nil, cfg, nil)
	runner.Trigger(context.Background())

	candidates := runner.ConceptCandidates()
	found := false
	for _, c := range candidates {
		if c.Keyword == "auth" {
			found = true
			if c.Count != 2 {
				t.Fatalf("expected count 2, got %d", c.Count)
			}
		}
	}
	if !found {
		t.Fatal("auth should be a concept candidate")
	}
}

func TestRunnerManifest(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	addNode(t, eng, "Record", "durable", 0.9, nil, now)

	runner := NewRunner(eng, nil, cfg, nil)
	runner.Trigger(context.Background())

	m := runner.Manifest()
	if m == nil {
		t.Fatal("manifest should not be nil after trigger")
	}
	if m.TotalRecords != 1 {
		t.Fatalf("expected 1 record, got %d", m.TotalRecords)
	}
}

func TestRunnerPostCycleHook(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	hookCalled := false
	runner := NewRunner(eng, nil, cfg, nil)
	runner.SetPostCycleHook(func() {
		hookCalled = true
	})

	runner.Trigger(context.Background())

	if !hookCalled {
		t.Fatal("post-cycle hook should have been called")
	}
}

func TestRunnerTriggerDryRun(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()
	// Create a pending node directly (addNode sets processing_status=processed).
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Pending record for dry-run"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	eng.Unlock()

	llm := &mockLLM{
		responses: []string{`{"temporality":"durable","confidence":0.8,"summary_short":"Test"}`},
	}

	runner := NewRunner(eng, llm, cfg, nil)
	result := runner.TriggerDryRun(context.Background())

	if result == nil {
		t.Fatal("TriggerDryRun should return a result")
	}
	if !result.DryRun {
		t.Fatal("result should have DryRun=true")
	}

	// Verify no mutation: record should still be "captured".
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps != "captured" {
			t.Fatalf("dry-run should not change processing_status, got %q", ps)
		}
	}
}

func TestRunnerTriggerDryRunNoLLM(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	runner := NewRunner(eng, nil, cfg, nil)
	result := runner.TriggerDryRun(context.Background())

	if result == nil {
		t.Fatal("should return result even without LLM")
	}
	if !result.DryRun {
		t.Fatal("should be dry-run")
	}
	if len(result.PlannedChanges) != 0 {
		t.Fatal("should have no planned changes without LLM")
	}
}

func TestDeterministicOrphanLinking(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	now := time.Now().UTC()

	// Create two records with similar embeddings but no edges.
	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Orphan one about kafka"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n1.Properties {
		eng.PropIdx().Add(n1.ID, k, v)
	}
	eng.VecIdx().Add(n1.ID, []float32{0.9, 0.1, 0.0})
	eng.Graph().SetNodeProperty(n1.ID, "embedding_full",
		graph.VectorProperty([]float32{0.9, 0.1, 0.0}))

	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Orphan two about kafka too"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n2.Properties {
		eng.PropIdx().Add(n2.ID, k, v)
	}
	eng.VecIdx().Add(n2.ID, []float32{0.85, 0.15, 0.0})
	eng.Graph().SetNodeProperty(n2.ID, "embedding_full",
		graph.VectorProperty([]float32{0.85, 0.15, 0.0}))

	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.OrphansLinked < 1 {
		t.Fatalf("expected at least 1 orphan linked, got %d", result.OrphansLinked)
	}

	// Verify edge was created.
	eng.RLock()
	defer eng.RUnlock()
	e1 := eng.Graph().EdgesFrom(n1.ID)
	e2 := eng.Graph().EdgesFrom(n2.ID)
	totalEdges := len(e1) + len(e2)
	if totalEdges < 1 {
		t.Fatal("expected at least 1 edge between orphans")
	}
}

// TestOrphanLinkerSkipsObservations pins the fix for
// 01KQ62SRYP2ZKYR40JKSHJAC69: observation nodes have an
// observation_of edge to a parent, but observation_of is filtered
// from SemanticEdgeCount as structural -- so they look orphaned.
// Pre-fix, the orphan-linking pass added a `related_to` edge from
// each observation to a similar real record every cycle (and also
// chose observations as link TARGETS), polluting the graph.
// Post-fix, observations are excluded from BOTH the orphan
// candidate set and the link-target candidate set.
func TestOrphanLinkerSkipsObservations(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Curation.OrphanSimilarityMin = 0.5

	now := time.Now().UTC()

	eng.Lock()
	// Two real records linked via related_to so neither is an orphan
	// candidate. Their embeddings sit close enough that an observation
	// pointed at the same vector space would similarity-match either.
	a := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Real record A about kafka"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range a.Properties {
		eng.PropIdx().Add(a.ID, k, v)
	}
	eng.VecIdx().Add(a.ID, []float32{0.85, 0.15, 0.0})
	eng.Graph().SetNodeProperty(a.ID, "embedding_full",
		graph.VectorProperty([]float32{0.85, 0.15, 0.0}))

	b := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Real record B about kafka"),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range b.Properties {
		eng.PropIdx().Add(b.ID, k, v)
	}
	eng.VecIdx().Add(b.ID, []float32{0.83, 0.17, 0.0})
	eng.Graph().SetNodeProperty(b.ID, "embedding_full",
		graph.VectorProperty([]float32{0.83, 0.17, 0.0}))
	// related_to edge so neither A nor B is an orphan candidate.
	eng.Graph().AddEdge(a.ID, b.ID, "related_to", 0.9, nil)

	// Observation attached to A via observation_of. Pre-fix this
	// shows up as a 0-semantic-edge node and would be treated as an
	// orphan; post-fix it is skipped.
	obs := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Observation about kafka"),
		"processing_status": graph.StringProperty("processed"),
		"node_type":         graph.StringProperty("observation"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range obs.Properties {
		eng.PropIdx().Add(obs.ID, k, v)
	}
	eng.VecIdx().Add(obs.ID, []float32{0.9, 0.1, 0.0})
	eng.Graph().SetNodeProperty(obs.ID, "embedding_full",
		graph.VectorProperty([]float32{0.9, 0.1, 0.0}))
	eng.Graph().AddEdge(obs.ID, a.ID, "observation_of", 1.0, nil)

	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	if result.OrphansLinked != 0 {
		t.Errorf("OrphansLinked = %d, want 0 (observations must not be orphan candidates)", result.OrphansLinked)
	}
	if got := result.Manifest.OrphanCount; got != 0 {
		t.Errorf("Manifest.OrphanCount = %d, want 0", got)
	}

	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesFrom(obs.ID) {
		if e.Type == "related_to" {
			t.Errorf("observation got an outbound related_to edge added (the bug): %+v", e)
		}
	}
	for _, e := range eng.Graph().EdgesTo(obs.ID) {
		if e.Type == "related_to" {
			t.Errorf("observation got an inbound related_to edge added (link-target bug): %+v", e)
		}
	}
}

func TestOrphanLinkerSkipsLowQuality(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Curation.OrphanSimilarityMin = 0.5

	now := time.Now().UTC()

	eng.Lock()
	// Low-confidence orphan -- should NOT be linked.
	lowConf := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Low confidence orphan about kafka"),
		"processing_status": graph.StringProperty("processed"),
		"confidence":        graph.Float64Property(0.1),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range lowConf.Properties {
		eng.PropIdx().Add(lowConf.ID, k, v)
	}
	eng.VecIdx().Add(lowConf.ID, []float32{0.9, 0.1, 0.0})
	eng.Graph().SetNodeProperty(lowConf.ID, "embedding_full",
		graph.VectorProperty([]float32{0.9, 0.1, 0.0}))

	// Unclassified orphan -- should NOT be linked.
	captured := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Unclassified orphan about kafka"),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty("durable"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range captured.Properties {
		eng.PropIdx().Add(captured.ID, k, v)
	}
	eng.VecIdx().Add(captured.ID, []float32{0.85, 0.15, 0.0})
	eng.Graph().SetNodeProperty(captured.ID, "embedding_full",
		graph.VectorProperty([]float32{0.85, 0.15, 0.0}))

	eng.Save("test")
	eng.Unlock()

	result := RunDeterministic(eng, cfg, nil)

	// Neither low-confidence nor captured orphans should be linked.
	if result.OrphansLinked != 0 {
		t.Fatalf("expected 0 orphans linked (quality filter), got %d", result.OrphansLinked)
	}
}

func TestCircuitBreakerTripsOnConsecutiveErrors(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 5
	cfg.LLMCuration.MaxCallsPerRun = 10

	// Add pending records.
	for i := 0; i < 5; i++ {
		addPendingNode(t, eng, "test content for circuit breaker")
	}

	// LLM that always errors.
	llm := &mockLLM{
		errors: []error{
			fmt.Errorf("billing error"), fmt.Errorf("billing error"),
			fmt.Errorf("billing error"), fmt.Errorf("billing error"),
			fmt.Errorf("billing error"),
		},
	}

	runner := NewRunner(eng, llm, cfg, testLogger())

	// Run 3 cycles -- should trip the circuit breaker.
	for i := 0; i < 3; i++ {
		runner.state.mu.Lock()
		runner.state.inProgress = false
		runner.state.mu.Unlock()
		runner.cycle(context.Background())
	}

	status := runner.Status()
	if !status.LLMPaused {
		t.Fatal("circuit breaker should be tripped after 3 error cycles")
	}
	if status.LLMPauseReason == "" {
		t.Fatal("pause reason should be set")
	}

	// 4th cycle should skip LLM work.
	llm.calls = 0
	runner.state.mu.Lock()
	runner.state.inProgress = false
	runner.state.mu.Unlock()
	runner.cycle(context.Background())
	if llm.calls > 0 {
		t.Fatalf("LLM should not be called when paused, got %d calls", llm.calls)
	}
}

func TestCircuitBreakerResetOnManualTrigger(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	runner := NewRunner(eng, &mockLLM{}, cfg, nil)

	// Manually set circuit breaker.
	runner.state.mu.Lock()
	runner.state.LLMPaused = true
	runner.state.LLMPauseReason = "test"
	runner.state.ConsecutiveErrorCycles = 5
	runner.state.mu.Unlock()

	// Trigger should reset it.
	runner.Trigger(context.Background())

	status := runner.Status()
	if status.LLMPaused {
		t.Fatal("manual trigger should reset circuit breaker")
	}
}

func TestCircuitBreakerResetsOnSuccessfulCycle(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.LLMCuration.BatchSize = 1
	cfg.LLMCuration.MaxCallsPerRun = 5

	addPendingNode(t, eng, "test content")

	classifyResp := `{"temporality":"durable","confidence":0.8,"knowledge_type":"semantic","epistemic_status":"probable","keywords":["test"],"summary_short":"test"}`
	llm := &mockLLM{responses: []string{classifyResp}}

	runner := NewRunner(eng, llm, cfg, nil)

	// Set a non-zero error count.
	runner.state.mu.Lock()
	runner.state.ConsecutiveErrorCycles = 2
	runner.state.mu.Unlock()

	runner.Trigger(context.Background())

	runner.state.mu.Lock()
	count := runner.state.ConsecutiveErrorCycles
	runner.state.mu.Unlock()

	if count != 0 {
		t.Fatalf("successful cycle should reset error count, got %d", count)
	}
}

func TestStartAndStop(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.Curation.Interval = 100 * time.Millisecond // fast for testing

	runner := NewRunner(eng, nil, cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Start(ctx)
		close(done)
	}()

	// Let it run a couple cycles.
	time.Sleep(350 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good, stopped cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop within timeout")
	}

	// Verify it ran.
	status := runner.Status()
	if status.LastCurated == nil {
		t.Fatal("runner should have run at least once")
	}
}
