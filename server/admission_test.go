package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	xrate "golang.org/x/time/rate"
)

func TestIsWriteRequest(t *testing.T) {
	cases := []struct {
		method, path string
		write        bool
	}{
		// Reads: GET is always a read.
		{"GET", "/v1/search", false},
		{"GET", "/v1/records/abc", false},
		{"GET", "/v1/health", false},
		{"HEAD", "/v1/status", false},
		// POST-but-read: take a JSON body but do not mutate the store.
		{"POST", "/v1/search", false},
		{"POST", "/v1/explore", false},
		{"POST", "/v1/duplicates", false},
		{"POST", "/v1/backup", false},
		{"POST", "/v1/export", false},
		{"POST", "/v1/store/carve", false},
		{"POST", "/v1/store/add", false},
		{"POST", "/v1/save/batch/job-123/cancel", false},
		// POST writes.
		{"POST", "/v1/records", true},
		{"POST", "/v1/save/batch", true},
		{"POST", "/v1/sessions/s1/save", true},
		{"POST", "/v1/intake", true},
		{"POST", "/v1/revert", true},
		{"POST", "/v1/ingest", true},
		// The /mcp tunnel carries write tools; treated conservatively as a write.
		{"POST", "/mcp", true},
		// Non-POST writes.
		{"PATCH", "/v1/records/abc", true},
		{"PUT", "/v1/collections/c1/schema", true},
		{"DELETE", "/v1/records/abc", true},
		{"DELETE", "/v1/edges/e1", true},
		// Unknown method: treated as a write so nothing mutating slips through.
		{"OPTIONS", "/v1/records", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := isWriteRequest(req); got != tc.write {
			t.Errorf("%s %s: isWriteRequest=%v want %v", tc.method, tc.path, got, tc.write)
		}
	}
}

// countingHandler records how many times it ran and always returns 200.
func countingHandler(served *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*served++
		w.WriteHeader(http.StatusOK)
	})
}

// TestAdmitWritesLoopbackExempt pins that a loopback caller bypasses both
// gates entirely. The limiter (burst 1) and the full concurrency gate
// would shed a remote caller after the first write; loopback must not be
// touched. Bug-pin: dropping the isLoopback check shed writes 2..10.
func TestAdmitWritesLoopbackExempt(t *testing.T) {
	s := &Server{
		writeLimiter: xrate.NewLimiter(xrate.Limit(0.001), 1),
		writeSlots:   make(chan struct{}, 1),
	}
	var served int
	h := s.admitWrites(countingHandler(&served))
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/v1/records", nil)
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback write %d shed with %d; loopback must be exempt", i, rec.Code)
		}
	}
	if served != 10 {
		t.Fatalf("served=%d want 10", served)
	}
}

// TestAdmitWritesReadsNotThrottled pins that reads bypass the write
// limiter even from a remote caller. Bug-pin: classifying search as a
// write would 429 writes 2..10.
func TestAdmitWritesReadsNotThrottled(t *testing.T) {
	s := &Server{writeLimiter: xrate.NewLimiter(xrate.Limit(0.001), 1)}
	var served int
	h := s.admitWrites(countingHandler(&served))
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/v1/search", nil) // POST but read
		req.RemoteAddr = "203.0.113.5:9999"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("remote read %d shed with %d; reads must not be throttled", i, rec.Code)
		}
	}
	if served != 10 {
		t.Fatalf("served=%d want 10", served)
	}
}

