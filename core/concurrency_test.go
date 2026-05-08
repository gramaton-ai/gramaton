package core

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// windowsTimeout scales a test timeout for the host platform.
// Windows CI runners are 3-5x slower than Linux/macOS for I/O-heavy
// paths under the race detector, so hard-coded short budgets that
// fit POSIX timing exhaust on Windows and writers exit early --
// surfacing as misleading "data corruption" assertion messages
// when the actual problem is "didn't have time to do the writes".
//
// Inlined here because core/ can't import testutil/ (testutil
// imports core, and splitting out a sub-package for one helper
// isn't worth the indirection). Mirrors testutil.Timeout's
// 3x-on-Windows policy.
func windowsTimeout(base time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return base * 3
	}
	return base
}

func TestConcurrentReads(t *testing.T) {
	eng := setupTestEngine(t)

	// Add some records.
	eng.Lock()
	for i := 0; i < 20; i++ {
		n := eng.Graph().AddNode(graph.Properties{
			"content_full": graph.StringProperty(fmt.Sprintf("Record %d", i)),
			"temporality":  graph.StringProperty("durable"),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
			"access_count": graph.Int64Property(0),
		})
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
	}
	eng.Save("seed")
	eng.Unlock()

	// Hammer with concurrent reads -- should not deadlock or panic.
	var wg sync.WaitGroup
	errs := make(chan error, 100)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				eng.RLock()
				ids := eng.Graph().AllNodeIDs()
				if len(ids) == 0 {
					eng.RUnlock()
					errs <- fmt.Errorf("got 0 nodes")
					return
				}
				// Read a random node.
				n, ok := eng.Graph().GetNode(ids[0])
				if !ok {
					eng.RUnlock()
					errs <- fmt.Errorf("node not found")
					return
				}
				_ = n.Properties
				eng.RUnlock()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent reads deadlocked (10s timeout)")
	}

	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	eng := setupTestEngine(t)

	// Seed some data.
	eng.Lock()
	for i := 0; i < 10; i++ {
		n := eng.Graph().AddNode(graph.Properties{
			"content_full": graph.StringProperty(fmt.Sprintf("Seed %d", i)),
			"temporality":  graph.StringProperty("durable"),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
			"access_count": graph.Int64Property(0),
		})
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
	}
	eng.Save("seed")
	eng.Unlock()

	var wg sync.WaitGroup
	// Bumped from 10s base to 20s after PR #52 surfaced
	// "expected 70 nodes, got 67" -- 30s on Windows wasn't enough for
	// 60 writes through bbolt under race + parallel-suite load. 60s
	// gives ~1s/write of headroom on slow Windows CI.
	ctx, cancel := context.WithTimeout(context.Background(), windowsTimeout(20*time.Second))
	defer cancel()

	// 10 readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.RLock()
				ids := eng.Graph().AllNodeIDs()
				for _, id := range ids {
					eng.Graph().GetNode(id)
				}
				// Use PropIdx under read lock.
				eng.PropIdx().Lookup("temporality", graph.StringProperty("durable"))
				eng.RUnlock()
			}
		}()
	}

	// 3 writers.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.Lock()
				n := eng.Graph().AddNode(graph.Properties{
					"content_full": graph.StringProperty(fmt.Sprintf("Writer %d record %d", id, j)),
					"temporality":  graph.StringProperty("temporal"),
					"created_at":   graph.TimestampProperty(time.Now().UTC()),
					"access_count": graph.Int64Property(0),
				})
				for k, v := range n.Properties {
					eng.PropIdx().Add(n.ID, k, v)
				}
				eng.Save("write")
				eng.Unlock()
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good -- no deadlock.
	case <-time.After(windowsTimeout(30 * time.Second)):
		t.Fatal("concurrent reads+writes deadlocked")
	}

	// Verify data integrity.
	eng.RLock()
	count := eng.Graph().NodeCount()
	eng.RUnlock()

	// 10 seed + 3 writers * 20 each = 70
	expected := 10 + 3*20
	if count != expected {
		t.Fatalf("expected %d nodes, got %d (data corruption)", expected, count)
	}
}

