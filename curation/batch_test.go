package curation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
)

// stubLLM is a minimal llm.Provider for wrapper-chain tests.
// curation doesn't have an llm test double today and the existing
// tests in this package don't exercise the provider interface, so
// this stub lives here alongside its consumer.
type stubLLM struct{}

func (stubLLM) Complete(ctx context.Context, prompt string) (string, error)        { return "", nil }
func (stubLLM) CompleteWithModel(ctx context.Context, m, p string) (string, error) { return "", nil }
func (stubLLM) ModelID() string                                                    { return "stub-model" }
func (stubLLM) ProviderName() string                                               { return "stub" }
func (stubLLM) SupportsStructuredOutput() bool                                     { return false }
func (stubLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

func TestFindMeteredDirect(t *testing.T) {
	tracker := llm.NewUsageTracker(t.TempDir(), 0, 0, 0)
	m := llm.NewMetered(stubLLM{}, tracker, nil)

	got := findMetered(m)
	if got == nil {
		t.Fatal("findMetered(*Metered) = nil, want the wrapper itself")
	}
	if got != m {
		t.Errorf("findMetered returned a different *Metered than the input")
	}
}

func TestFindMeteredThroughRateLimited(t *testing.T) {
	// Chain: RateLimited -> Metered -> stub. The batch-classification
	// accounting path must reach through RateLimited to find the
	// Metered wrapper, since CLI providers (claudecli/kirocli) are
	// rate-limited in production. The real path is
	// RateLimited(Metered(inner)), not the other order, but either
	// arrangement should resolve correctly.
	tracker := llm.NewUsageTracker(t.TempDir(), 0, 0, 0)
	m := llm.NewMetered(stubLLM{}, tracker, nil)
	rl := llm.NewRateLimited(m, 100*time.Millisecond)

	got := findMetered(rl)
	if got == nil {
		t.Fatal("findMetered(RateLimited(Metered)) = nil, want the inner Metered")
	}
	if got != m {
		t.Errorf("findMetered did not return the same *Metered wrapped by RateLimited")
	}
}

func TestFindMeteredRawProviderReturnsNil(t *testing.T) {
	got := findMetered(stubLLM{})
	if got != nil {
		t.Errorf("findMetered(raw) = %v, want nil", got)
	}
}

// TestApplyClassificationClearsAttempts pins the success-clear branch
// in applyClassification (called by the Anthropic batch path). Without
// this clear, a record that failed N-1 times in autonomous mode and
// then succeeded via batch mode would keep a stale classify_attempts
// counter and (incorrectly) push to "stuck" on its next autonomous
// failure.
func TestApplyClassificationClearsAttempts(t *testing.T) {
	eng := setupEngine(t)

	// Seed a record with classify_attempts=2 (simulating two prior
	// failures via the autonomous path) and a captured-status state.
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":         graph.StringProperty("content needing classification"),
		"processing_status":    graph.StringProperty("captured"),
		"classify_attempts":    graph.Int64Property(2),
		"last_classify_error":  graph.StringProperty("transient API timeout"),
		"created_at":           graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("seed")
	eng.Unlock()

	// Apply a successful classification (mimicking what the batch path
	// does after parsing an Anthropic batch result).
	classification := &classificationResult{
		Temporality:     "durable",
		Confidence:      0.85,
		KnowledgeType:   "semantic",
		EpistemicStatus: "well_established",
	}
	eng.Lock()
	applyClassification(eng, n.ID, classification, "haiku", "sonnet", 2000)
	eng.Unlock()

	eng.RLock()
	defer eng.RUnlock()
	got, _ := eng.Graph().GetNode(n.ID)
	attempts, _ := got.Properties.GetInt64("classify_attempts")
	status, _ := got.Properties.GetString("processing_status")

	if attempts != 0 {
		t.Errorf("classify_attempts after success: got %d, want 0 (cleared)", attempts)
	}
	if status != "processed" {
		t.Errorf("processing_status: got %q, want %q", status, "processed")
	}
}
