package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// buildCarveAddSource builds a source with a deterministic graph shaped
// for the top-up tests:
//
//	a1 --member_of--> coll-1
//	a2 --member_of--> coll-1
//	b1 --member_of--> coll-1     (b1 is the "missed" record)
//	b1 --related_to--> a1         (edge to an already-carved record)
//	b1 --related_to--> out-1      (edge to a record that stays outside)
//
// A first carve of {a1, a2} produces {a1, a2, coll-1}; adding {b1}
// reconnects b1->a1 (interior, a1 already present) and b1->coll-1, and
// drops b1->out-1 (out-1 never enters the destination).
func buildCarveAddSource(t *testing.T) *API {
	t.Helper()
	a, _ := newCarveStore(t, "openai", "", func(ws *core.WriteSession) {
		g := ws.Graph()
		add := func(id, content string, seed float32) {
			g.AddNodeWithID(id, carveMemProps(content))
			ws.IndexNode(id, content, carveVec(seed))
		}
		add("a1", "record a1", 0.9)
		add("a2", "record a2", 0.8)
		add("b1", "record b1 (missed)", 0.7)
		add("out-1", "outside record", 0.5)

		g.AddNodeWithID("coll-1", carveCollProps("Add Collection"))
		ws.IndexNode("coll-1", "Add Collection", nil)

		mustEdge := func(src, dst, typ string, w float64) {
			if _, err := ws.AddEdge(src, dst, typ, w, nil); err != nil {
				t.Fatalf("AddEdge %s->%s (%s): %v", src, dst, typ, err)
			}
		}
		mustEdge("a1", "coll-1", "member_of", 1.0)
		mustEdge("a2", "coll-1", "member_of", 1.0)
		mustEdge("b1", "coll-1", "member_of", 1.0)
		mustEdge("b1", "a1", "related_to", 0.5)
		mustEdge("b1", "out-1", "related_to", 0.5)
	})
	return a
}

// buildCarveAddDimSource builds a source holding a dim-4 record and a
// genuinely dim-8 record (embedding_full set directly, bypassing the dim-4
// vec index). It backs the dimension-mismatch and failure-restore tests: a
// destination carved from rec-4 has configured dimension 4, and adding
// rec-8 must be refused.
func buildCarveAddDimSource(t *testing.T) *API {
	t.Helper()
	a, _ := newCarveStore(t, "openai", "", func(ws *core.WriteSession) {
		g := ws.Graph()
		g.AddNodeWithID("rec-4", carveMemProps("dim four record"))
		ws.IndexNode("rec-4", "dim four record", carveVec(0.9))

		p8 := carveMemProps("dim eight record")
		p8["embedding_full"] = graph.VectorProperty([]float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8})
		g.AddNodeWithID("rec-8", p8)
		ws.IndexNode("rec-8", "dim eight record", nil)
	})
	return a
}

// destCounts opens a destination store, reads its node + edge counts, and
// closes it immediately (so it never holds the bbolt lock across a
// subsequent open). Use instead of openStore when the same store is opened
// more than once in a test.
func destCounts(t *testing.T, home string) (nodes, edges int) {
	t.Helper()
	eng, err := core.LoadEngine(home)
	if err != nil {
		t.Fatalf("open dest %s: %v", home, err)
	}
	defer eng.Close()
	eng.RLock()
	nodes = eng.Graph().NodeCount()
	edges = eng.Graph().EdgeCount()
	eng.RUnlock()
	return nodes, edges
}

