package api

import (
	"sync"
	"time"
)

// RetrievalTracker records which node IDs were served to agents via
// search/inspect/explore. The observe pipeline's feedback-loop
// detection reads this to avoid re-extracting knowledge that was just
// retrieved. Moved from server.Server because the track-serving path
// is now owned by api methods (formerly serviceSearch/serviceInspect/
// serviceExplore lived on *Server and called s.retrieval).
type RetrievalTracker struct {
	mu      sync.Mutex
	entries map[string]time.Time
	maxAge  time.Duration
	maxSize int
}

// NewRetrievalTracker returns a tracker with the historical defaults
// (4h max age, 500 max size). Caller can adjust via SetBounds.
func NewRetrievalTracker() *RetrievalTracker {
	return &RetrievalTracker{
		entries: make(map[string]time.Time),
		maxAge:  4 * time.Hour,
		maxSize: 500,
	}
}

// Track records that the given IDs were served to an agent.
func (rt *RetrievalTracker) Track(ids ...string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range ids {
		rt.entries[id] = now
	}
	if len(rt.entries) > rt.maxSize {
		rt.pruneOldestLocked()
	}
}

// RetrievedIDs returns all currently tracked, not-yet-expired IDs.
func (rt *RetrievalTracker) RetrievedIDs() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.pruneExpiredLocked()
	ids := make([]string, 0, len(rt.entries))
	for id := range rt.entries {
		ids = append(ids, id)
	}
	return ids
}

// Len returns the current tracked entry count (after pruning
// expired entries).
func (rt *RetrievalTracker) Len() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.pruneExpiredLocked()
	return len(rt.entries)
}

func (rt *RetrievalTracker) pruneExpiredLocked() {
	cutoff := time.Now().UTC().Add(-rt.maxAge)
	for id, t := range rt.entries {
		if t.Before(cutoff) {
			delete(rt.entries, id)
		}
	}
}

func (rt *RetrievalTracker) pruneOldestLocked() {
	for len(rt.entries) > rt.maxSize {
		var oldestID string
		var oldestTime time.Time
		for id, t := range rt.entries {
			if oldestID == "" || t.Before(oldestTime) {
				oldestID = id
				oldestTime = t
			}
		}
		if oldestID != "" {
			delete(rt.entries, oldestID)
		}
	}
}
