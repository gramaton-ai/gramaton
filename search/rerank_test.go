package search

import (
	"context"
	"strings"
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

// promptRecordingReranker captures the full prompt passed to
// CompleteWithModel, so tests can pin what the LLM actually sees.
type promptRecordingReranker struct {
	mu         sync.Mutex
	gotPrompts []string
	response   string
}

func (m *promptRecordingReranker) CompleteWithModel(_ context.Context, _, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotPrompts = append(m.gotPrompts, prompt)
	return m.response, nil
}

// TestRerankPromptCarriesEpistemicMetadata pins that every candidate
// line carries the record's lifecycle metadata. The reranked order
// replaces the composite-score order (effective_score is never
// re-applied), so the prompt is the only channel through which
// resolution/temporality/confidence can influence the final ordering;
// a bare-snippet prompt silently promotes superseded records.
func TestRerankPromptCarriesEpistemicMetadata(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	created := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	superseded := g.AddNode(graph.Properties{
		"content_short":    graph.StringProperty("old admission control plan"),
		"resolution":       graph.StringProperty("superseded"),
		"epistemic_status": graph.StringProperty("well_established"),
		"temporality":      graph.StringProperty("durable"),
		"confidence":       graph.Float64Property(0.9),
		"created_at":       graph.TimestampProperty(created),
	})
	current := g.AddNode(graph.Properties{
		"content_short": graph.StringProperty("current admission control decision"),
		"created_at":    graph.TimestampProperty(created),
	})
	for _, n := range []string{superseded.ID, current.ID} {
		node, _ := g.GetNode(n)
		for k, v := range node.Properties {
			propIdx.Add(n, k, v)
		}
	}

	rec := &promptRecordingReranker{response: "[1, 2]"}
	tool := New(g, propIdx, vecIdx, nil, nil, config.Defaults(), WithReranker(rec))

	tool.rerankWithLLM("admission control", []scored{
		{id: superseded.ID, score: 0.9},
		{id: current.ID, score: 0.8},
	})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.gotPrompts) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(rec.gotPrompts))
	}
	prompt := rec.gotPrompts[0]
	wantSuperseded := "(superseded; well_established; durable; conf 0.9; 2026-04-21) old admission control plan"
	if !strings.Contains(prompt, wantSuperseded) {
		t.Errorf("prompt missing metadata-prefixed superseded candidate %q\nprompt:\n%s", wantSuperseded, prompt)
	}
	wantCurrent := "(current; 2026-04-21) current admission control decision"
	if !strings.Contains(prompt, wantCurrent) {
		t.Errorf("prompt missing metadata-prefixed current candidate %q\nprompt:\n%s", wantCurrent, prompt)
	}
}

// TestRerankPromptMarksExpiredRecordsHistorical pins the valid_until
// fallback: a record past its validity window with no explicit
// resolution must be presented as historical, not current.
func TestRerankPromptMarksExpiredRecordsHistorical(t *testing.T) {
	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	n := g.AddNode(graph.Properties{
		"content_short": graph.StringProperty("expired ephemeral note"),
		"valid_until":   graph.TimestampProperty(time.Now().UTC().Add(-time.Hour)),
	})
	for k, v := range n.Properties {
		propIdx.Add(n.ID, k, v)
	}

	rec := &promptRecordingReranker{response: "[1]"}
	tool := New(g, propIdx, vecIdx, nil, nil, config.Defaults(), WithReranker(rec))
	tool.rerankWithLLM("ephemeral note", []scored{{id: n.ID, score: 1.0}})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.gotPrompts) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(rec.gotPrompts))
	}
	if !strings.Contains(rec.gotPrompts[0], "(historical) expired ephemeral note") {
		t.Errorf("expired record not marked historical\nprompt:\n%s", rec.gotPrompts[0])
	}
}

// TestRerankPromptCarriesMetadataGuidance pins the usage-guidance
// block. Metadata without instructions is noise to a small rerank
// model; the guidance is what turns it into query-conditional
// epistemic judgment (prefer current on ties, but let correcting
// records win status/history queries).
func TestRerankPromptCarriesMetadataGuidance(t *testing.T) {
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

	rec := &promptRecordingReranker{response: "[1]"}
	tool := New(g, propIdx, vecIdx, nil, nil, config.Defaults(), WithReranker(rec))
	tool.rerankWithLLM("test query", []scored{{id: n.ID, score: 1.0}})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.gotPrompts) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(rec.gotPrompts))
	}
	prompt := rec.gotPrompts[0]
	for _, marker := range []string{
		"use the metadata as a tiebreaker",
		"Prefer current records over superseded",
		"a superseding or correcting record is often the best answer",
		"Never rank refuted records as if their claim were true",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("prompt missing guidance line %q", marker)
		}
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
