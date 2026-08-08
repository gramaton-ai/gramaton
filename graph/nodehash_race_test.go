package graph

import (
	"fmt"
	"sync"
	"testing"
)

// TestNodeHashOfRacesLazyLoad pins NodeHashOf's cacheMu discipline.
// GetNode's lazy-load path writes g.nodeHashes while holding only
// the engine READ lock, so a concurrent NodeHashOf from another
// reader touches the same map -- an unsynchronized read the runtime
// aborts the whole process for. Run under -race: the pre-fix
// lock-free NodeHashOf fails here.
func TestNodeHashOfRacesLazyLoad(t *testing.T) {
	s := tempStorage(t)
	g := New()
	ids := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		n := g.AddNode(Properties{
			"content": StringProperty(fmt.Sprintf("record %d", i)),
		})
		ids = append(ids, n.ID)
	}
	commit, err := g.Save(s, "", "seed")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Fresh lazy graph: every GetNode is a cold load that writes
	// nodeHashes.
	g2 := New()
	if _, err := g2.Load(s, commit.Hash); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, id := range ids {
			g2.GetNode(id)
		}
	}()
	go func() {
		defer wg.Done()
		for _, id := range ids {
			g2.NodeHashOf(id)
		}
	}()
	wg.Wait()
}
