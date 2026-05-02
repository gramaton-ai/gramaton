package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestCollectionConfigDefaults confirms the read-time fallback: a
// collection created without explicit config returns the default for
// every getter. This is the "no migration sweep needed" guarantee --
// Phase 5 + Phase 8 consumers can read through these getters without
// worrying about pre-existing collections.
func TestCollectionConfigDefaults(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "No-Config"})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	n, ok := eng.Graph().GetNode(coll.ID)
	if !ok {
		t.Fatal("collection node not found")
	}

	if got := CollectionClearMode(n); got != DefaultClearMode {
		t.Errorf("ClearMode default: got %q, want %q", got, DefaultClearMode)
	}
	if got := CollectionSupersession(n); got != DefaultSupersession {
		t.Errorf("Supersession default: got %q, want %q", got, DefaultSupersession)
	}
	if got := CollectionCuration(n); got != DefaultCuration {
		t.Errorf("Curation default: got %q, want %q", got, DefaultCuration)
	}
}

// TestCollectionConfigRoundTrip creates a collection with explicit
// config for each field and verifies the getters read it back. Also
// verifies the values landed as raw node properties.
func TestCollectionConfigRoundTrip(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:         "With-Config",
		ClearMode:    "unlink",
		Supersession: "store",
		Curation:     "minimal",
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	n, _ := eng.Graph().GetNode(coll.ID)

	if got := CollectionClearMode(n); got != ClearModeUnlink {
		t.Errorf("ClearMode: got %q, want unlink", got)
	}
	if got := CollectionSupersession(n); got != SupersessionStore {
		t.Errorf("Supersession: got %q, want store", got)
	}
	if got := CollectionCuration(n); got != CurationMinimal {
		t.Errorf("Curation: got %q, want minimal", got)
	}

	// Sanity: raw property names.
	if v, _ := n.Properties.GetString("collection_clear_mode"); v != "unlink" {
		t.Errorf("raw collection_clear_mode: %q", v)
	}
	if v, _ := n.Properties.GetString("collection_supersession"); v != "store" {
		t.Errorf("raw collection_supersession: %q", v)
	}
	if v, _ := n.Properties.GetString("collection_curation"); v != "minimal" {
		t.Errorf("raw collection_curation: %q", v)
	}
}

func TestCollectionConfigInvalidClearMode(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:      "X",
		ClearMode: "delete",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for clear_mode=delete, got %+v", apiErr)
	}
}

func TestCollectionConfigInvalidSupersession(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:         "X",
		Supersession: "galactic",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for supersession=galactic, got %+v", apiErr)
	}
}

func TestCollectionConfigInvalidCuration(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:     "X",
		Curation: "aggressive",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for curation=aggressive, got %+v", apiErr)
	}
}

// TestCollectionConfigGettersOnNilNode returns defaults for nil input
// so callers don't have to nil-check before reading config.
func TestCollectionConfigGettersOnNilNode(t *testing.T) {
	var n *graph.Node
	if got := CollectionClearMode(n); got != DefaultClearMode {
		t.Errorf("nil node ClearMode: got %q, want %q", got, DefaultClearMode)
	}
	if got := CollectionSupersession(n); got != DefaultSupersession {
		t.Errorf("nil node Supersession: got %q, want %q", got, DefaultSupersession)
	}
	if got := CollectionCuration(n); got != DefaultCuration {
		t.Errorf("nil node Curation: got %q, want %q", got, DefaultCuration)
	}
}
