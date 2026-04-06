package curation

import (
	"context"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/graph"
)

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
