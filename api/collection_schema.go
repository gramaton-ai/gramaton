package api

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// fieldNameRe restricts field names to safe characters that won't
// collide with property key namespacing (dot-separated prefixes).
var fieldNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// SchemaFieldType identifies the type of a collection schema field.
type SchemaFieldType string

const (
	FieldTypeString  SchemaFieldType = "string"
	FieldTypeNumber  SchemaFieldType = "number"
	FieldTypeBoolean SchemaFieldType = "boolean"
	FieldTypeDate    SchemaFieldType = "date"
	FieldTypeEnum    SchemaFieldType = "enum"
	FieldTypeEnumSet SchemaFieldType = "enum[]"
)

var validFieldTypes = map[SchemaFieldType]bool{
	FieldTypeString:  true,
	FieldTypeNumber:  true,
	FieldTypeBoolean: true,
	FieldTypeDate:    true,
	FieldTypeEnum:    true,
	FieldTypeEnumSet: true,
}

// SchemaField defines a single field in a collection schema.
type SchemaField struct {
	Name     string          `json:"name"`
	Type     SchemaFieldType `json:"type"`
	Required bool            `json:"required"`
	Values   []string        `json:"values,omitempty"` // enum and enum[] only
}

// CollectionSchema defines what fields items in a collection must/may have.
//
// ContentFields is an ordered list of field names whose values
// constitute the canonical text representation of an item for
// LLM-stage curation (classify, summarize, contradictions, concept
// synthesis) and for embedding. Each name must reference a declared
// field of type=string. When unset, items in this collection use a
// schemaless wide concatenation of every field.* string property
// (acceptable for curation=none collections; rejected at create
// time for curation=standard collections lacking a template).
type CollectionSchema struct {
	Fields        []SchemaField `json:"fields"`
	ContentFields []string      `json:"content_fields,omitempty" yaml:"content_fields,omitempty"`
}

const (
	maxCollectionNameLen = 128
	maxSchemaFields      = 50
	maxEnumValues        = 100
	maxFieldNameLen      = 64
	maxEnumValueLen      = 256
	maxFieldStringLen    = 50000
)

