package api

import (
	"context"
	"testing"
)

// TestInspectEffectiveCurationOrphanMemoryRecord pins that a
// freshly captured Memory record (no member_of edges) returns the
// memory-orphan defaults via inspect's effective_curation field.
// curation=standard / supersession=store / contradictions=on are
// today's Memory record behaviour.
func TestInspectEffectiveCurationOrphanMemoryRecord(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	cap, apiErr := a.Capture(ctx, CaptureRequest{Content: "orphan memory record"})
	if apiErr != nil {
		t.Fatalf("Capture: %v", apiErr)
	}
	resp, apiErr := a.Inspect(ctx, InspectRequest{ID: cap.ID})
	if apiErr != nil {
		t.Fatalf("Inspect: %v", apiErr)
	}
	if resp.EffectiveCuration == nil {
		t.Fatal("expected effective_curation on a Memory record, got nil")
	}
	got := resp.EffectiveCuration
	if got.Curation != "standard" {
		t.Errorf("Curation = %q, want standard", got.Curation)
	}
	if got.Supersession != "store" {
		t.Errorf("Supersession = %q, want store", got.Supersession)
	}
	if got.Contradictions != "on" {
		t.Errorf("Contradictions = %q, want on", got.Contradictions)
	}
}

// TestInspectEffectiveCurationCollectionItem pins that a collection
// item inherits its collection's three knobs through inspect.
// Verifies the resolver runs end-to-end via the API surface.
func TestInspectEffectiveCurationCollectionItem(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:           "Journal-style",
		Curation:       "standard",
		Supersession:   "none",
		Contradictions: "off",
	})
	item, apiErr := a.CollectionAdd(ctx, coll.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "today's entry"},
	})
	if apiErr != nil {
		t.Fatalf("CollectionAdd: %v", apiErr)
	}
	resp, apiErr := a.Inspect(ctx, InspectRequest{ID: item.ID})
	if apiErr != nil {
		t.Fatalf("Inspect: %v", apiErr)
	}
	if resp.EffectiveCuration == nil {
		t.Fatal("expected effective_curation on a collection item, got nil")
	}
	got := resp.EffectiveCuration
	if got.Curation != "standard" {
		t.Errorf("Curation = %q, want standard", got.Curation)
	}
	if got.Supersession != "none" {
		t.Errorf("Supersession = %q, want none", got.Supersession)
	}
	if got.Contradictions != "off" {
		t.Errorf("Contradictions = %q, want off", got.Contradictions)
	}
}

// TestInspectEffectiveCurationAbsentOnCollectionContainer pins that
// inspect omits effective_curation for a collection node itself
// (the container, not its items). Collection containers are
// structural; the per-record knob model doesn't apply to them.
func TestInspectEffectiveCurationAbsentOnCollectionContainer(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "X"})
	resp, apiErr := a.Inspect(ctx, InspectRequest{ID: coll.ID})
	if apiErr != nil {
		t.Fatalf("Inspect: %v", apiErr)
	}
	if resp.EffectiveCuration != nil {
		t.Errorf("expected nil effective_curation on collection container, got %+v", resp.EffectiveCuration)
	}
}
