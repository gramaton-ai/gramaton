package graph

import (
	"strings"
	"testing"
)

// memoryNode constructs a Node mimicking a Memory record (has
// content_full). No field.* properties.
func memoryNode(content string) *Node {
	return &Node{
		ID: "mem-1",
		Properties: Properties{
			"content_full": StringProperty(content),
		},
	}
}

// itemNode constructs a Node mimicking a collection item (no
// content_full; a set of field.* properties).
func itemNode(fields map[string]string) *Node {
	props := Properties{}
	for k, v := range fields {
		props["field."+k] = StringProperty(v)
	}
	return &Node{ID: "item-1", Properties: props}
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

func TestRecordIndexText_IncludesSummaryVocabulary(t *testing.T) {
	n := memoryNode("the prose body of the record")
	n.Properties["content_short"] = StringProperty("summary with cobaltmarsh vocabulary")
	got := RecordIndexText(n)
	for _, want := range []string{"prose body", "cobaltmarsh"} {
		if !strings.Contains(got, want) {
			t.Errorf("RecordIndexText missing %q; got %q", want, got)
		}
	}
}

func TestRecordIndexText_SkipsContainedSummary(t *testing.T) {
	// Observation records carry a content_short that is a truncated
	// prefix of content_full; re-appending it would only inflate term
	// frequencies without adding a findable term.
	n := memoryNode("prefix and the rest of the content")
	n.Properties["content_short"] = StringProperty("prefix and the")
	if got := RecordIndexText(n); got != "prefix and the rest of the content" {
		t.Errorf("contained summary should be skipped; got %q", got)
	}
}

func TestRecordIndexText_SegmentContentPlusSummary(t *testing.T) {
	// Session segments carry their text under "content"; the summary
	// union applies to them the same way.
	n := &Node{ID: "seg-1", Properties: Properties{
		"content":       StringProperty("segment turn text"),
		"content_short": StringProperty("distinct viridianpeak anchor"),
	}}
	got := RecordIndexText(n)
	for _, want := range []string{"segment turn text", "viridianpeak"} {
		if !strings.Contains(got, want) {
			t.Errorf("RecordIndexText missing %q; got %q", want, got)
		}
	}
}

// TestRecordIndexText_MetaValueTypes pins the meta-term loop against
// every property type setMetaProps stores. The pre-fix code called
// StringList() unchecked, which panics on any non-list meta value --
// and this loop sits on the update, curation summary-write, and
// boot-rebuild paths.
func TestRecordIndexText_MetaValueTypes(t *testing.T) {
	n := memoryNode("body")
	n.Properties["meta.owner"] = StringProperty("sarah")
	n.Properties["meta.priority"] = Float64Property(1.5)
	n.Properties["meta.sprint"] = Int64Property(23)
	n.Properties["meta.blocked"] = BoolProperty(true)
	n.Properties["meta.tags"] = StringListProperty([]string{"auth", "infra"})
	n.Properties["meta.empty"] = StringListProperty(nil)

	got := RecordIndexText(n)
	for _, want := range []string{
		"owner:sarah", "priority:1.5", "sprint:23", "blocked:true",
		"tags:auth", "tags:infra",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RecordIndexText missing meta term %q; got %q", want, got)
		}
	}
	// An empty stored list emits nothing -- no "empty:[]" artifact.
	if strings.Contains(got, "empty:") {
		t.Errorf("empty meta list leaked a term; got %q", got)
	}
}

func TestLexicalSummaryText(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		short string
		want  string
	}{
		{"empty summary", "some base", "", ""},
		{"contained summary", "prefix and more", "prefix and", ""},
		{"distinct summary", "prose text", "search anchor", "search anchor"},
		{"summary without base", "", "orphan summary", "orphan summary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LexicalSummaryText(tc.base, tc.short); got != tc.want {
				t.Errorf("LexicalSummaryText(%q, %q) = %q, want %q", tc.base, tc.short, got, tc.want)
			}
		})
	}
}

// lexicalDocGraph builds a real graph holding a collection container
// and one member item, returning (graph, container, item).
func lexicalDocGraph(t *testing.T) (*Graph, *Node, *Node) {
	t.Helper()
	g := New()
	coll := g.AddNode(Properties{
		"knowledge_type":         StringProperty("collection"),
		"collection_name":        StringProperty("Gramaton development"),
		"collection_description": StringProperty("backlog for the knowledge store"),
	})
	item := g.AddNode(Properties{
		"field.title":  StringProperty("fix the flaky test"),
		"field.status": StringProperty("open"),
	})
	if _, err := g.AddEdge(item.ID, coll.ID, "member_of", 1.0, nil); err != nil {
		t.Fatal(err)
	}
	return g, coll, item
}

func TestLexicalDocument_CollectionItemGetsContainerContext(t *testing.T) {
	g, _, item := lexicalDocGraph(t)
	got := LexicalDocument(g, item)
	for _, want := range []string{"Gramaton development", "backlog", "fix the flaky test", "open"} {
		if !strings.Contains(got, want) {
			t.Errorf("LexicalDocument missing %q; got %q", want, got)
		}
	}
}

func TestLexicalDocument_ContainerIndexesNameAndDescription(t *testing.T) {
	g, coll, _ := lexicalDocGraph(t)
	got := LexicalDocument(g, coll)
	for _, want := range []string{"Gramaton development", "backlog for the knowledge store"} {
		if !strings.Contains(got, want) {
			t.Errorf("LexicalDocument missing %q; got %q", want, got)
		}
	}
}

func TestLexicalDocument_PlainRecordEqualsRecordIndexText(t *testing.T) {
	g := New()
	n := g.AddNode(Properties{
		"content_full":  StringProperty("plain record body"),
		"content_short": StringProperty("distinct summary anchor"),
	})
	if got, want := LexicalDocument(g, n), RecordIndexText(n); got != want {
		t.Errorf("LexicalDocument = %q, want RecordIndexText passthrough %q", got, want)
	}
}

func TestLexicalDocument_NilGraphDegradesToRecordIndexText(t *testing.T) {
	n := memoryNode("body without graph context")
	if got, want := LexicalDocument(nil, n), RecordIndexText(n); got != want {
		t.Errorf("LexicalDocument(nil) = %q, want %q", got, want)
	}
}
