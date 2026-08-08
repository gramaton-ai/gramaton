package graph

import (
	"sync"
	"testing"
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