// TestAdmitWritesRemoteWritesThrottled pins that a remote write flood is
// shed once the token bucket drains, with a well-formed 429.
func TestAdmitWritesRemoteWritesThrottled(t *testing.T) {
	// rate ~0 so the bucket never refills within the test; burst 2.
	s := &Server{writeLimiter: xrate.NewLimiter(xrate.Limit(0.001), 2)}
	var served int
	h := s.admitWrites(countingHandler(&served))

	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/v1/records", nil)
		req.RemoteAddr = "203.0.113.5:9999"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if served != 2 {
		t.Fatalf("served=%d want 2 (burst)", served)
	}
	for i := 0; i < 2; i++ {
		if codes[i] != http.StatusOK {
			t.Errorf("write %d: got %d want 200", i, codes[i])
		}
	}
	for i := 2; i < 5; i++ {
		if codes[i] != http.StatusTooManyRequests {
			t.Errorf("write %d: got %d want 429", i, codes[i])
		}
	}

	// Inspect the shed response shape.
	req := httptest.NewRequest("POST", "/v1/records", nil)
	req.RemoteAddr = "203.0.113.5:9999"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must set a Retry-After header")
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal 429 body: %v", err)
	}
	if resp.Error.Code != "too_many_requests" {
		t.Errorf("code=%q want too_many_requests", resp.Error.Code)
	}
	if !resp.Error.Retryable {
		t.Error("429 must be retryable")
	}
	if resp.Error.RetryAfter <= 0 {
		t.Errorf("retry_after=%d want > 0", resp.Error.RetryAfter)
	}
}

// TestAdmitWritesConcurrencyCap pins that the in-flight gate sheds a
// write when all slots are held, and frees the slot on completion.
func TestAdmitWritesConcurrencyCap(t *testing.T) {
	s := &Server{writeSlots: make(chan struct{}, 1)} // one slot, no rate limiter
	started := make(chan struct{})
	release := make(chan struct{})
	firstCode := make(chan int, 1)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	h := s.admitWrites(next)

	// Request 1 acquires the only slot and blocks inside the handler.
	go func() {
		req := httptest.NewRequest("POST", "/v1/records", nil)
		req.RemoteAddr = "203.0.113.5:1111"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		firstCode <- rec.Code
	}()
	<-started // slot now held

	// Request 2 finds the slot full and is shed.
	req2 := httptest.NewRequest("POST", "/v1/records", nil)
	req2.RemoteAddr = "203.0.113.5:2222"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second concurrent write: got %d want 429", rec2.Code)
	}

	// Release request 1; it completes and frees the slot.
	close(release)
	if code := <-firstCode; code != http.StatusOK {
		t.Fatalf("first write: got %d want 200", code)
	}

	// A fresh write now finds the slot free.
	req3 := httptest.NewRequest("POST", "/v1/records", nil)
	req3.RemoteAddr = "203.0.113.5:3333"
	rec3 := httptest.NewRecorder()
	// Non-blocking handler for the last request.
	h2 := s.admitWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h2.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("third write after release: got %d want 200 (slot should be free)", rec3.Code)
	}
}

// TestAdmitWritesRateShedReleasesConcurrencySlot pins that a remote write
// which passes the concurrency gate but is then shed by the rate limiter
// still releases its slot -- otherwise a sustained flood would drain the
// bounded gate and wedge all subsequent writes. Both gates are active
// (the interaction is otherwise untested). Bug-pin: dropping the deferred
// slot release so the slot frees only on the success path leaks a slot on
// every rate-shed, and write 3 below would 429 on the wedged gate.
func TestAdmitWritesRateShedReleasesConcurrencySlot(t *testing.T) {
	s := &Server{
		writeLimiter: xrate.NewLimiter(xrate.Limit(0.001), 1), // one token, then dry
		writeSlots:   make(chan struct{}, 1),                  // single slot
	}
	var served int
	h := s.admitWrites(countingHandler(&served))

	remote := func() int {
		req := httptest.NewRequest("POST", "/v1/records", nil)
		req.RemoteAddr = "203.0.113.5:9999"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Write 1: passes the concurrency gate and consumes the one token.
	if c := remote(); c != http.StatusOK {
		t.Fatalf("write 1: got %d want 200", c)
	}
	// Write 2: passes the concurrency gate (slot freed after write 1
	// returned) but the bucket is dry -> shed by the rate limiter. This
	// is the path that must release the slot.
	if c := remote(); c != http.StatusTooManyRequests {
		t.Fatalf("write 2: got %d want 429 (rate shed)", c)
	}
	// Write 3 with a refilled limiter must be served. If write 2 leaked
	// its slot, the single-slot gate is now permanently full and this
	// would 429 on the concurrency gate instead of being served.
	s.writeLimiter = xrate.NewLimiter(xrate.Limit(0.001), 1)
	if c := remote(); c != http.StatusOK {
		t.Fatalf("write 3 after refill: got %d want 200; a leaked slot wedged the concurrency gate", c)
	}
}

// TestAdmitWritesNoGatesPassthrough pins that with admission disabled
// (both gates nil -- e.g. a loopback-only server, or both dimensions
// turned off) even a remote write passes unthrottled.
func TestAdmitWritesNoGatesPassthrough(t *testing.T) {
	s := &Server{} // writeLimiter and writeSlots both nil
	var served int
	h := s.admitWrites(countingHandler(&served))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/v1/records", nil)
		req.RemoteAddr = "203.0.113.5:9999"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("write %d shed with %d; nil gates must not throttle", i, rec.Code)
		}
	}
	if served != 5 {
		t.Fatalf("served=%d want 5", served)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{
		{-time.Second, 1},           // negative -> floor 1
		{0, 1},                      // zero -> floor 1
		{300 * time.Millisecond, 1}, // rounds up to 1
		{time.Second, 1},
		{1500 * time.Millisecond, 2}, // rounds up
		{2300 * time.Millisecond, 3},
		{3 * time.Second, 3}, // exact
	}
	for _, tc := range cases {
		if got := retryAfterSeconds(tc.d); got != tc.want {
			t.Errorf("retryAfterSeconds(%v)=%d want %d", tc.d, got, tc.want)
		}
	}
}

