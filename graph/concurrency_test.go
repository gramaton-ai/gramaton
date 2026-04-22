package graph

import (
	"sync"
	"testing"
	"time"
)

// TestGetNodeConcurrentWithIterator stresses the cacheMu protocol.
// GetNode mutated g.nodes during lazy load while cachedIterator
// iterated it without a lock. Under -race this would fire a data
// race; without -race the Go runtime can panic on concurrent map
// write during iteration.
//
// In production these races were possible because both readers (search,
// inspect) and lazy loads happen under engine.RLock, which permits
// concurrent goroutines.
func TestGetNodeConcurrentWithIterator(t *testing.T) {
	g := New()

	// Seed with enough nodes that iteration takes long enough to
	// overlap concurrent GetNode calls.
	const N = 200
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		n := g.AddNode(Properties{"i": Int64Property(int64(i))})
		ids[i] = n.ID
	}

	const iterations = 200
	var wg sync.WaitGroup

	// Iterators: take snapshots concurrently.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				it := g.NodeIterator()
				for it.Next() {
					_ = it.Node()
				}
				it.Close()
			}
		}()
	}

	// Readers: hammer GetNode (LRU touch mutates state every call).
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations*N; i++ {
				_, _ = g.GetNode(ids[i%N])
			}
		}()
	}

	wg.Wait()
}

// TestRecordAccessLazyLoadsEvictedNode verifies RecordAccess handles
// evicted nodes. RecordAccess used to read g.nodes directly, so for
// nodes that had been evicted from the cache (lazy mode), it silently
// no-op'd. Now it routes through GetNode, which lazy-loads the node
// back so the access is actually recorded.
func TestRecordAccessLazyLoadsEvictedNode(t *testing.T) {
	// Save a graph with one node, then load it into a tiny-cache
	// graph, evict the node, and verify RecordAccess still works.
	g := New()
	s := tempStorage(t)

	n := g.AddNode(Properties{"content": StringProperty("test")})
	commit, err := g.Save(s, "", "seed")
	if err != nil {
		t.Fatalf("seed save: %v", err)
	}
	id := n.ID

	g2 := NewWithCapacity(1)
	if _, err := g2.Load(s, commit.Hash); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Force eviction: load several other nodes first. Since there's
	// only one in this store, simulate eviction by directly clearing
	// the cache for that ID.
	g2.cacheMu.Lock()
	delete(g2.nodes, id)
	g2.lru.removeID(id)
	g2.cacheMu.Unlock()

	// RecordAccess: previously this would silently no-op because
	// g.nodes[id] returned nothing.
	g2.RecordAccess(id, time.Now().UTC(), ActivationConfig{
		BaseAmount:        0.1,
		AttenuationFactor: 0.5,
	})

	// Verify access was recorded by re-fetching the node.
	loaded, ok := g2.GetNode(id)
	if !ok {
		t.Fatalf("node %s not found after RecordAccess", id)
	}
	count, _ := loaded.Properties.GetInt64("access_count")
	if count != 1 {
		t.Fatalf("expected access_count=1 after RecordAccess on evicted node, got %d", count)
	}
}
