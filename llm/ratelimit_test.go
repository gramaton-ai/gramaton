package llm

import (
	"context"
	"testing"
	"time"
)

type dummyProvider struct {
	calls int
}

func (d *dummyProvider) Complete(_ context.Context, _ string) (string, error) {
	d.calls++
	return "ok", nil
}

func (d *dummyProvider) CompleteWithModel(_ context.Context, _, _ string) (string, error) {
	d.calls++
	return "ok", nil
}

func (d *dummyProvider) ModelID() string      { return "dummy" }
func (d *dummyProvider) ProviderName() string { return "dummy" }

func TestRateLimitedEnforcesInterval(t *testing.T) {
	inner := &dummyProvider{}
	rl := NewRateLimited(inner, 50*time.Millisecond).(*RateLimited)

	start := time.Now()

	// Three calls with 50ms interval = at least 100ms total.
	for i := 0; i < 3; i++ {
		if _, err := rl.Complete(context.Background(), "test"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	elapsed := time.Since(start)
	if elapsed < 90*time.Millisecond {
		t.Fatalf("expected >= 100ms for 3 calls at 50ms interval, got %v", elapsed)
	}
	if inner.calls != 3 {
		t.Fatalf("expected 3 inner calls, got %d", inner.calls)
	}
}

func TestRateLimitedRespectsContext(t *testing.T) {
	inner := &dummyProvider{}
	rl := NewRateLimited(inner, 5*time.Second) // long interval

	// First call succeeds immediately.
	if _, err := rl.Complete(context.Background(), "test"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call with cancelled context should fail fast.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rl.Complete(ctx, "test")
	if err == nil {
		t.Fatal("expected context error for cancelled ctx")
	}
}

func TestRateLimitedZeroIntervalPassthrough(t *testing.T) {
	inner := &dummyProvider{}
	p := NewRateLimited(inner, 0)

	// Should return the inner provider directly, not wrapped.
	if _, ok := p.(*RateLimited); ok {
		t.Fatal("zero interval should return unwrapped provider")
	}
	if _, err := p.Complete(context.Background(), "test"); err != nil {
		t.Fatalf("call: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 call, got %d", inner.calls)
	}
}
