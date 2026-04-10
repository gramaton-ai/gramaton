package graph

import "sync"

// lruTracker maintains LRU ordering for cache eviction. It tracks
// access order via a doubly-linked list and supports O(1) access,
// promote, and eviction. Thread-safe via internal mutex.
//
// The tracker doesn't own the cached data -- it only tracks IDs.
// The caller is responsible for removing evicted entries from the
// actual cache.
type lruTracker struct {
	mu         sync.Mutex
	head, tail *lruEntry
	entries    map[string]*lruEntry
	capacity   int
}

type lruEntry struct {
	id         string
	prev, next *lruEntry
}

// newLRUTracker creates a tracker with the given capacity. A capacity
// of 0 or negative means unlimited (no eviction).
func newLRUTracker(capacity int) *lruTracker {
	return &lruTracker{
		entries:  make(map[string]*lruEntry),
		capacity: capacity,
	}
}

// touch records an access. If the ID is already tracked, it's promoted
// to most-recent. If new and at capacity, returns the evicted ID.
// Returns ("", false) if no eviction needed.
func (l *lruTracker) touch(id string) (evictedID string, evicted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.entries[id]; ok {
		l.moveToFront(e)
		return "", false
	}

	// New entry.
	e := &lruEntry{id: id}
	l.entries[id] = e
	l.pushFront(e)

	// Evict if over capacity.
	if l.capacity > 0 && len(l.entries) > l.capacity {
		victim := l.tail
		l.remove(victim)
		delete(l.entries, victim.id)
		return victim.id, true
	}
	return "", false
}

// removeID drops an ID from the tracker (e.g., when the node is deleted).
func (l *lruTracker) removeID(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.entries[id]; ok {
		l.remove(e)
		delete(l.entries, id)
	}
}

func (l *lruTracker) pushFront(e *lruEntry) {
	e.prev = nil
	e.next = l.head
	if l.head != nil {
		l.head.prev = e
	}
	l.head = e
	if l.tail == nil {
		l.tail = e
	}
}

func (l *lruTracker) remove(e *lruEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		l.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		l.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

func (l *lruTracker) moveToFront(e *lruEntry) {
	if l.head == e {
		return
	}
	l.remove(e)
	l.pushFront(e)
}
