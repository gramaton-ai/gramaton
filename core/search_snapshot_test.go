package core

import (
	"sync"
	"testing"
	"time"
)

func TestSnapshotStore_PutGetRoundTrip(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	snap := &SearchSnapshot{
		QueryID: s.NewQueryID(),
		IDs:     []string{"01ABC", "01DEF", "01GHI"},
		Scores:  []float32{0.9, 0.8, 0.7},
		Total:   3,
	}
	s.Put(snap)

	got, ok := s.Get(snap.QueryID)
	if !ok {
		t.Fatal("Get returned !ok for a freshly-Put snapshot")
	}
	if got.Total != 3 || len(got.IDs) != 3 || got.IDs[0] != "01ABC" {
		t.Errorf("snapshot mangled in store: %+v", got)
	}
}

func TestSnapshotStore_GetMissingReturnsFalse(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	if _, ok := s.Get("01nonexistent"); ok {
		t.Error("Get on unknown query_id should return !ok")
	}
}

func TestSnapshotStore_GetExpiredReturnsFalseAndEvicts(t *testing.T) {
	s := NewSnapshotStore(time.Hour) // long TTL so eviction loop doesn't fire
	defer s.Stop()

	id := s.NewQueryID()
	s.Put(&SearchSnapshot{
		QueryID:   id,
		IDs:       []string{"x"},
		Total:     1,
		ExpiresAt: time.Now().Add(-time.Second), // already expired
	})

	if _, ok := s.Get(id); ok {
		t.Error("Get on expired snapshot should return !ok")
	}
	if s.Len() != 0 {
		t.Errorf("expired snapshot wasn't lazily evicted on Get; Len=%d", s.Len())
	}
}

func TestSnapshotStore_SweepRemovesExpired(t *testing.T) {
	s := NewSnapshotStore(time.Hour) // long TTL; we control expiry manually
	defer s.Stop()

	now := time.Now()
	s.Put(&SearchSnapshot{QueryID: s.NewQueryID(), Total: 1, ExpiresAt: now.Add(-time.Minute)}) // expired
	s.Put(&SearchSnapshot{QueryID: s.NewQueryID(), Total: 1, ExpiresAt: now.Add(time.Hour)})    // fresh
	s.Put(&SearchSnapshot{QueryID: s.NewQueryID(), Total: 1, ExpiresAt: now.Add(-time.Hour)})   // expired

	removed := s.sweep()
	if removed != 2 {
		t.Errorf("sweep removed %d, want 2", removed)
	}
	if s.Len() != 1 {
		t.Errorf("sweep left %d snapshots, want 1 (the fresh one)", s.Len())
	}
}

func TestSnapshotStore_NewQueryIDIsUnique(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := s.NewQueryID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate QueryID after %d iterations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestSnapshotStore_NewQueryIDIsULIDShaped(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	id := s.NewQueryID()
	if len(id) != 26 {
		t.Errorf("QueryID length = %d, want 26 (ULID)", len(id))
	}
}

func TestSnapshotStore_PutNilNoOps(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	s.Put(nil)
	s.Put(&SearchSnapshot{}) // missing QueryID
	if s.Len() != 0 {
		t.Errorf("nil/empty Put leaked into store; Len=%d", s.Len())
	}
}

func TestSnapshotStore_PutFillsExpiresAtFromTTL(t *testing.T) {
	s := NewSnapshotStore(5 * time.Minute)
	defer s.Stop()

	before := time.Now()
	id := s.NewQueryID()
	s.Put(&SearchSnapshot{QueryID: id, Total: 1}) // ExpiresAt unset

	got, _ := s.Get(id)
	delta := got.ExpiresAt.Sub(before)
	// Expect ExpiresAt ~= now + 5min. Allow generous slack for
	// scheduler jitter.
	if delta < 4*time.Minute+30*time.Second || delta > 5*time.Minute+30*time.Second {
		t.Errorf("ExpiresAt delta = %v, want ~5min", delta)
	}
}

func TestSnapshotStore_StopIsIdempotent(t *testing.T) {
	s := NewSnapshotStore(time.Minute)
	s.Stop()
	s.Stop() // must not panic / hang
}

func TestSnapshotStore_StopTerminatesEvictionLoop(t *testing.T) {
	s := NewSnapshotStore(time.Minute)

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop didn't terminate eviction loop within 2s")
	}
}

func TestSnapshotStore_ConcurrentPutGet(t *testing.T) {
	// Race-detector smoke. Many goroutines hammering Put + Get
	// shouldn't deadlock or trip -race.
	s := NewSnapshotStore(time.Minute)
	defer s.Stop()

	var wg sync.WaitGroup
	const writers = 8
	const reads = 100

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < reads; j++ {
				id := s.NewQueryID()
				s.Put(&SearchSnapshot{
					QueryID: id,
					IDs:     []string{"x"},
					Total:   1,
				})
				_, _ = s.Get(id)
			}
		}()
	}
	wg.Wait()
}

func TestSnapshotStore_BackgroundSweepEvictsExpired(t *testing.T) {
	// Short TTL exercises the eviction loop. evictionLoop's
	// interval clamp lower-bounds at 1s, so the loop will fire
	// within ~1.1s.
	s := NewSnapshotStore(100 * time.Millisecond)
	defer s.Stop()

	id := s.NewQueryID()
	s.Put(&SearchSnapshot{QueryID: id, Total: 1}) // ExpiresAt = now + 100ms

	// Wait long enough for the eviction ticker to fire. The clamp
	// in evictionLoop floors the interval at 1s, so we need to wait
	// at least 1s + scheduling slack. 3s is generous enough to
	// remain reliable even when the test runs alongside many
	// parallel test packages competing for the scheduler.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Len() == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("background sweep didn't evict expired snapshot within 3s; Len=%d", s.Len())
}
