package llm

import (
	"context"
	"sync"
	"time"
)

// RateLimited wraps a Provider with a minimum interval between calls.
// Used for CLI providers that hit subscription rate limits without
// server-side backoff (no HTTP 429). API providers handle their own
// rate limiting and should not use this wrapper.
type RateLimited struct {
	inner    Provider
	interval time.Duration
	mu       sync.Mutex
	lastCall time.Time
}

// NewRateLimited wraps a provider with rate limiting. If interval is
// zero, returns the inner provider unwrapped.
func NewRateLimited(inner Provider, interval time.Duration) Provider {
	if interval <= 0 {
		return inner
	}
	return &RateLimited{inner: inner, interval: interval}
}

func (r *RateLimited) wait(ctx context.Context) error {
	r.mu.Lock()
	elapsed := time.Since(r.lastCall)
	if elapsed < r.interval {
		wait := r.interval - elapsed
		r.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		r.mu.Lock()
	}
	r.lastCall = time.Now()
	r.mu.Unlock()
	return nil // context not cancelled; lastCall updated
}

func (r *RateLimited) Complete(ctx context.Context, prompt string) (string, error) {
	if err := r.wait(ctx); err != nil {
		return "", err
	}
	return r.inner.Complete(ctx, prompt)
}

func (r *RateLimited) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if err := r.wait(ctx); err != nil {
		return "", err
	}
	return r.inner.CompleteWithModel(ctx, model, prompt)
}

func (r *RateLimited) ModelID() string {
	return r.inner.ModelID()
}

// Inner returns the wrapped provider for type assertion.
func (r *RateLimited) Inner() Provider {
	return r.inner
}
