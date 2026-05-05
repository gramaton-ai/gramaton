package api

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestCollectionConfigDefaults confirms the read-time fallback: a
// collection created without explicit config returns the default for
// every getter. This is the "no migration sweep needed" guarantee --
// downstream consumers can read through these getters without
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
	if got := CollectionContradictions(n); got != DefaultContradictions {
		t.Errorf("Contradictions default: got %q, want %q", got, DefaultContradictions)
	}
}

// TestCollectionConfigRoundTrip creates a collection with explicit
// config for each field and verifies the getters read it back. Also
// verifies the values landed as raw node properties.
func TestCollectionConfigRoundTrip(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	coll, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:           "With-Config",
		ClearMode:      "unlink",
		Supersession:   "store",
		Curation:       "none",
		Contradictions: "off",
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
	if got := CollectionCuration(n); got != CurationNone {
		t.Errorf("Curation: got %q, want none", got)
	}
	if got := CollectionContradictions(n); got != ContradictionsOff {
		t.Errorf("Contradictions: got %q, want off", got)
	}

	// Sanity: raw property names.
	if v, _ := n.Properties.GetString("collection_clear_mode"); v != "unlink" {
		t.Errorf("raw collection_clear_mode: %q", v)
	}
	if v, _ := n.Properties.GetString("collection_supersession"); v != "store" {
		t.Errorf("raw collection_supersession: %q", v)
	}
	if v, _ := n.Properties.GetString("collection_curation"); v != "none" {
		t.Errorf("raw collection_curation: %q", v)
	}
	if v, _ := n.Properties.GetString("collection_contradictions"); v != "off" {
		t.Errorf("raw collection_contradictions: %q", v)
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

// TestCollectionConfigInvalidContradictions ensures unknown values
// for the new contradictions knob are rejected at write time.
func TestCollectionConfigInvalidContradictions(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:           "X",
		Contradictions: "maybe",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for contradictions=maybe, got %+v", apiErr)
	}
}

// TestCollectionConfigLegacyCurationRejectedOnWrite confirms that
// the dropped 4-level enum values are rejected when written via the
// API (only "standard" and "none" are accepted). Reads still
// normalize legacy values; see TestCollectionCurationLegacyNormalize.
func TestCollectionConfigLegacyCurationRejectedOnWrite(t *testing.T) {
	a, _ := setupTestAPI(t)
	for _, v := range []string{"minimal", "full"} {
		_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
			Name:     "Legacy-" + v,
			Curation: v,
		})
		if apiErr == nil || apiErr.Code != "input_error" {
			t.Errorf("curation=%q should be rejected on write, got %+v", v, apiErr)
		}
	}
}

// TestCollectionCurationLegacyNormalize verifies that collections
// with the dropped 4-level enum values stored on-disk continue to
// work: the getter normalizes "minimal" to "none" and "full" to
// "standard". This is the no-migration-sweep contract for stores
// that pre-date the redesign.
func TestCollectionCurationLegacyNormalize(t *testing.T) {
	cases := []struct {
		stored string
		want   Curation
	}{
		{"minimal", CurationNone},
		{"full", CurationStandard},
		{"standard", CurationStandard},
		{"none", CurationNone},
		{"", CurationNone}, // empty -> DefaultCuration (none)
	}
	for _, tc := range cases {
		n := &graph.Node{
			ID:         "n_test",
			Properties: graph.Properties{},
		}
		if tc.stored != "" {
			n.Properties[propCuration] = graph.StringProperty(tc.stored)
		}
		if got := CollectionCuration(n); got != tc.want {
			t.Errorf("stored=%q: got %q, want %q", tc.stored, got, tc.want)
		}
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
	if got := CollectionContradictions(n); got != DefaultContradictions {
		t.Errorf("nil node Contradictions: got %q, want %q", got, DefaultContradictions)
	}
}
