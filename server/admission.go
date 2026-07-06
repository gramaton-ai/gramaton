package server

import (
	"math"
	"net/http"
	"strings"
	"time"
)

// admitWrites is the write-path admission-control middleware. It shields
// the engine's single write lock from a remote write flood: an
// authenticated remote caller's writes pass through a bounded in-flight
// concurrency gate and a token-bucket rate limiter, and are shed with
// 429 + Retry-After when either is exceeded.
//
// Loopback callers (the local CLI, hooks, and MCP proxy) are never
// limited, and reads are never limited -- only the write path contends
// on the lock. It sits INSIDE authenticate in the chain, so every
// request it sees has already passed the auth gate: the only non-
// loopback callers here are authenticated remotes. Both gates are nil
// unless remote access is enabled with limits configured, so a loopback-
// only server pays nothing.
func (s *Server) admitWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r) || !isWriteRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Concurrency gate first: a non-blocking acquire has no side
		// effect on failure, so shedding here spends no rate token. The
		// slot is held for the whole handler (a slow embedding write
		// keeps its slot), released on return.
		if s.writeSlots != nil {
			select {
			case s.writeSlots <- struct{}{}:
				defer func() { <-s.writeSlots }()
			default:
				s.writeTooMany(w, "too many concurrent writes in flight; retry shortly", 1)
				return
			}
		}
		// Rate limiter: one token per write REQUEST. A batch write costs
		// one token regardless of record count; the concurrency gate
		// above is the batch-size-independent bound on in-flight write
		// work against the engine lock.
		if s.writeLimiter != nil {
			res := s.writeLimiter.Reserve()
			delay := res.Delay()
			if !res.OK() || delay > 0 {
				// Never actually wait -- return the token and shed, so
				// the caller (not the server) holds the backoff.
				res.Cancel()
				s.writeTooMany(w, "write rate limit exceeded; retry after the suggested delay", retryAfterSeconds(delay))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isWriteRequest reports whether an HTTP request mutates store state, so
// the admission gate should limit it. GET/HEAD are always reads.
// PATCH/PUT/DELETE are always writes. POST is a write UNLESS it hits one
// of the endpoints that take a request body but only read (search,
// explore, ...). An unrecognized method is treated as a write so a new
// mutating verb is never silently exempted. The /mcp tunnel carries both
// read and write tools in its body, so it is conservatively a write.
func isWriteRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return false
	case http.MethodPost:
		return !isReadPost(r.URL.Path)
	default:
		return true
	}
}

// readPostPaths are the endpoints that use POST because they take a JSON
// request body, yet only read store state, so the write gate exempts
// them. Every entry MUST be a genuine read: a missing entry only over-
// limits a read (harmless), but an erroneous entry would exempt a write
// from the gate. Mirror of the guardRead POST routes in the bindings.
var readPostPaths = map[string]bool{
	"/v1/search":      true,
	"/v1/explore":     true,
	"/v1/duplicates":  true,
	"/v1/backup":      true, // BackupCreate: reads the store, writes outside its data dir
	"/v1/export":      true,
	"/v1/store/carve": true, // reads source store, writes a new dest store
	"/v1/store/add":   true,
}

func isReadPost(path string) bool {
	if readPostPaths[path] {
		return true
	}
	// POST /v1/save/batch/{job_id}/cancel cancels a running save job; it
	// does not mutate the store, so it is classified read.
	return strings.HasPrefix(path, "/v1/save/batch/") && strings.HasSuffix(path, "/cancel")
}

// retryAfterSeconds converts a limiter delay into whole seconds for the
// Retry-After header, rounding up so a caller never retries early, with
// a floor of 1.
func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int(math.Ceil(d.Seconds()))
}
