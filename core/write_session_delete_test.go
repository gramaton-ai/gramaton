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

// TestDeletedUncommittedNodeStaysDeleted pins the lazy-load window:
// between DeleteNode and the commit, the tree still holds the node,
// and the GetNode slow path must NOT resurrect it into the cache (a
// rebuild iterating the pre-commit tree would then re-index it, and
// search would serve a deleted record until restart).
func TestDeletedUncommittedNodeStaysDeleted(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("doomed record"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	// Reopen so the cache is cold and the tree is the only source --
	// the exact shape where the slow path resurrects.
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	eng2 := openReadOnlyTestEngine(t, dir)

	eng2.Lock()
	eng2.Graph().DeleteNode(n.ID)
	// BEFORE the commit: a lookup must miss even though the tree
	// still holds the blob.
	if _, ok := eng2.Graph().GetNode(n.ID); ok {
		eng2.Unlock()
		t.Fatal("deleted-but-uncommitted node resurrected by the lazy load")
	}
	if _, err := eng2.Save("delete"); err != nil {
		eng2.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng2.Unlock()

	if _, ok := eng2.Graph().GetNode(n.ID); ok {
		t.Fatal("node readable after committed deletion")
	}
}
