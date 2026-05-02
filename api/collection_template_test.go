package api

import (
	"context"
	"testing"
)

// TestTemplateRegistryHasStarterFive confirms the five starter
// templates ship embedded + loadable.
func TestTemplateRegistryHasStarterFive(t *testing.T) {
	names := ListTemplates()
	want := map[string]bool{
		"backlog":       false,
		"todo":          false,
		"reading-list":  false,
		"shopping-list": false,
		"packing-list":  false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("missing template: %s (registered: %v)", n, names)
		}
	}
}

// TestTemplateLookup returns the parsed descriptor for a known
// template and false for an unknown one.
func TestTemplateLookup(t *testing.T) {
	tmpl, ok := LookupTemplate("backlog")
	if !ok {
		t.Fatal("backlog template should exist")
	}
	if tmpl.Name != "backlog" {
		t.Errorf("Name = %q, want backlog", tmpl.Name)
	}
	if tmpl.ClearMode != "resolve" {
		t.Errorf("ClearMode = %q, want resolve", tmpl.ClearMode)
	}
	if tmpl.Schema == nil || len(tmpl.Schema.Fields) == 0 {
		t.Errorf("backlog schema should have fields, got %+v", tmpl.Schema)
	}

	if _, ok := LookupTemplate("nope"); ok {
		t.Error("LookupTemplate(\"nope\") should return false")
	}
}

// TestCollectionCreateWithTemplate confirms the end-to-end flow:
// pass template=shopping-list to CollectionCreate, expect the
// resulting collection to carry the template's schema + behaviour
// fields as node properties.
func TestCollectionCreateWithTemplate(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	resp, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "Weekly Groceries",
		Template: "shopping-list",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	n, ok := eng.Graph().GetNode(resp.ID)
	if !ok {
		t.Fatal("collection node missing")
	}

	// Template's shopping-list config landed as node properties.
	if got := CollectionClearMode(n); got != ClearModeResolve {
		t.Errorf("ClearMode = %q, want resolve", got)
	}
	if got := CollectionCuration(n); got != CurationMinimal {
		t.Errorf("Curation = %q, want minimal (template default)", got)
	}

	// Schema landed from the template.
	schema, _ := loadSchema(n)
	if schema == nil || len(schema.Fields) == 0 {
		t.Errorf("expected schema from template, got %+v", schema)
	}
	var hasTitle, hasQuantity bool
	for _, f := range schema.Fields {
		if f.Name == "title" {
			hasTitle = true
		}
		if f.Name == "quantity" {
			hasQuantity = true
		}
	}
	if !hasTitle || !hasQuantity {
		t.Errorf("expected title + quantity fields from shopping-list template, got %+v", schema.Fields)
	}
}

// TestCollectionCreateTemplateOverride confirms shallow-merge: a
// caller-provided field wins over the template's value.
func TestCollectionCreateTemplateOverride(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	// shopping-list template is minimal curation; override to full.
	resp, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "Notepad Groceries",
		Template: "shopping-list",
		Curation: "full",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	n, _ := eng.Graph().GetNode(resp.ID)
	if got := CollectionCuration(n); got != CurationFull {
		t.Errorf("override failed: Curation = %q, want full", got)
	}
	// But ClearMode stays from the template (caller didn't override).
	if got := CollectionClearMode(n); got != ClearModeResolve {
		t.Errorf("non-overridden field lost: ClearMode = %q, want resolve", got)
	}
}

// TestCollectionCreateUnknownTemplateRejected: garbage template
// name fails validation with an ErrInvalid.
func TestCollectionCreateUnknownTemplateRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	_, apiErr := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:     "X",
		Template: "does-not-exist",
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for unknown template, got %+v", apiErr)
	}
}
