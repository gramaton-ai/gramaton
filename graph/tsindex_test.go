package graph

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newTestTSIndex opens a fresh bbolt db in t.TempDir() and returns a
// ready TSIndex. t.Cleanup handles bolt.Close. Separate from the
// engine-level fixtures so these tests stay focused on the index.
func newTestTSIndex(t *testing.T) *TSIndex {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	idx, err := NewTSIndex(db)
	if err != nil {
		t.Fatalf("NewTSIndex: %v", err)
	}
	return idx
}

// putAt is a shorthand used by the date-focused tests below. The hash
// is synthesized from the label so each commit gets a distinct key.
// tsKey caps the short-hash at 12 chars; padding past that is cosmetic.
func putAt(t *testing.T, idx *TSIndex, ts time.Time, label string) string {
	t.Helper()
	hash := label + "0000000000000000000000"
	c := &Commit{Hash: hash, Timestamp: ts}
	if err := idx.Put(c); err != nil {
		t.Fatalf("Put(%s): %v", label, err)
	}
	return hash
}

func TestTSIndexPutIsIdempotent(t *testing.T) {
	idx := newTestTSIndex(t)
	ts := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	putAt(t, idx, ts, "a")
	if got := idx.Count(); got != 1 {
		t.Fatalf("after first Put: Count = %d, want 1", got)
	}
	// Re-put the exact same commit. Same key, same value. Count stays.
	putAt(t, idx, ts, "a")
	if got := idx.Count(); got != 1 {
		t.Errorf("after repeat Put: Count = %d, want 1 (idempotent)", got)
	}
}

func TestTSIndexCommitAtEmptyBucket(t *testing.T) {
	idx := newTestTSIndex(t)
	h, ok := idx.CommitAt(time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC))
	if ok || h != "" {
		t.Errorf("empty bucket: got (%q, %v), want (\"\", false)", h, ok)
	}
}

func TestTSIndexCommitAtExactHit(t *testing.T) {
	idx := newTestTSIndex(t)
	ts := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	want := putAt(t, idx, ts, "a")

	got, ok := idx.CommitAt(ts)
	if !ok {
		t.Fatalf("exact-hit: expected ok=true, got false")
	}
	if got != want {
		t.Errorf("exact-hit: hash mismatch, got %q want %q", got, want)
	}
}

func TestTSIndexCommitAtBetweenSnapsToPrior(t *testing.T) {
	idx := newTestTSIndex(t)
	earlier := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	later := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	earlierHash := putAt(t, idx, earlier, "a")
	putAt(t, idx, later, "b")

	query := time.Date(2026, 4, 21, 6, 0, 0, 0, time.UTC)
	got, ok := idx.CommitAt(query)
	if !ok {
		t.Fatalf("between: expected ok=true")
	}
	if got != earlierHash {
		t.Errorf("between: want snap-to-prior (%q), got %q", earlierHash, got)
	}
}

func TestTSIndexCommitAtBeforeFirst(t *testing.T) {
	idx := newTestTSIndex(t)
	putAt(t, idx, time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC), "a")

	query := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	got, ok := idx.CommitAt(query)
	if ok || got != "" {
		t.Errorf("before-first: got (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestTSIndexCommitAtAfterLast(t *testing.T) {
	idx := newTestTSIndex(t)
	lastHash := putAt(t, idx, time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC), "a")

	query := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	got, ok := idx.CommitAt(query)
	if !ok {
		t.Fatalf("after-last: expected ok=true")
	}
	if got != lastHash {
		t.Errorf("after-last: got %q, want %q", got, lastHash)
	}
}

