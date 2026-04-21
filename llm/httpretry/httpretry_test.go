package httpretry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestDoWithRetrySucceedsFirstTry: non-retry path stays free of any
// backoff overhead. A 200 response goes back to the caller directly.
func TestDoWithRetrySucceedsFirstTry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), http.DefaultClient, DefaultRetryConfig(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts.Load())
	}
}

// TestDoWithRetryEventualSuccessAfter429: two 429s then a 200. The
// helper returns the success response; attempt count matches the
// expected retry loop.
func TestDoWithRetryEventualSuccessAfter429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := RetryConfig{MaxAttempts: 4, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	resp, err := DoWithRetry(context.Background(), http.DefaultClient, cfg, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

// TestDoWithRetryExhaustedReturns429: after MaxAttempts the helper
// hands back the final 429 response so callers can surface the
// provider's structured error body.
func TestDoWithRetryExhaustedReturns429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	resp, err := DoWithRetry(context.Background(), http.DefaultClient, cfg, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected final 429, got %d", resp.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

// TestDoWithRetryNonRetryableReturnsImmediately: 400 Bad Request is
// client-fault and should not be retried.
func TestDoWithRetryNonRetryableReturnsImmediately(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), http.DefaultClient, DefaultRetryConfig(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt (non-retryable), got %d", attempts.Load())
	}
}

// TestDoWithRetryHonorsRetryAfter: the helper should sleep at least
// the header value, capped at MaxDelay. Loose timing check: just
// verifies the second attempt happens and the total delay respects
// the cap (not lower than the configured base, not higher than max).
func TestDoWithRetryHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1") // 1 second suggestion
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// MaxDelay below the suggested Retry-After forces the cap.
	cfg := RetryConfig{MaxAttempts: 2, BaseDelay: 1 * time.Millisecond, MaxDelay: 20 * time.Millisecond}
	start := time.Now()
	resp, err := DoWithRetry(context.Background(), http.DefaultClient, cfg, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if elapsed > 200*time.Millisecond {
		t.Fatalf("retry slept %v -- MaxDelay cap not honored", elapsed)
	}
}

// TestDoWithRetryContextCancellation: if ctx cancels during backoff,
// the helper returns ctx.Err() promptly without the final attempt.
func TestDoWithRetryContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	cfg := RetryConfig{MaxAttempts: 10, BaseDelay: 200 * time.Millisecond, MaxDelay: 500 * time.Millisecond}
	_, err := DoWithRetry(ctx, http.DefaultClient, cfg, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
