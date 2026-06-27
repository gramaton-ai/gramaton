package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// edgeReq builds a SaveBatchRequest with N items each carrying its
// own ClientRef ("ref-0", "ref-1", ...) plus the supplied edges.
func edgeReq(itemContents []string, edges []EdgeSpec) SaveBatchRequest {
	items := make([]SaveBatchItem, len(itemContents))
	for i, c := range itemContents {
		items[i] = SaveBatchItem{
			ClientRef:   refName(i),
			SaveRequest: SaveRequest{Content: c},
		}
	}
	return SaveBatchRequest{Items: items, Edges: edges}
}

func refName(i int) string {
	switch i {
	case 0:
		return "ref-0"
	case 1:
		return "ref-1"
	case 2:
		return "ref-2"
	case 3:
		return "ref-3"
	case 4:
		return "ref-4"
	default:
		return "ref-" + itoa(i)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	if i < 100 {
		return string(rune('0'+i/10)) + string(rune('0'+i%10))
	}
	return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}

// TestSaveBatchEdgesIntraBatch: 3 items + 2 ClientRef edges, all
// commit. Edges round-trip with their assigned IDs.
func TestSaveBatchEdgesIntraBatch(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"alpha", "beta", "gamma"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
			{SourceClientRef: "ref-1", TargetClientRef: "ref-2", Type: "supports"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 2 {
		t.Fatalf("edges: got %d want 2", len(resp.Edges))
	}
	if len(resp.EdgesFailed) != 0 {
		t.Errorf("unexpected edge failures: %+v", resp.EdgesFailed)
	}
	if resp.Stats.EdgesAdded != 2 || resp.Stats.EdgesFailed != 0 {
		t.Errorf("stats: %+v", resp.Stats)
	}
	for _, e := range resp.Edges {
		if e.EdgeID == "" {
			t.Errorf("missing edge_id at index %d", e.Index)
		}
	}
}

// TestSaveBatchEdgeToExistingNode: edge.TargetID points at a
// pre-existing record; edge created.
func TestSaveBatchEdgeToExistingNode(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	existing := addRecord(t, eng, "existing target record")
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"new item"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetID: existing, Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: got %d want 1", len(resp.Edges))
	}
	if resp.Edges[0].TargetID != existing {
		t.Errorf("target_id: got %s want %s", resp.Edges[0].TargetID, existing)
	}
}

// TestSaveBatchEdgeForwardRef: item 0's edge points at item 2 by
// ClientRef. Resolution doesn't depend on item commit order — items
// commit in batch order, then edges resolve against the full map.
func TestSaveBatchEdgeForwardRef(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b", "c"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-2", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: got %d want 1", len(resp.Edges))
	}
	src := resp.Added[0].ID
	tgt := resp.Added[2].ID
	if resp.Edges[0].SourceID != src || resp.Edges[0].TargetID != tgt {
		t.Errorf("forward ref didn't resolve: got src=%s tgt=%s want src=%s tgt=%s",
			resp.Edges[0].SourceID, resp.Edges[0].TargetID, src, tgt)
	}
}