func TestTSIndexCommitBefore(t *testing.T) {
	idx := newTestTSIndex(t)
	t1 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	h1 := putAt(t, idx, t1, "a")
	h2 := putAt(t, idx, t2, "b")

	// Strictly before t1: nothing.
	if h, ok := idx.CommitBefore(t1); ok || h != "" {
		t.Errorf("before-first: got (%q, %v), want (\"\", false)", h, ok)
	}
	// Exactly at t1: strict-before excludes t1, so nothing.
	if h, ok := idx.CommitBefore(t1); ok || h != "" {
		t.Errorf("equal-t1: got (%q, %v), want (\"\", false)", h, ok)
	}
	// Between t1 and t2: h1.
	midpoint := t1.Add(12 * time.Hour)
	if h, _ := idx.CommitBefore(midpoint); h != h1 {
		t.Errorf("between: got %q, want %q", h, h1)
	}
	// Exactly at t2: h1 (strict-before t2).
	if h, _ := idx.CommitBefore(t2); h != h1 {
		t.Errorf("equal-t2: got %q, want %q (strict-before excludes t2)", h, h1)
	}
	// After t2: h2 (last commit).
	if h, _ := idx.CommitBefore(t2.Add(time.Hour)); h != h2 {
		t.Errorf("after-last: got %q, want %q", h, h2)
	}
}

func TestTSIndexCommitsBetweenInclusive(t *testing.T) {
	idx := newTestTSIndex(t)
	t1 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	h1 := putAt(t, idx, t1, "a")
	h2 := putAt(t, idx, t2, "b")
	h3 := putAt(t, idx, t3, "c")

	// Inclusive on both ends: endpoints are returned.
	got := idx.CommitsBetween(t1, t3)
	want := []string{h1, h2, h3}
	if !stringSliceEqual(got, want) {
		t.Errorf("inclusive both: got %v, want %v", got, want)
	}

	// Narrow to middle two.
	got = idx.CommitsBetween(t1.Add(time.Second), t3.Add(-time.Second))
	want = []string{h2}
	if !stringSliceEqual(got, want) {
		t.Errorf("narrow: got %v, want %v", got, want)
	}
}

func TestTSIndexCommitsBetweenEmptyRangeAndEmptyStore(t *testing.T) {
	idx := newTestTSIndex(t)

	// Empty store, any range -> nil.
	got := idx.CommitsBetween(
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	)
	if got != nil {
		t.Errorf("empty store: got %v, want nil", got)
	}

	// Populated store but range before first commit.
	putAt(t, idx, time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC), "a")
	got = idx.CommitsBetween(
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	)
	if got != nil {
		t.Errorf("range-before-first: got %v, want nil", got)
	}

	// Inverted range (end < start) -> nil.
	got = idx.CommitsBetween(
		time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	)
	if got != nil {
		t.Errorf("inverted range: got %v, want nil", got)
	}
}

func TestTSIndexCollidingNanosecondDifferentHashes(t *testing.T) {
	// Two commits in the same nanosecond — short-hash suffix
	// disambiguates the key so both are stored.
	idx := newTestTSIndex(t)
	ts := time.Date(2026, 4, 20, 12, 0, 0, 42, time.UTC)
	h1 := putAt(t, idx, ts, "a")
	h2 := putAt(t, idx, ts, "b")

	if got := idx.Count(); got != 2 {
		t.Fatalf("two-at-same-ns: Count = %d, want 2", got)
	}
	got := idx.CommitsBetween(ts, ts)
	if len(got) != 2 {
		t.Fatalf("two-at-same-ns: Between = %v, want 2 entries", got)
	}
	// Both hashes present, order determined by short-hash sort.
	seen := map[string]bool{}
	for _, h := range got {
		seen[h] = true
	}
	if !seen[h1] || !seen[h2] {
		t.Errorf("two-at-same-ns: missing one of (%q, %q), got %v", h1, h2, got)
	}
}

func TestTSIndexTimestampsInDifferentTimezonesEqual(t *testing.T) {
	// Same instant expressed in PST vs UTC must produce identical keys.
	// tsKey calls .UTC() internally; this protects that contract.
	idx := newTestTSIndex(t)
	pst := time.FixedZone("PST", -8*3600)
	tsPST := time.Date(2026, 4, 20, 4, 0, 0, 0, pst)
	tsUTC := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	hashPST := putAt(t, idx, tsPST, "a")
	// If the UTC conversion works, the second put writes to the same key.
	c := &Commit{Hash: hashPST, Timestamp: tsUTC}
	if err := idx.Put(c); err != nil {
		t.Fatalf("Put UTC: %v", err)
	}

	if got := idx.Count(); got != 1 {
		t.Errorf("zone-equivalence: Count = %d, want 1 (same instant)", got)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
