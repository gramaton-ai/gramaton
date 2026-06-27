package core

import (
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// memoryNode constructs a Node mimicking a Memory record (has
// content_full). No field.* properties.
func memoryNode(content string) *graph.Node {
	return &graph.Node{
		ID: "mem-1",
		Properties: graph.Properties{
			"content_full": graph.StringProperty(content),
		},
	}
}

// itemNode constructs a Node mimicking a collection item (no
// content_full; a set of field.* properties).
func itemNode(fields map[string]string) *graph.Node {
	props := graph.Properties{}
	for k, v := range fields {
		props["field."+k] = graph.StringProperty(v)
	}
	return &graph.Node{ID: "item-1", Properties: props}
}

func TestRecordIndexText_MemoryRecordReturnsContentFull(t *testing.T) {
	n := memoryNode("a memory record body")
	got := RecordIndexText(n)
	if got != "a memory record body" {
		t.Errorf("RecordIndexText: got %q, want content_full passthrough", got)
	}
}

func TestRecordIndexText_CollectionItemConcatenatesFieldStrings(t *testing.T) {
	n := itemNode(map[string]string{
		"title":   "implement caching layer",
		"details": "redis vs postgres for session store",
		"status":  "open",
		"area":    "backend",
	})
	got := RecordIndexText(n)
	// Order is non-deterministic (map iteration). Just check every
	// string appears.
	for _, want := range []string{"implement caching layer", "redis vs postgres for session store", "open", "backend"} {
		if !strings.Contains(got, want) {
			t.Errorf("RecordIndexText missing %q; got %q", want, got)
		}
	}
}

func TestRecordIndexText_NilNode(t *testing.T) {
	if got := RecordIndexText(nil); got != "" {
		t.Errorf("RecordIndexText(nil) = %q, want empty", got)
	}
}

func TestRecordIndexText_EmptyNode(t *testing.T) {
	n := &graph.Node{ID: "x", Properties: graph.Properties{}}
	if got := RecordIndexText(n); got != "" {
		t.Errorf("RecordIndexText(empty) = %q, want empty", got)
	}
}

func TestRecordIndexText_IgnoresNonStringProperties(t *testing.T) {
	// Non-field.* properties shouldn't leak in; non-string field.*
	// shouldn't leak in either. Non-string field.* is unusual but
	// handled defensively.
	n := &graph.Node{
		ID: "x",
		Properties: graph.Properties{
			"field.title":  graph.StringProperty("hello"),
			"field.rating": graph.Float64Property(4.5),
			"created_at":   graph.StringProperty("2026-05-04"),
			"access_count": graph.Int64Property(7),
		},
	}
	got := RecordIndexText(n)
	if got != "hello" {
		t.Errorf("RecordIndexText = %q, want %q", got, "hello")
	}
}

func TestRecordContent_MemoryRecordReturnsContentFull(t *testing.T) {
	n := memoryNode("a memory record body")
	got := RecordContent(n, []string{"anything"})
	if got != "a memory record body" {
		t.Errorf("RecordContent on Memory record: got %q, want content_full passthrough", got)
	}
}

func TestRecordContent_OrderedJoinFromContentFields(t *testing.T) {
	n := itemNode(map[string]string{
		"title":   "implement caching layer",
		"details": "redis vs postgres for session store",
		"status":  "open",
		"area":    "backend",
	})
	got := RecordContent(n, []string{"title", "details"})
	want := "implement caching layer\nredis vs postgres for session store"
	if got != want {
		t.Errorf("RecordContent ordered:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestRecordContent_OrderingIsRespected(t *testing.T) {
	n := itemNode(map[string]string{
		"title":   "T-04",
		"details": "the body",
	})
	a := RecordContent(n, []string{"title", "details"})
	b := RecordContent(n, []string{"details", "title"})
	if a == b {
		t.Errorf("RecordContent ignores ordering: both produced %q", a)
	}
	if a != "T-04\nthe body" {
		t.Errorf("RecordContent [title,details] = %q, want %q", a, "T-04\nthe body")
	}
	if b != "the body\nT-04" {
		t.Errorf("RecordContent [details,title] = %q, want %q", b, "the body\nT-04")
	}
}

func TestRecordContent_SkipsMissingFields(t *testing.T) {
	// content_fields names a field that the item doesn't have set.
	n := itemNode(map[string]string{
		"title": "T-04 caching",
		// no details on this ticket
	})
	got := RecordContent(n, []string{"title", "details"})
	want := "T-04 caching"
	if got != want {
		t.Errorf("RecordContent with missing field: got %q, want %q", got, want)
	}
}

func TestRecordContent_SkipsEmptyFields(t *testing.T) {
	// field.details is set but empty -- treat as missing for the
	// purpose of synthesis (no blank-line padding).
	n := itemNode(map[string]string{
		"title":   "T-04",
		"details": "",
	})
	got := RecordContent(n, []string{"title", "details"})
	want := "T-04"
	if got != want {
		t.Errorf("RecordContent with empty field: got %q, want %q", got, want)
	}
}

func TestRecordContent_EmptyContentFieldsFallsBackToWide(t *testing.T) {
	// Schemaless / ad-hoc collection: caller passes empty list.
	// Helper falls back to RecordIndexText's wide concatenation.
	n := itemNode(map[string]string{
		"title":  "the title",
		"status": "the status",
	})
	got := RecordContent(n, nil)
	for _, want := range []string{"the title", "the status"} {
		if !strings.Contains(got, want) {
			t.Errorf("RecordContent fallback missing %q; got %q", want, got)
		}
	}
}

func TestRecordContent_NilNode(t *testing.T) {
	if got := RecordContent(nil, []string{"title"}); got != "" {
		t.Errorf("RecordContent(nil) = %q, want empty", got)
	}
}

func TestRecordContent_AllFieldsMissingReturnsEmpty(t *testing.T) {
	// content_fields names fields that don't exist on the node.
	n := itemNode(map[string]string{
		"title": "the title",
	})
	got := RecordContent(n, []string{"missing1", "missing2"})
	if got != "" {
		t.Errorf("RecordContent all-missing: got %q, want empty", got)
	}
}

func TestRecordContentFromFields_OrderedJoin(t *testing.T) {
	fields := map[string]any{
		"title":   "the title",
		"details": "the details",
		"area":    "the area",
	}
	got := RecordContentFromFields([]string{"title", "details"}, fields)
	want := "the title\nthe details"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRecordContentFromFields_OrderingRespected(t *testing.T) {
	fields := map[string]any{"title": "T", "details": "D"}
	a := RecordContentFromFields([]string{"title", "details"}, fields)
	b := RecordContentFromFields([]string{"details", "title"}, fields)
	if a == b {
		t.Errorf("ordering ignored: both produced %q", a)
	}
}

func TestRecordContentFromFields_SkipsMissingAndEmpty(t *testing.T) {
	fields := map[string]any{
		"title":   "the title",
		"details": "",
	}
	got := RecordContentFromFields([]string{"title", "details", "ghost"}, fields)
	if got != "the title" {
		t.Errorf("got %q, want %q", got, "the title")
	}
}

func TestRecordContentFromFields_SkipsNonStringTypes(t *testing.T) {
	// Schema validation should keep enums/numbers out of
	// content_fields, but defend against the wrong type at runtime.
	fields := map[string]any{
		"title":  "the title",
		"rating": 4.5,
		"open":   true,
	}
	got := RecordContentFromFields([]string{"title", "rating", "open"}, fields)
	if got != "the title" {
		t.Errorf("got %q, want %q", got, "the title")
	}
}

func TestRecordContentFromFields_SchemalessFallback(t *testing.T) {
	// Empty contentFields -> wide concat (every string-typed value).
	fields := map[string]any{
		"title": "the title",
		"notes": "the notes",
	}
	got := RecordContentFromFields(nil, fields)
	for _, want := range []string{"the title", "the notes"} {
		if !strings.Contains(got, want) {
			t.Errorf("schemaless fallback missing %q; got %q", want, got)
		}
	}
}

func TestRecordContentFromFields_ParityWithRecordContent(t *testing.T) {
	// The two helpers should produce equivalent text for the same
	// inputs. Insert-time pre-embed and post-insert reembed must
	// converge for embedding stability.
	fields := map[string]any{
		"title":   "T-04 caching",
		"details": "redis vs postgres",
	}
	contentFields := []string{"title", "details"}
	got := RecordContentFromFields(contentFields, fields)

	stringFields := make(map[string]string, len(fields))
	for k, v := range fields {
		if s, ok := v.(string); ok {
			stringFields[k] = s
		}
	}
	n := itemNode(stringFields)
	want := RecordContent(n, contentFields)

	if got != want {
		t.Errorf("parity violation:\n  RecordContentFromFields: %q\n  RecordContent:           %q", got, want)
	}
}
