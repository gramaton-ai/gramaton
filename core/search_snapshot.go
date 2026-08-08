package core

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// SearchSnapshot captures the result set of a search query at a
// point in time. Stores IDs + scores only -- record content is
// fetched fresh on each page response so modifications to a
// still-present record show up on subsequent reads. Deletions
// manifest as silently-missing records (page returns N-1).
//
// Snapshots are short-lived (TTL configured at the store) and
// keyed by a ULID QueryID generated at creation time. Pagination
// cursor tokens encode (QueryID, start, end) into the snapshot.
type SearchSnapshot struct {
	// QueryID is the unique handle for this snapshot. ULID-shaped
	// so debugging and log correlation work like for any other
	// node ID in the system.
	QueryID string

	// IDs are the matched record IDs in ranked order. The order is
	// load-bearing -- pagination slices into this slice by index.
	IDs []string

	// Scores parallel IDs (Scores[i] is the score for IDs[i]).
	// Stale relative to the live index by the time a paginated
	// call slices the snapshot, but recorded for debugging /
	// auditing. Scores are not re-computed on cursor pages.
	Scores []float32

	// Total is the count materialized into the snapshot. Equal to
	// len(IDs); cached for response shape clarity.
	Total int

	// Truncated indicates the underlying ranked candidate set
	// exceeded the snapshot's candidate cap. Callers should signal
	// this in the response so agents know more results exist
	// beyond the snapshot.
	Truncated bool

	// ExpiresAt is when this snapshot becomes invalid. Lazy
	// eviction on Get + periodic sweep both honor this.
	ExpiresAt time.Time
}

// SnapshotStore caches recent search results so paginated calls
// slice into a stable matched-set without re-running the query.
// Eviction is periodic (background goroutine) plus lazy (on Get
// of an expired snapshot). Safe for concurrent use.
//
// One store per engine; lifetime tied to engine lifetime. Caller
// must call Stop on shutdown to terminate the eviction goroutine
// cleanly.
type SnapshotStore struct {
	mu        sync.Mutex
	snapshots map[string]*SearchSnapshot
	ttl       time.Duration
	entropy   *ulid.MonotonicEntropy

	stopCh   chan struct{}
	stopOnce sync.Once
	stopWG   sync.WaitGroup
}

// NewSnapshotStore creates a SnapshotStore with the given TTL and
// starts the background eviction loop. Caller must call Stop on
// shutdown.
func NewSnapshotStore(ttl time.Duration) *SnapshotStore {
	s := &SnapshotStore{
		snapshots: make(map[string]*SearchSnapshot),
		ttl:       ttl,
		entropy:   ulid.Monotonic(rand.Reader, 0),
		stopCh:    make(chan struct{}),
	}
	s.stopWG.Add(1)
	go s.evictionLoop()
	return s
}

// NewQueryID mints a fresh ULID for use as a snapshot's QueryID.
// Exposed so callers can pre-mint the ID before populating the
// rest of the snapshot.
func (s *SnapshotStore) NewQueryID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), s.entropy).String()
}

// Put stores a snapshot. If snap.ExpiresAt is the zero time, it's
// set from the store's TTL; tests can pre-set it for deterministic
// expiry behavior.
func (s *SnapshotStore) Put(snap *SearchSnapshot) {
	if snap == nil || snap.QueryID == "" {
		return
	}
	if snap.ExpiresAt.IsZero() {
		snap.ExpiresAt = time.Now().Add(s.ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snap.QueryID] = snap
}

// Get returns the snapshot for queryID, or nil + false if missing
// or expired. Expired snapshots are removed on access (lazy
// eviction) so successive Gets after expiry don't repeatedly
// surface a doomed entry.
func (s *SnapshotStore) Get(queryID string) (*SearchSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snapshots[queryID]
	if !ok {
		return nil, false
	}
	if time.Now().After(snap.ExpiresAt) {
		delete(s.snapshots, queryID)
		return nil, false
	}
	return snap, true
}

// Stop terminates the eviction goroutine and blocks until it
// exits. Idempotent; subsequent calls are no-ops.
func (s *SnapshotStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.stopWG.Wait()
}

// sweep removes all expired snapshots. Returns the number removed.
// Internal; the eviction loop calls this on a ticker.
func (s *SnapshotStore) sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	removed := 0
	for id, snap := range s.snapshots {
		if now.After(snap.ExpiresAt) {
			delete(s.snapshots, id)
			removed++
		}
	}
	return removed
}

// evictionLoop sweeps expired snapshots periodically. Cadence is
// TTL/4, clamped to [1s, 1m]. The clamp keeps short-TTL stores
// (used in tests with sub-second TTLs) from spinning, and stops
// long-TTL stores from accumulating expired entries between
// sweeps.
func (s *SnapshotStore) evictionLoop() {
	defer s.stopWG.Done()
	interval := s.ttl / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.sweep()
		}
	}
}
