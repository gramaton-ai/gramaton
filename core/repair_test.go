package core

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestRepairDeletesThroughTheFullIndexSet pins two repair behaviors.
//
// Deletion completeness: the old repair hand-rolled node removal
// (property index, vector, BM25) and left every other surface stale
// -- a deleted orphan chunk stayed in its collection's member cache
// forever, and the post-repair index rebuild did not cover that
// cache. Routing through WriteSession.DeleteNode gives repair the
// same complete deletion set as every other hard delete.
//
// Scan-before-delete: the old repair removed dangling edges BEFORE
// scanning for orphan chunks, and a chunk_of edge to a missing
// parent is definitionally dangling -- the first pass destroyed the
// evidence, so the orphan itself was never detected or removed.
// Both scans now complete before the first deletion.
func TestRepairDeletesThroughTheFullIndexSet(t *testing.T) {
	eng := setupTestEngine(t)
	g := eng.Graph()

	eng.Lock()
	orphan := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("orphaned chunk body"),
	})
	coll := g.AddNode(graph.Properties{
		"field.name": graph.StringProperty("holding collection"),
	})
	live := g.AddNode(graph.Properties{
		"content_full": graph.StringProperty("live record"),
	})
	if _, err := g.AddEdge(orphan.ID, coll.ID, "member_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("member_of: %v", err)
	}
	eng.CollCache().AddMember(coll.ID, orphan.ID)
	// Manufacture the corruption directly in the edge store -- the
	// graph API validates endpoints, which is why these shapes only
	// arise from historical bugs and crashes.
	eng.EdgeStore().Put(&graph.Edge{
		ID: "edge-orphan-chunkof", SourceID: orphan.ID,
		TargetID: "01MISSINGPARENT0000000000",
		Type:     "chunk_of", Weight: 1.0,
	})
	eng.EdgeStore().Put(&graph.Edge{
		ID: "edge-dangling-rel", SourceID: live.ID,
		TargetID: "01MISSINGTARGET0000000000",
		Type:     "related_to", Weight: 0.5,
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("seed save: %v", err)
	}
	eng.Unlock()

	r := eng.Repair()
	if r.OrphanChunksRemoved != 1 {
		t.Fatalf("OrphanChunksRemoved = %d, want 1 (%v)", r.OrphanChunksRemoved, r.Messages)
	}
	if r.DanglingEdgesRemoved != 1 {
		t.Fatalf("DanglingEdgesRemoved = %d, want 1 -- the related_to edge; the chunk_of cascades with its orphan (%v)", r.DanglingEdgesRemoved, r.Messages)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := g.GetNode(orphan.ID); ok {
		t.Error("orphan chunk survived repair")
	}
	for _, m := range eng.CollCache().Members(coll.ID) {
		if m == orphan.ID {
			t.Error("deleted orphan still listed in the collection member cache")
		}
	}
	for _, e := range g.EdgesFrom(live.ID) {
		if e.ID == "edge-dangling-rel" {
			t.Error("dangling edge survived repair")
		}
	}
	if _, ok := g.GetNode(live.ID); !ok {
		t.Error("live record must survive repair")
	}

	commit, err := graph.LoadCommitMeta(eng.Store(), eng.HeadHash())
	if err != nil {
		t.Fatalf("load HEAD commit: %v", err)
	}
	hasRepairAction := false
	for _, a := range commit.Actions {
		if a.Kind == graph.ActionRepair {
			hasRepairAction = true
		}
	}
	if !hasRepairAction {
		t.Error("repair commit missing the structured repair action")
	}
}