// TestSaveBatchEdgeBackwardRef: item 2's edge points at item 0 by
// ClientRef.
func TestSaveBatchEdgeBackwardRef(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b", "c"},
		[]EdgeSpec{
			{SourceClientRef: "ref-2", TargetClientRef: "ref-0", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: got %d want 1", len(resp.Edges))
	}
}

// TestSaveBatchEdgeTargetItemFailed: edge target points at an item
// whose validation failed -> target_item_failed.
func TestSaveBatchEdgeTargetItemFailed(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	bad := 2.0
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{ClientRef: "ref-0", SaveRequest: SaveRequest{Content: "valid"}},
			{ClientRef: "ref-broken", SaveRequest: SaveRequest{Content: "bad", Confidence: &bad}},
		},
		Edges: []EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-broken", Type: "related_to"},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 0 {
		t.Errorf("expected 0 edges committed, got %d", len(resp.Edges))
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "target_item_failed" {
		t.Errorf("expected target_item_failed, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeSourceItemFailed: edge source points at a failed
// item -> source_item_failed.
func TestSaveBatchEdgeSourceItemFailed(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	bad := 2.0
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{ClientRef: "ref-broken", SaveRequest: SaveRequest{Content: "bad", Confidence: &bad}},
			{ClientRef: "ref-good", SaveRequest: SaveRequest{Content: "good"}},
		},
		Edges: []EdgeSpec{
			{SourceClientRef: "ref-broken", TargetClientRef: "ref-good", Type: "related_to"},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "source_item_failed" {
		t.Errorf("expected source_item_failed, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeTargetIDNotFound: edge.TargetID references a
// record that doesn't exist anywhere.
func TestSaveBatchEdgeTargetIDNotFound(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetID: "01HQQQQQQQQQQQQQQQQQQQQQQQ", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "target_id_not_found" {
		t.Errorf("expected target_id_not_found, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeSourceIDNotFound: edge.SourceID references a
// record that doesn't exist.
func TestSaveBatchEdgeSourceIDNotFound(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceID: "01HQQQQQQQQQQQQQQQQQQQQQQQ", TargetClientRef: "ref-0", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "source_id_not_found" {
		t.Errorf("expected source_id_not_found, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeRefNotFound: a ClientRef that doesn't match any
// item also surfaces *_id_not_found.
func TestSaveBatchEdgeRefNotFound(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-nonexistent", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "target_id_not_found" {
		t.Errorf("expected target_id_not_found for missing ref, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeInvalidWeight: weight outside [0,1] is rejected
// per edge with invalid_weight.
func TestSaveBatchEdgeInvalidWeight(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	bad := 2.0
	neg := -0.5
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to", Weight: &bad},
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "supports", Weight: &neg},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 2 {
		t.Fatalf("expected 2 invalid_weight failures, got %d: %+v", len(resp.EdgesFailed), resp.EdgesFailed)
	}
	for _, ef := range resp.EdgesFailed {
		if ef.Code != "invalid_weight" {
			t.Errorf("expected invalid_weight, got %q", ef.Code)
		}
	}
}

// TestSaveBatchEdgeWeightZeroExplicit: an explicit zero weight is a
// valid edge weight (pinning the 0=valid policy; 0 is a meaningful
// signal, not a placeholder).
func TestSaveBatchEdgeWeightZeroExplicit(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	zero := 0.0
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to", Weight: &zero},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(resp.Edges))
	}
	if resp.Edges[0].Weight != 0.0 {
		t.Errorf("expected weight=0, got %v", resp.Edges[0].Weight)
	}
}

// TestSaveBatchEdgeSelfLoop: source and target resolve to same ID.
func TestSaveBatchEdgeSelfLoop(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-0", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "self_loop" {
		t.Errorf("expected self_loop, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeDuplicate: same (source, target, type) tuple
// twice -> second fails with duplicate_edge.
func TestSaveBatchEdgeDuplicate(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(resp.Edges))
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "duplicate_edge" {
		t.Errorf("expected duplicate_edge, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeOverMultiplier: len(Edges) > 10 * len(Items)
// -> envelope ErrInvalid.
func TestSaveBatchEdgeOverMultiplier(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	items := []string{"a"}
	edges := make([]EdgeSpec, MaxBatchEdgeMultiplier+1)
	for i := range edges {
		edges[i] = EdgeSpec{SourceClientRef: "ref-0", TargetClientRef: "ref-0", Type: "related_to"}
	}
	_, apiErr := a.SaveBatch(context.Background(), edgeReq(items, edges))
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
	if !strings.Contains(apiErr.Message, "edges exceeds") {
		t.Errorf("expected edges-cap message, got %q", apiErr.Message)
	}
}

// TestSaveBatchEdgeInvalidType: empty edge type -> invalid_type.
func TestSaveBatchEdgeInvalidType(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: ""},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "invalid_type" {
		t.Errorf("expected invalid_type, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeMissingEndpoint: neither id nor ref supplied
// for an endpoint -> missing_endpoint.
func TestSaveBatchEdgeMissingEndpoint(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", Type: "related_to"}, // no target
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "missing_endpoint" {
		t.Errorf("expected missing_endpoint, got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchEdgeBothEndpointsSet: id and ref both set on the
// same endpoint -> missing_endpoint (mutually exclusive).
func TestSaveBatchEdgeBothEndpointsSet(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	existing := addRecord(t, eng, "existing")
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetID: existing, TargetClientRef: "ref-0", Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.EdgesFailed) != 1 || resp.EdgesFailed[0].Code != "missing_endpoint" {
		t.Errorf("expected missing_endpoint (mutually exclusive), got %+v", resp.EdgesFailed)
	}
}

// TestSaveBatchMixedRefs: edge with SourceClientRef + TargetID;
// both resolution paths exercised in one edge.
func TestSaveBatchMixedRefs(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	existing := addRecord(t, eng, "existing target")
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"new"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetID: existing, Type: "related_to"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(resp.Edges))
	}
	if resp.Edges[0].TargetID != existing {
		t.Errorf("target: got %s want %s", resp.Edges[0].TargetID, existing)
	}
	if resp.Edges[0].SourceID != resp.Added[0].ID {
		t.Errorf("source: got %s want %s", resp.Edges[0].SourceID, resp.Added[0].ID)
	}
}

// TestSaveBatchEdgesRollbackOnSaveFailure: edges added in-memory
// during Phase 3 must be removed on Save failure rollback.
func TestSaveBatchEdgesRollbackOnSaveFailure(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseChunkSave: errors.New("forced"),
	}})
	defer a.SetFaultInjector(nil)
	_, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
		},
	))
	if apiErr == nil {
		t.Fatal("expected save failure")
	}
	eng.RLock()
	defer eng.RUnlock()
	if got := len(eng.Graph().AllNodeIDs()); got != 0 {
		t.Errorf("expected 0 nodes after rollback, got %d", got)
	}
}

// TestSaveBatchEdgesCommitActions: each successful edge contributes
// an ActionLink CommitAction; combined with N item ActionSave
// entries the total = items + edges.
func TestSaveBatchEdgesCommitActions(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), edgeReq(
		[]string{"a", "b", "c"},
		[]EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
			{SourceClientRef: "ref-1", TargetClientRef: "ref-2", Type: "supports"},
		},
	))
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	_ = resp
	eng.RLock()
	defer eng.RUnlock()
	commit, err := loadCommitMeta(eng.Store(), eng.HeadHashLocked())
	if err != nil {
		t.Fatalf("loadCommitMeta: %v", err)
	}
	// 3 ActionSave + 2 ActionLink = 5 total
	if len(commit.Actions) != 5 {
		t.Errorf("expected 5 actions (3 capture + 2 link), got %d", len(commit.Actions))
	}
}

// TestCanonicalizeRequestEdgesAffectHash: adding an edge changes the
// canonical bytes so two superficially-similar requests don't collide
// for ClientToken idempotency.
func TestCanonicalizeRequestEdgesAffectHash(t *testing.T) {
	a, _ := canonicalizeRequest(edgeReq([]string{"x", "y"}, nil))
	b, _ := canonicalizeRequest(edgeReq([]string{"x", "y"}, []EdgeSpec{
		{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
	}))
	if string(a) == string(b) {
		t.Errorf("edges should change canonical bytes\n  a=%s\n  b=%s", a, b)
	}
}
