// Package httpretry provides shared exponential-backoff retry logic
// for Gramaton's HTTP LLM providers (anthropic, openai). Kept in a
// subpackage so each provider can import it without creating a cycle
// back through llm's provider registry.
package httpretry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// RetryConfig controls the exponential-backoff retry policy applied
// to HTTP LLM providers (anthropic, openai-compatible). The defaults
// target the 429 / 5xx window seen from major providers without
// turning short curation cycles into long waits on the wrong kind
// of error.
type RetryConfig struct {
	// MaxAttempts is the total number of tries (initial + retries).
	// Zero means "do not retry" (single attempt). Negative values
	// are treated as zero.
	MaxAttempts int
	// BaseDelay is the first retry's nominal delay. Subsequent
	// delays double until MaxDelay. Zero defaults to 500ms.
	BaseDelay time.Duration
	// MaxDelay caps the computed backoff. Zero defaults to 30s.
	MaxDelay time.Duration
}

// DefaultRetryConfig is the policy used by providers that don't
// override it: up to 4 attempts, 500ms -> 1s -> 2s -> 4s with jitter,
// 30s cap. Total worst-case delay is ~7.5s across retries, well under
// typical per-call timeouts.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 4,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
	}
}

// DoWithRetry executes buildReq + client.Do inside a retry loop that
// re-tries on 429 Too Many Requests and 5xx responses. buildReq is
// invoked fresh on every attempt so callers can reset the request
// body reader; invoking it once and reusing the request would let
// the body drain on the first attempt.
//
// The returned response is the final attempt's response (either a
// successful one, or the last failure after MaxAttempts). Callers
// still own the response body and must close it.
//
// Respects Retry-After on 429/503 (seconds or HTTP-date). Falls back
// to exponential backoff with decorrelated jitter when the header is
// absent. Returns early on context cancellation.
func DoWithRetry(ctx context.Context, client *http.Client, cfg RetryConfig, buildReq func() (*http.Request, error)) (*http.Response, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 500 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}

	var lastResp *http.Response
	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Previous attempt's body must be released before we loop.
		// An early return inside the loop body also needs to close,
		// which the caller handles on success / non-retryable paths.
		if lastResp != nil {
			lastResp.Body.Close()
			lastResp = nil
		}

		req, err := buildReq()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			// Network errors are retryable; fall through to backoff.
		} else {
			lastErr = nil
			if !isRetryable(resp.StatusCode) {
				return resp, nil
			}
			lastResp = resp
		}

		// Don't sleep after the final attempt.
		if attempt == cfg.MaxAttempts-1 {
			break
		}

		delay := backoffDelay(attempt, cfg, lastResp)
		select {
		case <-ctx.Done():
			if lastResp != nil {
				lastResp.Body.Close()
			}
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", cfg.MaxAttempts, lastErr)
}

// isRetryable reports whether a status code warrants a retry.
// 429 is explicit rate limiting; 5xx is server side and usually
// transient. 4xx other than 429 is client-fault and not retried.
func isRetryable(status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500 && status <= 599
}

// backoffDelay computes the wait before the next attempt. Honors
// Retry-After on the prior response if present (caps at MaxDelay to
// prevent a server suggesting a 10-minute wait from freezing a
// curation cycle). Otherwise, returns cfg.BaseDelay * 2^attempt with
// decorrelated jitter, capped at MaxDelay.
func backoffDelay(attempt int, cfg RetryConfig, prior *http.Response) time.Duration {
	if prior != nil {
		if retryAfter := parseRetryAfter(prior); retryAfter > 0 {
			if retryAfter > cfg.MaxDelay {
				return cfg.MaxDelay
			}
			return retryAfter
		}
	}
	exp := time.Duration(1<<attempt) * cfg.BaseDelay
	if exp > cfg.MaxDelay {
		exp = cfg.MaxDelay
	}
	// Decorrelated jitter: random value in [BaseDelay, exp).
	// Prevents thundering herd when many callers hit the same 429.
	jitter := cfg.BaseDelay + time.Duration(rand.Int64N(int64(exp-cfg.BaseDelay+1)))
	return jitter
}

// parseRetryAfter reads the Retry-After header as either an integer
// number of seconds or an HTTP-date. Returns 0 if the header is
// absent or unparseable.
func parseRetryAfter(resp *http.Response) time.Duration {
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