// TestCarveAddMissedRecords is the headline top-up: carve a subset, then
// add a record NOT in the first carve whose edges reach an already-carved
// record. Its interior edge reconnects; its edge to a still-outside record
// is dropped and reported.
func TestCarveAddMissedRecords(t *testing.T) {
	a := buildCarveAddSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "topup")
	destData := filepath.Join(destHome, "data")

	createResp, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs:         []string{"a1", "a2"},
		DestName:    "topup",
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	if createResp.NodeCount != 3 { // a1, a2, coll-1
		t.Fatalf("create NodeCount = %d, want 3", createResp.NodeCount)
	}

	addResp, apiErr := a.CarveAdd(ctx, CarveAddRequest{
		IDs:         []string{"b1"},
		DestName:    "topup",
		DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveAdd: %v", apiErr)
	}

	if addResp.NodesAdded != 1 {
		t.Errorf("NodesAdded = %d, want 1 (b1)", addResp.NodesAdded)
	}
	if addResp.NodesSkippedPresent != 1 {
		t.Errorf("NodesSkippedPresent = %d, want 1 (coll-1)", addResp.NodesSkippedPresent)
	}
	// b1->coll-1 (member_of) and b1->a1 (related_to, a1 pre-existing).
	if addResp.EdgesAdded != 2 {
		t.Errorf("EdgesAdded = %d, want 2 (b1->coll-1, b1->a1)", addResp.EdgesAdded)
	}
	if addResp.DroppedTotal != 1 || addResp.DroppedByType["related_to"] != 1 {
		t.Errorf("dropped = %d %v, want 1 {related_to:1}", addResp.DroppedTotal, addResp.DroppedByType)
	}
	if !containsDangling(addResp.DanglingSample, "b1", "out-1", "related_to") {
		t.Errorf("DanglingSample = %v, want it to contain {b1, out-1, related_to}", addResp.DanglingSample)
	}
	if addResp.Thawed {
		t.Error("an unfrozen target must not report thawed_and_refrozen")
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "b1") {
		t.Error("dest missing the added node b1")
	}
	if nodeExists(t, dest, "out-1") {
		t.Error("dest must NOT contain the still-outside out-1")
	}
	if !hasEdge(t, dest, "b1", "a1", "related_to") {
		t.Error("dest missing the reconnected edge b1 -> a1 (already-present record)")
	}
	if !hasEdge(t, dest, "b1", "coll-1", "member_of") {
		t.Error("dest missing edge b1 -> coll-1")
	}
	if !hasEdge(t, dest, "a1", "coll-1", "member_of") {
		t.Error("dest lost the pre-existing edge a1 -> coll-1")
	}
	if hasEdge(t, dest, "b1", "out-1", "related_to") {
		t.Error("dest should not carry the dropped edge b1 -> out-1")
	}
	dest.RLock()
	b1n, _ := dest.Graph().GetNode("b1")
	dest.RUnlock()
	if got, _ := b1n.Properties.GetString("origin_store"); got != "source-store" {
		t.Errorf("b1 origin_store = %q, want source-store", got)
	}
}

// TestCarveAddIdempotent proves a double-run is a no-op: the second add
// skips every node and every edge and leaves the destination's counts
// unchanged.
func TestCarveAddIdempotent(t *testing.T) {
	a := buildCarveAddSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "idem")
	destData := filepath.Join(destHome, "data")

	if _, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs: []string{"a1", "a2"}, DestName: "idem", DestDataDir: destData,
	}); apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	addReq := CarveAddRequest{IDs: []string{"b1"}, DestName: "idem", DestDataDir: destData}

	first, apiErr := a.CarveAdd(ctx, addReq)
	if apiErr != nil {
		t.Fatalf("first CarveAdd: %v", apiErr)
	}
	if first.NodesAdded != 1 || first.EdgesAdded != 2 {
		t.Fatalf("first add: NodesAdded=%d EdgesAdded=%d, want 1/2", first.NodesAdded, first.EdgesAdded)
	}
	nodesAfterFirst, edgesAfterFirst := destCounts(t, destHome)

	second, apiErr := a.CarveAdd(ctx, addReq)
	if apiErr != nil {
		t.Fatalf("second CarveAdd: %v", apiErr)
	}
	if second.NodesAdded != 0 {
		t.Errorf("second run NodesAdded = %d, want 0 (all nodes skip-present)", second.NodesAdded)
	}
	if second.EdgesAdded != 0 {
		t.Errorf("second run EdgesAdded = %d, want 0 (all edges deduped)", second.EdgesAdded)
	}
	if second.NodesSkippedPresent != 2 {
		t.Errorf("second run NodesSkippedPresent = %d, want 2 (b1, coll-1)", second.NodesSkippedPresent)
	}
	if second.EdgesSkippedPresent != 2 {
		t.Errorf("second run EdgesSkippedPresent = %d, want 2 (b1->coll-1, b1->a1)", second.EdgesSkippedPresent)
	}

	nodesAfterSecond, edgesAfterSecond := destCounts(t, destHome)
	if nodesAfterSecond != nodesAfterFirst {
		t.Errorf("node count changed on re-run: %d -> %d", nodesAfterFirst, nodesAfterSecond)
	}
	if edgesAfterSecond != edgesAfterFirst {
		t.Errorf("edge count changed on re-run: %d -> %d", edgesAfterFirst, edgesAfterSecond)
	}
}

