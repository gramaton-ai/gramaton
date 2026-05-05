package api

import (
	"strings"
	"testing"
)

// TestValidateSchema_ContentFieldsValid pins the happy path: a
// schema with an ordered content_fields list referencing declared
// type=string fields validates clean.
func TestValidateSchema_ContentFieldsValid(t *testing.T) {
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "details", Type: FieldTypeString},
			{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
		},
		ContentFields: []string{"title", "details"},
	}
	if err := validateSchema(s); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestValidateSchema_ContentFieldsRejectsUndeclaredField(t *testing.T) {
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
		ContentFields: []string{"title", "ghost"},
	}
	err := validateSchema(s)
	if err == nil {
		t.Fatal("expected error for undeclared content_field, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "not a declared field") {
		t.Errorf("error message should name the offender; got: %v", err)
	}
}

func TestValidateSchema_ContentFieldsRejectsEnum(t *testing.T) {
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
		},
		ContentFields: []string{"title", "status"},
	}
	err := validateSchema(s)
	if err == nil {
		t.Fatal("expected error for enum in content_fields, got nil")
	}
	if !strings.Contains(err.Error(), "status") || !strings.Contains(err.Error(), "string") {
		t.Errorf("error message should explain the type rejection; got: %v", err)
	}
}

func TestValidateSchema_ContentFieldsRejectsNonStringTypes(t *testing.T) {
	cases := []struct {
		name      string
		fieldType SchemaFieldType
		values    []string
	}{
		{"number", FieldTypeNumber, nil},
		{"boolean", FieldTypeBoolean, nil},
		{"date", FieldTypeDate, nil},
		{"enum_set", FieldTypeEnumSet, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &CollectionSchema{
				Fields: []SchemaField{
					{Name: "title", Type: FieldTypeString, Required: true},
					{Name: "f", Type: tc.fieldType, Values: tc.values},
				},
				ContentFields: []string{"title", "f"},
			}
			if err := validateSchema(s); err == nil {
				t.Errorf("%s in content_fields should be rejected", tc.fieldType)
			}
		})
	}
}

func TestValidateSchema_ContentFieldsRejectsDuplicate(t *testing.T) {
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
		ContentFields: []string{"title", "title"},
	}
	err := validateSchema(s)
	if err == nil {
		t.Fatal("expected error for duplicate content_field, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error message should mention duplicate; got: %v", err)
	}
}

func TestValidateSchema_ContentFieldsRejectsEmptyName(t *testing.T) {
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
		ContentFields: []string{""},
	}
	if err := validateSchema(s); err == nil {
		t.Fatal("expected error for empty content_field name, got nil")
	}
}

func TestValidateSchema_ContentFieldsEmptyAllowed(t *testing.T) {
	// Empty/unset content_fields is fine at schema-validation time.
	// CollectionCreate enforces curation=standard requires it; this
	// validator is shared with curation=none collections that
	// legitimately omit it.
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
	}
	if err := validateSchema(s); err != nil {
		t.Fatalf("empty content_fields should validate clean, got: %v", err)
	}
}

func TestValidateSchema_ContentFieldsOrderingPreservedAfterRoundtrip(t *testing.T) {
	// content_fields ordering is load-bearing for RecordContent join
	// order. Pin that JSON serialization preserves it.
	s := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "details", Type: FieldTypeString},
		},
		ContentFields: []string{"details", "title"},
	}
	raw, err := serializeCollectionSchema(s)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	parsed, err := parseCollectionSchema(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.ContentFields) != 2 || parsed.ContentFields[0] != "details" || parsed.ContentFields[1] != "title" {
		t.Errorf("roundtrip lost ordering: got %v, want [details title]", parsed.ContentFields)
	}
}