// validateSchema checks that a schema definition is well-formed.
func validateSchema(s *CollectionSchema) error {
	if s == nil {
		return nil
	}
	if len(s.Fields) == 0 {
		return fmt.Errorf("schema must have at least one field")
	}
	if len(s.Fields) > maxSchemaFields {
		return fmt.Errorf("schema exceeds maximum of %d fields", maxSchemaFields)
	}

	seen := make(map[string]bool, len(s.Fields))
	for i, f := range s.Fields {
		if f.Name == "" {
			return fmt.Errorf("field %d: name is required", i)
		}
		if len(f.Name) > maxFieldNameLen {
			return fmt.Errorf("field %q: name exceeds %d characters", f.Name, maxFieldNameLen)
		}
		if !fieldNameRe.MatchString(f.Name) {
			return fmt.Errorf("field %q: name must match [a-zA-Z_][a-zA-Z0-9_]*", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("field %q: duplicate name", f.Name)
		}
		seen[f.Name] = true

		if !validFieldTypes[f.Type] {
			return fmt.Errorf("field %q: unknown type %q", f.Name, f.Type)
		}

		// Values only allowed for enum types.
		if f.Type == FieldTypeEnum || f.Type == FieldTypeEnumSet {
			if len(f.Values) == 0 {
				return fmt.Errorf("field %q: enum type requires at least one value", f.Name)
			}
			if len(f.Values) > maxEnumValues {
				return fmt.Errorf("field %q: exceeds maximum of %d enum values", f.Name, maxEnumValues)
			}
			enumSeen := make(map[string]bool, len(f.Values))
			for _, v := range f.Values {
				if v == "" {
					return fmt.Errorf("field %q: enum values cannot be empty", f.Name)
				}
				if len(v) > maxEnumValueLen {
					return fmt.Errorf("field %q: enum value %q exceeds %d characters", f.Name, v, maxEnumValueLen)
				}
				if enumSeen[v] {
					return fmt.Errorf("field %q: duplicate enum value %q", f.Name, v)
				}
				enumSeen[v] = true
			}
		} else if len(f.Values) > 0 {
			return fmt.Errorf("field %q: values only allowed for enum and enum[] types", f.Name)
		}
	}

	// Validate content_fields if declared. Each name must reference a
	// declared field, the referenced field must be type=string, and
	// each name must appear at most once. Empty/unset is permitted
	// here -- the curation=standard requirement is enforced at
	// CollectionCreate time so curation=none collections (which never
	// read content_fields) aren't burdened with the declaration.
	if len(s.ContentFields) > 0 {
		fieldDefs := make(map[string]SchemaField, len(s.Fields))
		for _, f := range s.Fields {
			fieldDefs[f.Name] = f
		}
		seenCF := make(map[string]bool, len(s.ContentFields))
		for i, name := range s.ContentFields {
			if name == "" {
				return fmt.Errorf("content_fields[%d]: name cannot be empty", i)
			}
			def, ok := fieldDefs[name]
			if !ok {
				return fmt.Errorf("content_fields[%d]: %q is not a declared field", i, name)
			}
			if def.Type != FieldTypeString {
				return fmt.Errorf("content_fields[%d]: %q is type %q; only string fields are allowed", i, name, def.Type)
			}
			if seenCF[name] {
				return fmt.Errorf("content_fields[%d]: duplicate field %q", i, name)
			}
			seenCF[name] = true
		}
	}

	return nil
}

// validateItemFields checks that an item's fields satisfy the collection
// schema. If schema is nil, field names are still validated and list
// values are type-checked so setFieldProps cannot panic on a non-string
// element (the schema branch enforces this via validateFieldValue).
func validateItemFields(schema *CollectionSchema, fields map[string]any) error {
	if schema == nil {
		for name, val := range fields {
			if !fieldNameRe.MatchString(name) {
				return fmt.Errorf("field name %q contains invalid characters", name)
			}
			if arr, ok := val.([]any); ok {
				for i, elem := range arr {
					if _, ok := elem.(string); !ok {
						return fmt.Errorf("field %q element %d: expected string, got %T", name, i, elem)
					}
				}
			}
		}
		return nil
	}

	// Build lookup.
	fieldDefs := make(map[string]SchemaField, len(schema.Fields))
	for _, f := range schema.Fields {
		fieldDefs[f.Name] = f
	}

	// Check required fields are present.
	for _, f := range schema.Fields {
		if f.Required {
			v, ok := fields[f.Name]
			if !ok || v == nil {
				return fmt.Errorf("required field %q is missing", f.Name)
			}
		}
	}

	// Check all provided fields are declared and type-correct.
	for name, val := range fields {
		// Validate field name characters even if schema allows it,
		// since names become property keys (field.<name>).
		if !fieldNameRe.MatchString(name) {
			return fmt.Errorf("field name %q contains invalid characters", name)
		}
		if val == nil {
			continue // explicit null is allowed
		}
		def, ok := fieldDefs[name]
		if !ok {
			return fmt.Errorf("unknown field %q (not in schema)", name)
		}
		if err := validateFieldValue(def, val); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
	}

	return nil
}

// validateFieldValue checks that a value matches the expected type.
func validateFieldValue(f SchemaField, val any) error {
	switch f.Type {
	case FieldTypeString:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", val)
		}
		if len(s) > maxFieldStringLen {
			return fmt.Errorf("string exceeds maximum length of %d", maxFieldStringLen)
		}

	case FieldTypeNumber:
		switch v := val.(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("value must be a finite number")
			}
		case int:
			// OK
		default:
			return fmt.Errorf("expected number, got %T", val)
		}

	case FieldTypeBoolean:
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", val)
		}

	case FieldTypeDate:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected date string, got %T", val)
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				return fmt.Errorf("expected date (YYYY-MM-DD or RFC3339), got %q", s)
			}
		}

	case FieldTypeEnum:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", val)
		}
		valid := false
		for _, v := range f.Values {
			if s == v {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("value %q not in allowed values %v", s, f.Values)
		}

	case FieldTypeEnumSet:
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("expected array, got %T", val)
		}
		allowed := make(map[string]bool, len(f.Values))
		for _, v := range f.Values {
			allowed[v] = true
		}
		for i, item := range arr {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("element %d: expected string, got %T", i, item)
			}
			if !allowed[s] {
				return fmt.Errorf("element %d: value %q not in allowed values %v", i, s, f.Values)
			}
		}
	}

	return nil
}

// parseCollectionSchema deserializes a schema from the stored JSON string.
func parseCollectionSchema(raw string) (*CollectionSchema, error) {
	if raw == "" {
		return nil, nil
	}
	var s CollectionSchema
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("parse collection schema: %w", err)
	}
	return &s, nil
}

// serializeCollectionSchema serializes a schema to a JSON string for storage.
func serializeCollectionSchema(s *CollectionSchema) (string, error) {
	if s == nil {
		return "", nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("serialize collection schema: %w", err)
	}
	return string(data), nil
}

// validateCollectionName checks that a collection name meets requirements.
func validateCollectionName(name string) error {
	if name == "" {
		return fmt.Errorf("collection name is required")
	}
	if len(name) > maxCollectionNameLen {
		return fmt.Errorf("collection name exceeds %d characters", maxCollectionNameLen)
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("collection name cannot be only whitespace")
	}
	if trimmed != name {
		return fmt.Errorf("collection name cannot have leading or trailing whitespace")
	}
	return nil
}
