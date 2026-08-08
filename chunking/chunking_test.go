package chunking

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
)

// fakeEmbedder is a deterministic embed.Provider for unit tests.
type fakeEmbedder struct {
	window int
	calls  int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = []float32{float32(len(texts[i])), 1}
	}
	return vecs, nil
}
func (f *fakeEmbedder) ModelID() string    { return "fake-model" }
func (f *fakeEmbedder) ContextWindow() int { return f.window }

// fakeApplier records Apply's writes without an engine.
type fakeApplier struct {
	nodes   map[string]graph.Properties
	edges   []graph.Edge
	vecs    map[string][]float32
	indexed map[string]string
	seq     int
}

func newFakeApplier() *fakeApplier {
	return &fakeApplier{
		nodes:   make(map[string]graph.Properties),
		vecs:    make(map[string][]float32),
		indexed: make(map[string]string),
	}
}

func (f *fakeApplier) AddNode(p graph.Properties) *graph.Node {
	f.seq++
	id := fmt.Sprintf("child-%02d", f.seq)
	f.nodes[id] = p
	return &graph.Node{ID: id, Properties: p}
}

func (f *fakeApplier) AddEdge(sourceID, targetID, edgeType string, weight float64, _ graph.Properties) (*graph.Edge, error) {
	e := graph.Edge{SourceID: sourceID, TargetID: targetID, Type: edgeType}
	f.edges = append(f.edges, e)
	return &e, nil
}

func (f *fakeApplier) IndexNode(nodeID, content string, vec []float32) {
	f.indexed[nodeID] = content
	if vec != nil {
		f.vecs[nodeID] = vec
	}
}

func (f *fakeApplier) SetProp(nodeID, key string, val graph.Property) {
	if p, ok := f.nodes[nodeID]; ok {
		p[key] = val
		return
	}
	f.nodes[nodeID] = graph.Properties{key: val}
}

func (f *fakeApplier) AddVector(nodeID string, vec []float32) {
	f.vecs[nodeID] = vec
}

func testCfg() config.ChunkingConfig {
	return config.ChunkingConfig{
		Threshold:  8000,
		ChunkSize:  512,
		Overlap:    128,
		SectionMin: 500,
		SectionMax: 5000,
	}
}

// TestTriggerThresholdGoverns pins the raised character threshold as
// the user-facing trigger: content at or below it never chunks, even
// though it exceeds the embedding window.
func TestTriggerThresholdGoverns(t *testing.T) {
	emb := &fakeEmbedder{window: 512}
	cfg := testCfg()
	ecfg := config.EmbeddingConfig{}

	if Trigger(8000, emb, cfg, ecfg) {
		t.Fatal("content at the threshold must not trigger")
	}
	if !Trigger(8001, emb, cfg, ecfg) {
		t.Fatal("content above the threshold must trigger")
	}
	// A threshold below the window floor is clamped up: chunking
	// content that fits one embedding is pure overhead.
	cfg.Threshold = 100
	if Trigger(1500, emb, cfg, ecfg) {
		t.Fatal("window floor (512*3=1536) must govern when threshold is below it")
	}
	if !Trigger(1600, emb, cfg, ecfg) {
		t.Fatal("above the window floor must trigger when threshold is lower")
	}
}

// TestPreChunkBelowThresholdReturnsNil pins the gate at the config
// threshold, not the old window-derived 1536 chars.
func TestPreChunkBelowThresholdReturnsNil(t *testing.T) {
	emb := &fakeEmbedder{window: 512}
	content := strings.Repeat("word ", 1000) // 5000 chars: above window floor, below 8000
	if pre := PreChunk(context.Background(), emb, testCfg(), config.EmbeddingConfig{}, content, ""); pre != nil {
		t.Fatal("content below chunking.threshold must not chunk")
	}
}

