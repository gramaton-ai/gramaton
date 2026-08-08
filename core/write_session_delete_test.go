package core

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// seedDeleteFixture creates two connected nodes with fully populated
// indexes and returns their IDs plus the connecting edge's ID.
func seedDeleteFixture(t *testing.T, eng *Engine) (victimID, peerID, edgeID string) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	victim := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("the victim record slated for batched deletion"),
		"temporality":  graph.StringProperty("durable"),
	})
	eng.IndexNode(victim.ID, "the victim record slated for batched deletion", []float32{1, 0, 0, 0})

	peer := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("the surviving peer record"),
	})
	eng.IndexNode(peer.ID, "the surviving peer record", nil)

	out, err := eng.Graph().AddEdge(victim.ID, peer.ID, "related_to", 0.8, nil)
	if err != nil {
		t.Fatalf("AddEdge out: %v", err)
	}
	if _, err := eng.Graph().AddEdge(peer.ID, victim.ID, "elaborates", 0.7, nil); err != nil {
		t.Fatalf("AddEdge in: %v", err)
	}
	if _, err := eng.Save("seed delete fixture"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return victim.ID, peer.ID, out.ID
}

// TestWriteSessionDeleteNode pins the batched hard-delete: inside
// WithWriteBatch's shared bbolt transaction the deletion must
// complete (the non-Tx DeleteNode cascade opens its own bbolt Update
// per edge and self-deadlocks there), remove every index entry, and
// cascade both edge directions while leaving the peer intact.
func TestWriteSessionDeleteNode(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)
	victimID, peerID, _ := seedDeleteFixture(t, eng)

	err := eng.WithWriteBatch("collapse test", func(ws *WriteSession) (bool, error) {
		if err := ws.DeleteNode(victimID); err != nil {
			return false, err
		}
		ws.AddAction(graph.CommitAction{Kind: graph.ActionBackfill, Field: "collapse_test"})
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch delete: %v", err)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(victimID); ok {
		t.Fatal("victim node survived DeleteNode")
	}
	if _, ok := eng.Graph().GetNode(peerID); !ok {
		t.Fatal("peer node was deleted; cascade must stop at edges")
	}
	if ids := eng.PropIdx().Lookup("temporality", graph.StringProperty("durable")); len(ids) != 0 {
		t.Fatalf("property index still lists the victim: %v", ids)
	}
	if hits := eng.BM25Full().Search([]string{"victim"}, 10, nil); len(hits) != 0 {
		t.Fatalf("BM25 still lists the victim: %v", hits)
	}
	if res := eng.VecIdx().Search([]float32{1, 0, 0, 0}, 5, nil); len(res) != 0 && res[0].NodeID == victimID {
		t.Fatal("vector index still lists the victim")
	}
	for _, e := range eng.Graph().EdgesFrom(peerID) {
		if e.TargetID == victimID {
			t.Fatal("inbound edge to the victim survived the cascade")
		}
	}
	if es := eng.Graph().EdgesTo(peerID); len(es) != 0 {
		t.Fatalf("victim's outbound edge survived the cascade: %v", es)
	}
}

// TestWriteSessionDeleteEdge pins the batched single-edge removal:
// the edge disappears, both endpoints survive.
func TestWriteSessionDeleteEdge(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)
	victimID, peerID, edgeID := seedDeleteFixture(t, eng)

	err := eng.WithWriteBatch("edge delete test", func(ws *WriteSession) (bool, error) {
		return true, ws.DeleteEdge(edgeID)
	})
	if err != nil {
		t.Fatalf("WithWriteBatch delete edge: %v", err)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetEdge(edgeID); ok {
		t.Fatal("edge survived DeleteEdge")
	}
	for _, id := range []string{victimID, peerID} {
		if _, ok := eng.Graph().GetNode(id); !ok {
			t.Fatalf("node %s deleted by an edge removal", id)
		}
	}
	// The reverse edge is untouched.
	if es := eng.Graph().EdgesFrom(peerID); len(es) != 1 {
		t.Fatalf("reverse edge count = %d, want 1", len(es))
	}
}
