package search

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// modelRecordingReranker is a reranker that captures the model arg
// passed to CompleteWithModel. Used to pin the contract that
// rerank.go resolves the model via cfg.ModelForTask(TaskRerank)
// and threads that string through to the LLM call -- without an
// end-to-end test on this path, a wiring regression (e.g. somebody
// switches back to plain Complete) goes silent.
type modelRecordingReranker struct {
	mu        sync.Mutex
	gotModels []string
	response  string
}

func (m *modelRecordingReranker) CompleteWithModel(_ context.Context, model, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotModels = append(m.gotModels, model)
	return m.response, nil
}

// TestRerankResolvesModelFromTaskTier asserts that rerankWithLLM
// passes the model string returned by cfg.ModelForTask(TaskRerank)
// to the LLM, not a hardcoded default and not the provider's
// construction-time model. Pre-Layer-1, rerank called Complete()
// (no model arg) and the provider's default fired; the Layer-1
// rewrite switched to CompleteWithModel(cfg.ModelForTask(...)).
// Without this test, a future refactor that reverts to Complete()
// passes every other test silently.
func TestRerankResolvesModelFromTaskTier(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	n := g.AddNode(graph.Properties{
		"content_short": graph.StringProperty("test record"),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		propIdx.Add(n.ID, k, v)
	}

	cfg := config.Defaults()
	// Override the rerank tier model so the test can distinguish
	// it from anything else in the config. Using a string the
	// production defaults wouldn't produce.
	cfg.LLM.Models.Low = "test-rerank-model-low"
	cfg.LLM.Models.Tasks["rerank"] = "low"

	rec := &modelRecordingReranker{response: "[1]"}
	tool := New(g, propIdx, vecIdx, nil, nil, cfg, WithReranker(rec))

	candidates := []scored{{id: n.ID, score: 1.0}}
	tool.rerankWithLLM("test query", candidates)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.gotModels) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(rec.gotModels))
	}
	if rec.gotModels[0] != "test-rerank-model-low" {
		t.Errorf("rerank used model %q, want %q (cfg.ModelForTask(TaskRerank))",
			rec.gotModels[0], "test-rerank-model-low")
	}
}

// TestRerankPicksUpTaskTierOverride flips the rerank task to the
// high tier and verifies the model swap propagates. Pins the
// task-tier system for rerank specifically (rather than asserting
// the whole tier system in a generic test).
func TestRerankPicksUpTaskTierOverride(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	n := g.AddNode(graph.Properties{
		"content_short": graph.StringProperty("test record"),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		propIdx.Add(n.ID, k, v)
	}

	cfg := config.Defaults()
	cfg.LLM.Models.High = "test-rerank-model-high"
	cfg.LLM.Models.Tasks["rerank"] = "high"

	rec := &modelRecordingReranker{response: "[1]"}
	tool := New(g, propIdx, vecIdx, nil, nil, cfg, WithReranker(rec))

	tool.rerankWithLLM("test query", []scored{{id: n.ID, score: 1.0}})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.gotModels) != 1 || rec.gotModels[0] != "test-rerank-model-high" {
		t.Errorf("rerank with high-tier override used %v, want [test-rerank-model-high]", rec.gotModels)
	}
}