// TestCarveAddFrozenTarget adds into a FROZEN destination: it must be
// thawed for the add and re-frozen to its exact prior manifest, with the
// new record present afterward.
func TestCarveAddFrozenTarget(t *testing.T) {
	a := buildCarveAddSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "frozen")
	destData := filepath.Join(destHome, "data")

	if _, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs: []string{"a1", "a2"}, DestName: "frozen", DestDataDir: destData, ReadOnly: true,
	}); apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}

	before, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}
	if !before.ReadOnly {
		t.Fatal("dest should be frozen before the add")
	}

	addResp, apiErr := a.CarveAdd(ctx, CarveAddRequest{
		IDs: []string{"b1"}, DestName: "frozen", DestDataDir: destData,
	})
	if apiErr != nil {
		t.Fatalf("CarveAdd into frozen target: %v", apiErr)
	}
	if !addResp.Thawed {
		t.Error("a frozen target should report thawed_and_refrozen")
	}
	if addResp.NodesAdded != 1 {
		t.Errorf("NodesAdded = %d, want 1", addResp.NodesAdded)
	}

	after, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest after: %v", err)
	}
	if !after.ReadOnly {
		t.Error("dest manifest should be frozen again after the add (thaw->add->refreeze)")
	}
	if after.Owner != before.Owner {
		t.Errorf("owner not preserved: before %q, after %q", before.Owner, after.Owner)
	}
	if !after.PublishedAt.Equal(before.PublishedAt) {
		t.Errorf("published_at not preserved: before %v, after %v", before.PublishedAt, after.PublishedAt)
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "b1") {
		t.Error("dest missing the added node b1 after add into a frozen target")
	}
	if !dest.ReadOnly() {
		t.Error("reopened dest engine should report read-only")
	}
}

// TestCarveAddDimensionMismatch refuses records whose embedding dimension
// differs from the destination's configured dimension, leaving the
// (unfrozen) destination unchanged.
func TestCarveAddDimensionMismatch(t *testing.T) {
	a := buildCarveAddDimSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "dim")
	destData := filepath.Join(destHome, "data")

	if _, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs: []string{"rec-4"}, DestName: "dim", DestDataDir: destData,
	}); apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	nodesBefore, edgesBefore := destCounts(t, destHome)

	_, apiErr := a.CarveAdd(ctx, CarveAddRequest{
		IDs: []string{"rec-8"}, DestName: "dim", DestDataDir: destData,
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for dimension mismatch, got %v", apiErr)
	}

	nodesAfter, edgesAfter := destCounts(t, destHome)
	if nodesAfter != nodesBefore || edgesAfter != edgesBefore {
		t.Errorf("dest changed after refused add: nodes %d->%d edges %d->%d",
			nodesBefore, nodesAfter, edgesBefore, edgesAfter)
	}
	dest := openStore(t, destHome)
	if nodeExists(t, dest, "rec-8") {
		t.Error("dest must not contain rec-8 after a refused dimension-mismatch add")
	}
}

// TestCarveAddDryRun previews an add against a FROZEN destination: it
// reports what would be added, writes nothing, and leaves the destination
// frozen and byte-for-byte unchanged.
func TestCarveAddDryRun(t *testing.T) {
	a := buildCarveAddSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "dry")
	destData := filepath.Join(destHome, "data")

	if _, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs: []string{"a1", "a2"}, DestName: "dry", DestDataDir: destData, ReadOnly: true,
	}); apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	nodesBefore, edgesBefore := destCounts(t, destHome)
	manifestBefore, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}

	resp, apiErr := a.CarveAdd(ctx, CarveAddRequest{
		IDs: []string{"b1"}, DestName: "dry", DestDataDir: destData, DryRun: true,
	})
	if apiErr != nil {
		t.Fatalf("CarveAdd dry-run: %v", apiErr)
	}
	if !resp.DryRun {
		t.Error("resp.DryRun should be true")
	}
	if resp.NodesAdded != 1 || resp.NodesSkippedPresent != 1 {
		t.Errorf("dry-run node counts = %d added / %d skipped, want 1/1", resp.NodesAdded, resp.NodesSkippedPresent)
	}
	if resp.EdgesAdded != 2 {
		t.Errorf("dry-run EdgesAdded = %d, want 2", resp.EdgesAdded)
	}
	if resp.DroppedTotal != 1 {
		t.Errorf("dry-run DroppedTotal = %d, want 1", resp.DroppedTotal)
	}
	if resp.Thawed {
		t.Error("a dry run must never thaw")
	}

	nodesAfter, edgesAfter := destCounts(t, destHome)
	if nodesAfter != nodesBefore || edgesAfter != edgesBefore {
		t.Errorf("dry-run changed dest: nodes %d->%d edges %d->%d",
			nodesBefore, nodesAfter, edgesBefore, edgesAfter)
	}
	manifestAfter, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest after: %v", err)
	}
	if !manifestAfter.ReadOnly {
		t.Error("a frozen dest must stay frozen after a dry-run")
	}
	if !manifestAfter.PublishedAt.Equal(manifestBefore.PublishedAt) {
		t.Error("dry-run must not rewrite the STORE manifest")
	}
	dest := openStore(t, destHome)
	if nodeExists(t, dest, "b1") {
		t.Error("dry-run must not add b1 to the dest")
	}
}

