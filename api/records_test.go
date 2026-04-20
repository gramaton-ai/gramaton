package api

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// addRecord is the api-package analog of server.addRecord: seeds a
// Memory record directly through the engine for test setup.
func addRecord(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.9),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

// --- Inspect ---

// TestInspectRelatedHasEdgeID verifies the Bug-2 fix: inspect results
// include edge_id on every related entry.
func TestInspectRelatedHasEdgeID(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	id1 := addRecord(t, eng, "record one")
	id2 := addRecord(t, eng, "record two")

	eng.Lock()
	if _, err := eng.Graph().AddEdge(id1, id2, "related_to", 0.8, nil); err != nil {
		eng.Unlock()
		t.Fatalf("add edge: %v", err)
	}
	if _, err := eng.Save("test-edge"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	resp, apiErr := a.Inspect(ctx, InspectRequest{ID: id1})
	if apiErr != nil {
		t.Fatalf("Inspect: %v", apiErr)
	}
	if len(resp.Related) == 0 {
		t.Fatal("expected related entries")
	}
	for i, rel := range resp.Related {
		if rel.EdgeID == "" {
			t.Errorf("related[%d] missing edge_id", i)
		}
	}
}

func TestInspectNotFound(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	_, apiErr := a.Inspect(ctx, InspectRequest{ID: "nonexistent"})
	if apiErr == nil {
		t.Fatal("expected error for nonexistent record")
	}
	if apiErr.Code != "not_found" {
		t.Fatalf("expected not_found, got %s", apiErr.Code)
	}
}

// --- Update ---

func TestUpdate(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "updatable record")

	conf := 0.5
	resp, apiErr := a.Update(ctx, UpdateRequest{
		ID:          id,
		Confidence:  &conf,
		Temporality: "temporal",
	})
	if apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}
	if !resp.Updated {
		t.Error("expected Updated=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if c, ok := n.Properties.GetFloat64("confidence"); !ok || c != 0.5 {
		t.Errorf("expected confidence=0.5, got %v", c)
	}
}

func TestUpdateClearValidUntil(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "record with valid_until")

	eng.Lock()
	eng.SetProp(id, "valid_until", graph.TimestampProperty(time.Now().UTC()))
	eng.SetProp(id, "resolution", graph.StringProperty("completed"))
	eng.SetProp(id, "resolved_at", graph.TimestampProperty(time.Now().UTC()))
	if _, err := eng.Save("test"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	resp, apiErr := a.Update(ctx, UpdateRequest{ID: id, ValidUntil: "clear"})
	if apiErr != nil {
		t.Fatalf("Update clear: %v", apiErr)
	}
	if !resp.Updated {
		t.Error("expected Updated=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetTimestamp("valid_until"); ok {
		t.Error("expected valid_until to be cleared")
	}
	if _, ok := n.Properties.GetString("resolution"); ok {
		t.Error("expected resolution to be cleared")
	}
}

func TestUpdateWithMeta(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "updatable with meta")

	resp, apiErr := a.Update(ctx, UpdateRequest{
		ID: id,
		Meta: map[string]any{
			"status": "done",
			"sprint": float64(24),
		},
	})
	if apiErr != nil {
		t.Fatalf("Update with meta: %v", apiErr)
	}
	if !resp.Updated {
		t.Error("expected Updated=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if v, ok := n.Properties.GetString("meta.status"); !ok || v != "done" {
		t.Errorf("expected meta.status=done, got %q", v)
	}
}

// --- Classify ---

func TestClassify(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("pending record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	eng.IndexNode(n.ID, "pending record", nil)
	if _, err := eng.Save("test"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	conf := 0.8
	_, apiErr := a.Classify(ctx, ClassifyRequest{
		ID:            n.ID,
		Temporality:   "durable",
		Confidence:    &conf,
		KnowledgeType: "semantic",
	})
	if apiErr != nil {
		t.Fatalf("Classify: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	updated, _ := eng.Graph().GetNode(n.ID)
	if ps, ok := updated.Properties.GetString("processing_status"); !ok || ps != "processed" {
		t.Errorf("expected processing_status=processed, got %q", ps)
	}
}

// --- Resolve ---

func TestResolve(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "resolvable record")

	resp, apiErr := a.Resolve(ctx, ResolveRequest{
		ID:             id,
		Resolution:     "completed",
		ResolutionNote: "done",
	})
	if apiErr != nil {
		t.Fatalf("Resolve: %v", apiErr)
	}
	if !resp.Resolved {
		t.Error("expected Resolved=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetTimestamp("valid_until"); !ok {
		t.Error("expected valid_until auto-set")
	}
}

// --- Link / Unlink ---

func TestLink(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id1 := addRecord(t, eng, "source")
	id2 := addRecord(t, eng, "target")

	resp, apiErr := a.Link(ctx, LinkRequest{
		SourceID: id1,
		TargetID: id2,
		EdgeType: "related_to",
	})
	if apiErr != nil {
		t.Fatalf("Link: %v", apiErr)
	}
	if resp.EdgeID == "" {
		t.Error("expected EdgeID in response")
	}
}

func TestUnlink(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id1 := addRecord(t, eng, "source")
	id2 := addRecord(t, eng, "target")

	eng.Lock()
	e, err := eng.Graph().AddEdge(id1, id2, "related_to", 0.5, nil)
	if err != nil {
		eng.Unlock()
		t.Fatalf("add edge: %v", err)
	}
	if _, err := eng.Save("test"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	resp, apiErr := a.Unlink(ctx, UnlinkRequest{EdgeID: e.ID})
	if apiErr != nil {
		t.Fatalf("Unlink: %v", apiErr)
	}
	if !resp.Deleted {
		t.Error("expected Deleted=true")
	}
}

// --- DeleteRecord ---

func TestDeleteRecord(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "deletable")

	resp, apiErr := a.DeleteRecord(ctx, DeleteRecordRequest{ID: id, Reason: "test reason"})
	if apiErr != nil {
		t.Fatalf("DeleteRecord: %v", apiErr)
	}
	if !resp.Deleted {
		t.Error("expected Deleted=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if ps, ok := n.Properties.GetString("processing_status"); !ok || ps != "deleted" {
		t.Errorf("expected processing_status=deleted, got %q", ps)
	}
}
