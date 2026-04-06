package server

import (
	"testing"
	"time"
)

func TestRetrievalTrackerTrackAndRetrieve(t *testing.T) {
	rt := newRetrievalTracker()

	rt.Track("node-1", "node-2", "node-3")

	ids := rt.RetrievedIDs()
	if len(ids) != 3 {
		t.Fatalf("expected 3 tracked IDs, got %d", len(ids))
	}

	// Verify all three are present.
	found := make(map[string]bool)
	for _, id := range ids {
		found[id] = true
	}
	for _, expected := range []string{"node-1", "node-2", "node-3"} {
		if !found[expected] {
			t.Fatalf("expected %q in tracked IDs", expected)
		}
	}
}

func TestRetrievalTrackerDedup(t *testing.T) {
	rt := newRetrievalTracker()

	rt.Track("node-1")
	rt.Track("node-1") // duplicate
	rt.Track("node-2")

	if rt.Len() != 2 {
		t.Fatalf("expected 2 unique entries, got %d", rt.Len())
	}
}

func TestRetrievalTrackerExpiration(t *testing.T) {
	rt := newRetrievalTracker()
	rt.maxAge = 100 * time.Millisecond

	rt.Track("old-node")
	time.Sleep(150 * time.Millisecond)
	rt.Track("new-node")

	ids := rt.RetrievedIDs() // triggers prune
	if len(ids) != 1 {
		t.Fatalf("expected 1 ID after expiration, got %d", len(ids))
	}
	if ids[0] != "new-node" {
		t.Fatalf("expected new-node to survive, got %q", ids[0])
	}
}

func TestRetrievalTrackerMaxSize(t *testing.T) {
	rt := newRetrievalTracker()
	rt.maxSize = 5

	// Add 10 entries.
	for i := 0; i < 10; i++ {
		rt.Track("node-" + string(rune('A'+i)))
		// Small sleep so timestamps differ for oldest detection.
		time.Sleep(time.Millisecond)
	}

	if rt.Len() > 5 {
		t.Fatalf("expected max 5 entries, got %d", rt.Len())
	}
}

func TestRetrievalTrackerEmpty(t *testing.T) {
	rt := newRetrievalTracker()

	ids := rt.RetrievedIDs()
	if len(ids) != 0 {
		t.Fatalf("expected 0 IDs from empty tracker, got %d", len(ids))
	}

	if rt.Len() != 0 {
		t.Fatalf("expected Len 0, got %d", rt.Len())
	}
}

func TestRetrievalTrackerConcurrent(t *testing.T) {
	rt := newRetrievalTracker()

	done := make(chan struct{})
	// Writer goroutines.
	for i := 0; i < 5; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				rt.Track("node-" + string(rune('A'+n)) + "-" + string(rune('0'+j)))
			}
		}(i)
	}
	// Reader goroutines.
	for i := 0; i < 3; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				_ = rt.RetrievedIDs()
			}
		}()
	}

	for i := 0; i < 8; i++ {
		<-done
	}

	// If we get here without a race condition panic, the test passes.
	if rt.Len() == 0 {
		t.Fatal("expected some tracked entries after concurrent writes")
	}
}
