package api

import (
	"context"
	"slices"
	"testing"
)

// TestTemplateRegistryHasStarterTemplates confirms the starter
// templates ship embedded + loadable. Adding a new template:
// extend the want map AND TestTemplateThreeKnobsExplicit's entries
// so its three behaviour knobs are explicit.
func TestTemplateRegistryHasStarterTemplates(t *testing.T) {
	names := ListTemplates()
	want := map[string]bool{
		"backlog":       false,
		"todo":          false,
		"reading-list":  false,
		"shopping-list": false,
		"packing-list":  false,
		"journal":       false,
		"references":    false,
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

// TestTemplateKnobsExplicit asserts that every starter template sets
// both behaviour knobs (curation, contradictions) explicitly.
// Templates that don't set a knob would inherit the global default,
// which can drift from the template's intent. Catching this at the
// template-registry level prevents accidental drops.
func TestTemplateKnobsExplicit(t *testing.T) {
	want := map[string]struct {
		curation       string
		contradictions string
	}{
		"backlog":       {curation: "standard", contradictions: "on"},
		"todo":          {curation: "standard", contradictions: "on"},
		"reading-list":  {curation: "standard", contradictions: "off"},
		"shopping-list": {curation: "none", contradictions: "off"},
		"packing-list":  {curation: "none", contradictions: "off"},
		"journal":       {curation: "standard", contradictions: "off"},
		"references":    {curation: "standard", contradictions: "off"},
	}
	for name, w := range want {
		tmpl, ok := LookupTemplate(name)
		if !ok {
			t.Errorf("template %q missing from registry", name)
			continue
		}
		if tmpl.Curation != w.curation {
			t.Errorf("%s.curation = %q, want %q", name, tmpl.Curation, w.curation)
		}
		if tmpl.Contradictions != w.contradictions {
			t.Errorf("%s.contradictions = %q, want %q", name, tmpl.Contradictions, w.contradictions)
		}
	}
}

// TestTemplateContentFieldsExplicit pins each starter template's
// content_fields declaration. content_fields drives RecordContent
// (the LLM/embedding text representation of an item) so a silent
// drop or reorder would change classify/summarize input. The test
// catches that at template-load time. Curation=none templates
// (shopping/packing) intentionally omit content_fields -- they
// don't enter the LLM pipeline.
func TestTemplateContentFieldsExplicit(t *testing.T) {
	want := map[string][]string{
		"backlog":      {"title", "details"},
		"todo":         {"title", "notes"},
		"reading-list": {"title", "author", "notes"},
		"journal":      {"title", "entry"},
		"references":   {"title", "description", "notes"},
	}
	for name, w := range want {
		tmpl, ok := LookupTemplate(name)
		if !ok {
			t.Errorf("template %q missing from registry", name)
			continue
		}
		if tmpl.Schema == nil {
			t.Errorf("%s schema missing", name)
			continue
		}
		if !slices.Equal(tmpl.Schema.ContentFields, w) {
			t.Errorf("%s.content_fields = %v, want %v", name, tmpl.Schema.ContentFields, w)
		}
	}
	for _, name := range []string{"shopping-list", "packing-list"} {
		tmpl, ok := LookupTemplate(name)
		if !ok {
			t.Errorf("template %q missing", name)
			continue
		}
		if tmpl.Schema != nil && len(tmpl.Schema.ContentFields) != 0 {
			t.Errorf("%s.content_fields should be empty (curation=none), got %v", name, tmpl.Schema.ContentFields)
		}
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
	if got := CollectionCuration(n); got != CurationNone {
		t.Errorf("Curation = %q, want none (shopping-list template default)", got)
	}
	if got := CollectionContradictions(n); got != ContradictionsOff {
		t.Errorf("Contradictions = %q, want off (shopping-list template default)", got)
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

	// shopping-list template is curation=none + contradictions=off.
	// Override curation to standard (one of the two valid values) and
	// supply a schema with content_fields, since curation=standard
	// requires content_fields declared (enforced at create time).
	resp, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:           "Notepad Groceries",
		Template:       "shopping-list",
		Curation:       "standard",
		Contradictions: "on",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
				{Name: "notes", Type: FieldTypeString},
			},
			ContentFields: []string{"title", "notes"},
		},
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	n, _ := eng.Graph().GetNode(resp.ID)
	if got := CollectionCuration(n); got != CurationStandard {
		t.Errorf("override failed: Curation = %q, want standard", got)
	}
	if got := CollectionContradictions(n); got != ContradictionsOn {
		t.Errorf("override failed: Contradictions = %q, want on", got)
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
