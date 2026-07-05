package dedup

import (
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

func dedupCfg() config.DedupConfig {
	return config.DedupConfig{SimilarityThreshold: 0.92, Action: "supersede"}
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

func TestCheckVecEmptyIndex(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	if id, sim := CheckVec(g, idx, dedupCfg(), []float32{1, 0, 0}, "content"); id != "" || sim != 0 {
		t.Fatalf("empty index returned %q/%.3f, want none", id, sim)
	}
}

func TestCheckVecNilVec(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	seedCandidate(g, idx, "existing content", []float32{1, 0, 0}, true)
	if id, sim := CheckVec(g, idx, dedupCfg(), nil, "content"); id != "" || sim != 0 {
		t.Fatalf("nil vec returned %q/%.3f, want none", id, sim)
	}
}

// TestCheckVecFindsShortDuplicate pins the pre-insert scan against a
// single existing record: with only one index entry (Check's own
// Len<2 guard would bail here) the pre-insert variant must still
// find the duplicate.
func TestCheckVecFindsShortDuplicate(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	seed := seedCandidate(g, idx, "short shared phrase", vec, true)

	id, sim := CheckVec(g, idx, dedupCfg(), vec, "short shared phrase")
	if id != seed.ID {
		t.Fatalf("duplicate = %q, want %q", id, seed.ID)
	}
	if sim < 0.99 {
		t.Fatalf("similarity %.3f, want ~1.0 for identical vectors", sim)
	}
}

// TestCheckVecJaccardRejectsLongDissimilar pins the textA threading
// through the refactored verifyJaccard: for >=200-char content,
// cosine-identical vectors with disjoint wording must be rejected by
// the Jaccard guard, using the CALLER-supplied content (a
// not-yet-inserted record has no node to read text from).
func TestCheckVecJaccardRejectsLongDissimilar(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	longA := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 5)
	longB := strings.Repeat("india juliett kilo lima mike november oscar papa ", 5)
	seedCandidate(g, idx, longA, vec, true)

	if id, _ := CheckVec(g, idx, dedupCfg(), vec, longB); id != "" {
		t.Fatalf("Jaccard guard failed: dissimilar long text matched %q", id)
	}
}

// TestCheckVecJaccardAcceptsLongIdentical is the positive complement:
// the same long text passes the Jaccard guard.
func TestCheckVecJaccardAcceptsLongIdentical(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	long := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 5)
	seed := seedCandidate(g, idx, long, vec, true)

	id, _ := CheckVec(g, idx, dedupCfg(), vec, long)
	if id != seed.ID {
		t.Fatalf("identical long text = %q, want %q", id, seed.ID)
	}
}

// TestCheckVecQuantizedFallback pins the fallback when the candidate
// has a vector-index entry but no stored float32 embedding (legacy
// records): similarity comes from the quantized index score.
func TestCheckVecQuantizedFallback(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	seed := seedCandidate(g, idx, "short legacy content", vec, false)

	id, sim := CheckVec(g, idx, dedupCfg(), vec, "short legacy content")
	if id != seed.ID {
		t.Fatalf("legacy candidate = %q, want %q", id, seed.ID)
	}
	if sim < 0.92 {
		t.Fatalf("quantized similarity %.3f below threshold; fallback broken", sim)
	}
}

// TestCheckSelfSkipPreserved pins the post-insert variant's semantics
// through the shared-core refactor: a node must never match itself,
// and with only itself in the index Check reports no duplicate.
func TestCheckSelfSkipPreserved(t *testing.T) {
	g := graph.New()
	idx := index.NewFlatIndex()
	vec := []float32{0.5, 0.5, 0.1}
	a := seedCandidate(g, idx, "self skip content", vec, true)
	b := seedCandidate(g, idx, "self skip content", vec, true)

	id, _ := Check(g, idx, dedupCfg(), a.ID)
	if id != b.ID {
		t.Fatalf("Check(a) = %q, want the sibling %q, never self", id, b.ID)
	}
}