func TestConcurrentSearchAndCapture(t *testing.T) {
	eng := setupTestEngine(t)

	// Seed data.
	eng.Lock()
	for i := 0; i < 5; i++ {
		n := eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty(fmt.Sprintf("Searchable %d", i)),
			"content_keywords":  graph.StringListProperty([]string{"test", fmt.Sprintf("kw%d", i)}),
			"temporality":       graph.StringProperty("durable"),
			"processing_status": graph.StringProperty("processed"),
			"confidence":        graph.Float64Property(0.9),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
			"access_count":      graph.Int64Property(0),
		})
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
		eng.VecIdx().Add(n.ID, []float32{float32(i) * 0.1, 1.0 - float32(i)*0.1, 0.0})
	}
	eng.Save("seed")
	eng.Unlock()

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Concurrent searches (read lock).
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.RLock()
				// Search by keyword.
				ids := eng.PropIdx().LookupKeyword("content_keywords", "test")
				_ = ids
				// Search by property.
				eng.PropIdx().Lookup("temporality", graph.StringProperty("durable"))
				eng.RUnlock()
			}
		}()
	}

	// Concurrent captures (write lock).
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.Lock()
				n := eng.Graph().AddNode(graph.Properties{
					"content_full":      graph.StringProperty(fmt.Sprintf("Captured %d-%d", id, j)),
					"content_keywords":  graph.StringListProperty([]string{"test", "captured"}),
					"processing_status": graph.StringProperty("captured"),
					"created_at":        graph.TimestampProperty(time.Now().UTC()),
					"access_count":      graph.Int64Property(0),
				})
				for k, v := range n.Properties {
					eng.PropIdx().Add(n.ID, k, v)
				}
				eng.Save("capture")
				eng.Unlock()
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent search+capture deadlocked")
	}
}

func TestHeadHashNotDeadlockUnderLoad(t *testing.T) {
	eng := setupTestEngine(t)

	// Seed.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("seed"),
	})
	eng.Save("seed")
	eng.Unlock()

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// HeadHash uses RLock internally. Read it concurrently with writes.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				h := eng.HeadHash()
				_ = h
			}
		}()
	}

	// HeadHashLocked used under write lock.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.Lock()
				_ = eng.HeadHashLocked()
				eng.Graph().AddNode(graph.Properties{
					"content_full": graph.StringProperty("write"),
				})
				eng.Save("w")
				eng.Unlock()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HeadHash deadlocked under concurrent load")
	}
}

func TestPreChunkConcurrentWithReads(t *testing.T) {
	eng := setupTestEngine(t)

	// Seed.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("existing"),
	})
	eng.Save("seed")
	eng.Unlock()

	// PreChunk runs outside the lock (calls embedder, which is nil
	// in test, so it's fast). Verify it doesn't conflict with reads.
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Readers.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.RLock()
				eng.Graph().AllNodeIDs()
				eng.RUnlock()
			}
		}()
	}

	// PreChunk calls (no lock needed).
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				// PreChunk is safe to call without lock.
				longContent := ""
				for k := 0; k < 600; k++ {
					longContent += "word "
				}
				_ = eng.PreChunk(ctx, longContent, "", "")
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PreChunk deadlocked with concurrent reads")
	}
}

func TestNodeCountEdgeCountNoDeadlock(t *testing.T) {
	eng := setupTestEngine(t)

	eng.Lock()
	n1 := eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("a")})
	n2 := eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("b")})
	eng.Graph().AddEdge(n1.ID, n2.ID, "related_to", 0.5, nil)
	eng.Save("seed")
	eng.Unlock()

	// NodeCount and EdgeCount acquire RLock. Call them concurrently
	// with writes to verify no deadlock.
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				_ = eng.NodeCount()
				_ = eng.EdgeCount()
			}
		}()
	}

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				eng.Lock()
				eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("x")})
				eng.Save("w")
				eng.Unlock()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("NodeCount/EdgeCount deadlocked with concurrent writes")
	}
}
