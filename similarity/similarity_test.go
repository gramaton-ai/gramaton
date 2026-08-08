package similarity

import (
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

func guardCfg() config.SaveGuardConfig {
	return config.SaveGuardConfig{SimilarHoldThreshold: 0.92, AdvisoryThreshold: 0.85}
}

// seedCandidate inserts a node with content and (optionally) a stored
// float32 embedding, and registers vec in the vector index.
func seedCandidate(g *graph.Graph, idx *index.FlatIndex, content string, vec []float32, storeFloat32 bool) *graph.Node {
	props := graph.Properties{"content_full": graph.StringProperty(content)}
	if storeFloat32 {
		props["embedding_full"] = graph.VectorProperty(vec)
	}
	n := g.AddNode(props)
	idx.Add(n.ID, vec)
	return n
}

func TestScanEmptyIndex(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	if out := Scan(g, idx, guardCfg(), []float32{1, 0, 0}, "content", ""); out.Hold != nil || out.Advisory != nil {
		t.Fatalf("empty index returned %+v, want none", out)
	}
}

func TestScanNilVec(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	seedCandidate(g, idx, "existing content", []float32{1, 0, 0}, true)
	if out := Scan(g, idx, guardCfg(), nil, "content", ""); out.Hold != nil || out.Advisory != nil {
		t.Fatalf("nil vec returned %+v, want none", out)
	}
}

// TestScanHoldsShortDuplicate pins the pre-insert scan against a
// single existing record: with only one index entry the scan must
// still find and hold the duplicate.
func TestScanHoldsShortDuplicate(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	seed := seedCandidate(g, idx, "short shared phrase", vec, true)

	out := Scan(g, idx, guardCfg(), vec, "short shared phrase", "")
	if out.Hold == nil || out.Hold.NodeID != seed.ID {
		t.Fatalf("hold = %+v, want %q", out.Hold, seed.ID)
	}
	if out.Hold.Similarity < 0.99 {
		t.Fatalf("similarity %.3f, want ~1.0 for identical vectors", out.Hold.Similarity)
	}
	if out.Advisory != nil {
		t.Fatalf("a hold must suppress the advisory, got %+v", out.Advisory)
	}
}

// TestScanJaccardRejectsLongDissimilar pins the textA threading
// through the refactored verifyJaccard: for >=200-char content,
// cosine-identical vectors with disjoint wording must be rejected by
// the Jaccard guard, using the CALLER-supplied content (a
// not-yet-inserted record has no node to read text from).
func TestScanJaccardRejectsLongDissimilar(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	longA := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 5)
	longB := strings.Repeat("india juliett kilo lima mike november oscar papa ", 5)
	seedCandidate(g, idx, longA, vec, true)

	out := Scan(g, idx, guardCfg(), vec, longB, "")
	if out.Hold != nil {
		t.Fatalf("Jaccard guard failed: dissimilar long text held against %+v", out.Hold)
	}
	// The cosine-identical, Jaccard-failed candidate degrades to an
	// advisory (structurally similar, unverified) rather than a hold.
	if out.Advisory == nil {
		t.Fatal("expected an advisory for a hold-level cosine that failed Jaccard")
	}
}

// TestScanJaccardAcceptsLongIdentical is the positive complement:
// the same long text passes the Jaccard guard.
func TestScanJaccardAcceptsLongIdentical(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	long := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 5)
	seed := seedCandidate(g, idx, long, vec, true)

	out := Scan(g, idx, guardCfg(), vec, long, "")
	if out.Hold == nil || out.Hold.NodeID != seed.ID {
		t.Fatalf("identical long text = %+v, want hold on %q", out.Hold, seed.ID)
	}
}

// TestScanQuantizedFallback pins the fallback when the candidate
// has a vector-index entry but no stored float32 embedding (legacy
// records): similarity comes from the quantized index score.
func TestScanQuantizedFallback(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	seed := seedCandidate(g, idx, "short legacy content", vec, false)

	out := Scan(g, idx, guardCfg(), vec, "short legacy content", "")
	if out.Hold == nil || out.Hold.NodeID != seed.ID {
		t.Fatalf("legacy candidate = %+v, want hold on %q", out.Hold, seed.ID)
	}
	if out.Hold.Similarity < 0.92 {
		t.Fatalf("quantized similarity %.3f below threshold; fallback broken", out.Hold.Similarity)
	}
}

// TestScanSelfSkipPreserved pins the post-insert variant's semantics
// through the shared-core refactor: a node must never match itself,
// and with only itself in the index Check reports no duplicate.
func TestScanSelfSkipPreserved(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	a := seedCandidate(g, idx, "self skip content", vec, true)
	b := seedCandidate(g, idx, "self skip content", vec, true)

	vecA, contentA := NodeEmbeddingAndContent(a)
	out := Scan(g, idx, guardCfg(), vecA, contentA, a.ID)
	if out.Hold == nil || out.Hold.NodeID != b.ID {
		t.Fatalf("Scan(a, selfID=a) = %+v, want the sibling %q, never self", out.Hold, b.ID)
	}
}

// TestScanExcludesSectionAndChunkChildren pins the machine-derived
// exclusion for long-document children: a cosine-identical section
// or chunk child must produce neither a hold nor an advisory -- a
// save can never be held against a fragment its author cannot
// meaningfully update.
func TestScanExcludesSectionAndChunkChildren(t *testing.T) {
	for _, nt := range []string{"section", "chunk"} {
		g := graph.New()
		idx := index.NewFlatIndex()
		vec := []float32{0.5, 0.5, 0.1}
		n := g.AddNode(graph.Properties{
			"content_full":   graph.StringProperty("identical text for the exclusion test"),
			"embedding_full": graph.VectorProperty(vec),
			"node_type":      graph.StringProperty(nt),
		})
		idx.Add(n.ID, vec)

		out := Scan(g, idx, guardCfg(), vec, "identical text for the exclusion test", "")
		if out.Hold != nil || out.Advisory != nil {
			t.Fatalf("node_type=%s candidate produced %+v, want no hold and no advisory", nt, out)
		}
	}
}
