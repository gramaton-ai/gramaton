package curation

import (
	"context"
	"testing"
	"time"

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
