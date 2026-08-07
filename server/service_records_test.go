package server

import (
	"context"
	"testing"
)

// Tests here cover the server-level serviceSave wrapper that
// handler_intake.go still depends on. Coverage of the other record
// operations (Inspect/Update/Classify/Resolve/Link/Unlink/Delete) moved
// to api/records_test.go when the dead server-level services were
// removed in the canonical-api cascade cleanup.

func TestServiceCaptureBasic(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, svcErr := srv.serviceSave(context.Background(), &saveRequest{
		Content:     "test knowledge record",
		Temporality: "durable",
	})
	if svcErr != nil {
		t.Fatalf("serviceSave: %v", svcErr)
	}

	id, ok := result["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected id in result")
	}
}

func TestServiceCaptureValidation(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceSave(context.Background(), &saveRequest{})
	if svcErr == nil {
		t.Fatal("expected error for empty content")
	}
	if svcErr.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %s", svcErr.Code)
	}
}

func TestServiceCaptureWithMeta(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, svcErr := srv.serviceSave(context.Background(), &saveRequest{
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
		t.Fatalf("serviceSave with meta: %v", svcErr)
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

	_, svcErr := srv.serviceSave(context.Background(), &saveRequest{
		Content: "test",
		Meta:    map[string]any{"nested": map[string]any{"bad": true}},
	})
	if svcErr == nil {
		t.Fatal("expected error for nested map in meta")
	}

	_, svcErr = srv.serviceSave(context.Background(), &saveRequest{
		Content: "test",
		Meta:    map[string]any{"": "value"},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty meta key")
	}
}