func TestWriteTooManyEnvelope(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeTooMany(rec, "slow down", 3)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After=%q want 3", got)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error.Code != "too_many_requests" || !resp.Error.Retryable || resp.Error.RetryAfter != 3 {
		t.Errorf("error detail = %+v", resp.Error)
	}
	// Lean envelope: no curation/store-state, like the 401 path.
	if resp.Curation != (CurationStatus{}) {
		t.Errorf("429 must omit the curation envelope; got %+v", resp.Curation)
	}
}

func TestWriteTooManyFloorsRetryAfter(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeTooMany(rec, "x", 0)
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After=%q want floored to 1", got)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error.RetryAfter != 1 {
		t.Errorf("retry_after=%d want 1", resp.Error.RetryAfter)
	}
}

func TestBuildWriteLimiter(t *testing.T) {
	// Fully configured.
	s := &Server{cfg: Config{Remote: RemoteRuntime{WriteRate: 10, WriteBurst: 20, MaxConcurrentWrites: 8}}}
	s.buildWriteLimiter()
	if s.writeLimiter == nil {
		t.Fatal("expected a limiter")
	}
	if got := float64(s.writeLimiter.Limit()); got != 10 {
		t.Errorf("rate=%v want 10", got)
	}
	if got := s.writeLimiter.Burst(); got != 20 {
		t.Errorf("burst=%d want 20", got)
	}
	if s.writeSlots == nil || cap(s.writeSlots) != 8 {
		t.Errorf("slots cap=%d want 8", cap(s.writeSlots))
	}

	// Disabled dimensions leave the gates nil.
	s2 := &Server{cfg: Config{Remote: RemoteRuntime{WriteRate: -1, MaxConcurrentWrites: -1}}}
	s2.buildWriteLimiter()
	if s2.writeLimiter != nil {
		t.Error("negative rate should leave the limiter nil")
	}
	if s2.writeSlots != nil {
		t.Error("negative concurrency should leave slots nil")
	}

	// Zero RemoteRuntime (remote access disabled) builds no gates.
	s3 := &Server{}
	s3.buildWriteLimiter()
	if s3.writeLimiter != nil || s3.writeSlots != nil {
		t.Error("zero runtime should build no gates")
	}

	// Rate on with a zero burst floors the bucket at 1 (a zero-size
	// bucket would reject every write).
	s4 := &Server{cfg: Config{Remote: RemoteRuntime{WriteRate: 5, WriteBurst: 0}}}
	s4.buildWriteLimiter()
	if s4.writeLimiter == nil || s4.writeLimiter.Burst() != 1 {
		t.Errorf("burst floor not applied: %+v", s4.writeLimiter)
	}
}