// TestCarveAddFailureLeavesDestIntact forces a materialize failure (a
// dimension mismatch) against a FROZEN destination and asserts the
// destination keeps its original records and its exact frozen state.
func TestCarveAddFailureLeavesDestIntact(t *testing.T) {
	a := buildCarveAddDimSource(t)
	ctx := context.Background()

	destHome := filepath.Join(t.TempDir(), "fail")
	destData := filepath.Join(destHome, "data")

	if _, apiErr := a.CarveOut(ctx, CarveOutRequest{
		IDs: []string{"rec-4"}, DestName: "fail", DestDataDir: destData, ReadOnly: true,
	}); apiErr != nil {
		t.Fatalf("CarveOut: %v", apiErr)
	}
	before, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}
	if !before.ReadOnly {
		t.Fatal("dest should be frozen before the failing add")
	}
	nodesBefore, edgesBefore := destCounts(t, destHome)

	// The dim-8 add fails AFTER the thaw, exercising the freeze-restore path.
	_, apiErr := a.CarveAdd(ctx, CarveAddRequest{
		IDs: []string{"rec-8"}, DestName: "fail", DestDataDir: destData,
	})
	if apiErr == nil {
		t.Fatal("expected the dimension-mismatch add to fail")
	}

	nodesAfter, edgesAfter := destCounts(t, destHome)
	if nodesAfter != nodesBefore || edgesAfter != edgesBefore {
		t.Errorf("failed add changed dest: nodes %d->%d edges %d->%d",
			nodesBefore, nodesAfter, edgesBefore, edgesAfter)
	}

	after, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("ReadStoreManifest after: %v", err)
	}
	if !after.ReadOnly {
		t.Error("dest must be re-frozen after a failed add (freeze state restored)")
	}
	if after.Owner != before.Owner || !after.PublishedAt.Equal(before.PublishedAt) {
		t.Errorf("freeze provenance not restored exactly: before {%q %v}, after {%q %v}",
			before.Owner, before.PublishedAt, after.Owner, after.PublishedAt)
	}

	dest := openStore(t, destHome)
	if !nodeExists(t, dest, "rec-4") {
		t.Error("dest lost its original record rec-4 after a failed add")
	}
	if nodeExists(t, dest, "rec-8") {
		t.Error("dest gained rec-8 despite the add failing")
	}
}

// TestCarveAddRequiresSeed rejects a top-up with no seeds.
func TestCarveAddRequiresSeed(t *testing.T) {
	a := buildCarveAddSource(t)
	_, apiErr := a.CarveAdd(context.Background(), CarveAddRequest{
		DestDataDir: filepath.Join(t.TempDir(), "x", "data"),
	})
	if apiErr == nil || apiErr.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %v", apiErr)
	}
}

// TestCarveAddMissingDestination rejects a top-up whose destination store
// does not exist.
func TestCarveAddMissingDestination(t *testing.T) {
	a := buildCarveAddSource(t)
	_, apiErr := a.CarveAdd(context.Background(), CarveAddRequest{
		IDs:         []string{"b1"},
		DestDataDir: filepath.Join(t.TempDir(), "nope", "data"),
	})
	if apiErr == nil || apiErr.Code != "not_found" {
		t.Fatalf("expected not_found for a missing destination, got %v", apiErr)
	}
}
