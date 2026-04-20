package server

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// Tests here cover the server-level serviceCapture wrapper that
// handler_intake.go still depends on. Coverage of the other record
// operations (Inspect/Update/Classify/Resolve/Link/Unlink/Delete) moved
// to api/records_test.go when the dead server-level services were
// removed in the T-02 cascade cleanup.

func TestServiceCaptureBasic(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, svcErr := srv.serviceCapture(context.Background(), &captureRequest{
		Content:     "test knowledge record",
		Temporality: "durable",
	})
	if svcErr != nil {
		t.Fatalf("serviceCapture: %v", svcErr)
	}

	id, ok := result["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected id in result")
	}
}

func TestServiceCaptureValidation(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceCapture(context.Background(), &captureRequest{})
	if svcErr == nil {
		t.Fatal("expected error for empty content")
	}
	if svcErr.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %s", svcErr.Code)
	}
}

// TestServiceCaptureSupersedeSetResolution verifies Bug 1 fix: when a
// capture auto-supersedes a near-duplicate, the old record gets resolution
// and resolved_at set (previously missing in MCP path).
func TestServiceCaptureSupersedeSetResolution(t *testing.T) {
	_, eng := setupTestServer(t)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("the sky is blue on clear days"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
		"embedding_full":    graph.VectorProperty([]float32{1, 0, 0, 0}),
	})
	eng.IndexNode(n.ID, "the sky is blue on clear days", nil)
	eng.VecIdx().Add(n.ID, []float32{1, 0, 0, 0})
	eng.Save("test")
	oldID := n.ID
	eng.Unlock()

	eng.Lock()
	n2 := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("the sky is blue on clear days indeed"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
		"embedding_full":    graph.VectorProperty([]float32{1, 0, 0, 0}),
	})
	eng.IndexNode(n2.ID, "the sky is blue on clear days indeed", nil)
	eng.VecIdx().Add(n2.ID, []float32{1, 0, 0, 0})

	if dupID, sim := eng.CheckDedup(n2.ID); dupID != "" {
		now := time.Now().UTC()
		oldNode, _ := eng.Graph().GetNode(dupID)
		if oldNode != nil {
			eng.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
			eng.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
			eng.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
			eng.Graph().AddEdge(n2.ID, dupID, "supersedes", sim, nil)
		}
	}
	eng.Save("test-supersede")
	eng.Unlock()

	eng.RLock()
	defer eng.RUnlock()
	old, ok := eng.Graph().GetNode(oldID)
	if !ok {
		t.Fatal("old record not found")
	}
	if res, ok := old.Properties.GetString("resolution"); !ok || res != "superseded" {
		t.Errorf("expected resolution=superseded, got %q", res)
	}
	if _, ok := old.Properties.GetTimestamp("resolved_at"); !ok {
		t.Error("expected resolved_at to be set")
	}
	if _, ok := old.Properties.GetTimestamp("valid_until"); !ok {
		t.Error("expected valid_until to be set")
	}
}

func TestServiceCaptureWithMeta(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, svcErr := srv.serviceCapture(context.Background(), &captureRequest{
		Content:     "PLAT-142: Add rate limiting to API gateway",
		Temporality: "temporal",
		Meta: map[string]any{
			"assignee": "Sarah Chen",
			"priority": "P1",
			"sprint":   float64(23),
			"status":   "in_progress",
			"labels":   []any{"gateway", "security"},
		},
	})
	if svcErr != nil {
		t.Fatalf("serviceCapture with meta: %v", svcErr)
	}

	id := result["id"].(string)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)

	if v, ok := n.Properties.GetString("meta.assignee"); !ok || v != "Sarah Chen" {
		t.Errorf("expected meta.assignee=Sarah Chen, got %q", v)
	}
	if v, ok := n.Properties.GetString("meta.priority"); !ok || v != "P1" {
		t.Errorf("expected meta.priority=P1, got %q", v)
	}
	if v, ok := n.Properties.GetFloat64("meta.sprint"); !ok || v != 23 {
		t.Errorf("expected meta.sprint=23, got %v", v)
	}
	if v, ok := n.Properties.GetString("meta.status"); !ok || v != "in_progress" {
		t.Errorf("expected meta.status=in_progress, got %q", v)
	}
	if v, ok := n.Properties.GetStringList("meta.labels"); !ok || len(v) != 2 {
		t.Errorf("expected meta.labels=[gateway security], got %v", v)
	}
}

func TestServiceCaptureMetaValidation(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceCapture(context.Background(), &captureRequest{
		Content: "test",
		Meta:    map[string]any{"nested": map[string]any{"bad": true}},
	})
	if svcErr == nil {
		t.Fatal("expected error for nested map in meta")
	}

	_, svcErr = srv.serviceCapture(context.Background(), &captureRequest{
		Content: "test",
		Meta:    map[string]any{"": "value"},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty meta key")
	}
}