// TestApplyStampsChildIdentity verifies both child kinds carry the
// machine-derived discriminator, ordinal, creation time, and the
// parent's inherited metadata -- the properties every exclusion
// surface (save guard, duplicates, curation, changelog) keys on.
func TestApplyStampsChildIdentity(t *testing.T) {
	emb := &fakeEmbedder{window: 512}
	parentProps := graph.Properties{
		"temporality": graph.StringProperty("durable"),
		"confidence":  graph.Float64Property(0.9),
		"author":      graph.StringProperty("Ada Lovelace <ada@example.com>"),
	}

	cases := []struct {
		name     string
		content  string
		wantType string
		wantEdge string
	}{
		{
			name:     "sections",
			content:  structuredContent(),
			wantType: "section",
			wantEdge: "section_of",
		},
		{
			name:     "chunks",
			content:  strings.Repeat("plain unstructured prose without any headings at all ", 200),
			wantType: "chunk",
			wantEdge: "chunk_of",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pre := PreChunk(context.Background(), emb, testCfg(), config.EmbeddingConfig{}, tc.content, "a summary")
			if pre == nil {
				t.Fatalf("PreChunk returned nil for %d chars", len(tc.content))
			}
			if tc.wantType == "section" && len(pre.Sections) == 0 {
				t.Fatal("expected structural sections")
			}
			if tc.wantType == "chunk" && len(pre.Texts) == 0 {
				t.Fatal("expected dumb chunks")
			}

			a := newFakeApplier()
			created := Apply(a, "parent-1", pre, parentProps)
			n := len(created)
			if n == 0 {
				t.Fatal("Apply created no children")
			}

			ordinals := map[int64]bool{}
			for id, props := range a.nodes {
				if id == "parent-1" {
					continue
				}
				nt, _ := props.GetString("node_type")
				if nt != tc.wantType {
					t.Fatalf("child %s node_type = %q, want %q", id, nt, tc.wantType)
				}
				if _, ok := props["created_at"]; !ok {
					t.Fatalf("child %s missing created_at", id)
				}
				idx, ok := props.GetInt64("section_index")
				if !ok || idx < 1 || idx > int64(n) {
					t.Fatalf("child %s section_index = %d (ok=%v), want 1..%d", id, idx, ok, n)
				}
				ordinals[idx] = true
				for _, key := range []string{"temporality", "confidence", "author"} {
					if _, ok := props[key]; !ok {
						t.Fatalf("child %s missing inherited %s", id, key)
					}
				}
				// Never inherited: an unclassified parent must not
				// flood the pending queue with unclassifiable children.
				if ps, _ := props.GetString("processing_status"); ps != "processed" {
					t.Fatalf("child %s processing_status = %q, want processed", id, ps)
				}
			}
			if len(ordinals) != n {
				t.Fatalf("ordinals not unique: %d distinct for %d children", len(ordinals), n)
			}

			edgeCount := 0
			for _, e := range a.edges {
				if e.Type != tc.wantEdge || e.TargetID != "parent-1" {
					t.Fatalf("unexpected edge %+v", e)
				}
				edgeCount++
			}
			if edgeCount != n {
				t.Fatalf("expected %d %s edges, got %d", n, tc.wantEdge, edgeCount)
			}

			// Parent embedding replaced via SetProp + AddVector.
			if pre.ParentVec == nil {
				t.Fatal("expected a ParentVec from the fake embedder")
			}
			if _, ok := a.vecs["parent-1"]; !ok {
				t.Fatal("parent vector not written via AddVector")
			}
			pp := a.nodes["parent-1"]
			if _, ok := pp["embedding_full"]; !ok {
				t.Fatal("parent embedding_full not written via SetProp")
			}
		})
	}
}

// TestResultEmbedded pins the fail-closed helper the update path
// relies on.
func TestResultEmbedded(t *testing.T) {
	if (&Result{Texts: []string{"a", "b"}}).Embedded() {
		t.Fatal("no vectors must report unembedded")
	}
	if !(&Result{Texts: []string{"a"}, Vectors: [][]float32{{1}}}).Embedded() {
		t.Fatal("full vectors must report embedded")
	}
	if (&Result{}).Embedded() {
		t.Fatal("empty result must report unembedded")
	}
	var nilResult *Result
	if nilResult.Embedded() {
		t.Fatal("nil result must report unembedded")
	}
}

func structuredContent() string {
	var sb strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sb, "## Heading %d\n\n", i+1)
		for j := 0; j < 45; j++ {
			sb.WriteString("A sentence of section body for the identity test. ")
		}
		sb.WriteString("\n\n")
	}
	return sb.String()
}
