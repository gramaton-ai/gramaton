package core

import (
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// addTestConcept seeds a curation-shaped concept node.
func addTestConcept(t *testing.T, eng *Engine, keyword, content string) string {
	t.Helper()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":    graph.StringProperty(content),
		"content_short":   graph.StringProperty("Concept: " + keyword),
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty(keyword),
		"knowledge_type":  graph.StringProperty("conceptual"),
		"embedding_full":  graph.VectorProperty([]float32{1, 0, 0}),
		"created_at":      graph.TimestampProperty(time.Now().UTC()),
	})
	return n.ID
}

// TestConceptMintsNoChangelogVersions pins the derived-layer
// changelog boundary: concept creation, synthesis rewrites, and
// deletion all mint zero logical versions -- curation churn is not
// knowledge history.
func TestConceptMintsNoChangelogVersions(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	id := addTestConcept(t, eng, "resilience", "(template) resilience concept")
	if _, err := eng.Save("curation: concept emerge"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.SetContentProp(id, "content_full", "Resilience is the synthesized theme across members.")
	if _, err := eng.Save("curation: enrich concepts"); err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.Unlock()

	if got := eng.Changelog().Versions(id); len(got) != 0 {
		t.Fatalf("concept minted %d versions (%+v), want none", len(got), got)
	}

	err := eng.WithWriteBatch("delete", func(ws *WriteSession) (bool, error) {
		return true, ws.DeleteNode(id)
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := eng.Changelog().Versions(id); len(got) != 0 {
		t.Fatalf("concept deletion minted a tombstone (%+v), want none", got)
	}
}

// TestConceptOutOfPrimaryIndexesOnRebuild pins the index boundary
// across a reopen: the startup rebuild leaves concepts out of BM25
// and the vector index (this is also how legacy stores shed their
// pre-exclusion entries) while records index normally.
func TestConceptOutOfPrimaryIndexesOnRebuild(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	conceptID := addTestConcept(t, eng, "caching", "the caching concept with a distinctivetermone")
	record := eng.Graph().AddNode(graph.Properties{
		"content_full":   graph.StringProperty("a record with a distinctivetermtwo"),
		"embedding_full": graph.VectorProperty([]float32{0, 1, 0}),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	eng2.RLock()
	defer eng2.RUnlock()
	if hits := eng2.BM25Full().Search([]string{"distinctivetermone"}, 5, nil); len(hits) != 0 {
		t.Fatalf("concept text found in BM25 after rebuild: %+v", hits)
	}
	if hits := eng2.BM25Full().Search([]string{"distinctivetermtwo"}, 5, nil); len(hits) != 1 || hits[0].NodeID != record.ID {
		t.Fatalf("record text missing from BM25 after rebuild: %+v", hits)
	}
	if hits := eng2.VecIdx().Search([]float32{1, 0, 0}, 5, nil); len(hits) != 0 && hits[0].NodeID == conceptID && hits[0].Similarity > 0.99 {
		t.Fatalf("concept vector found in primary index after rebuild: %+v", hits)
	}
	// The concept node itself is a full graph citizen.
	if _, ok := eng2.Graph().GetNode(conceptID); !ok {
		t.Fatal("concept node lost across reopen")
	}
}

// TestConceptLiveIndexPathsSkipBM25 pins the two LIVE write paths
// (concept emergence via a write session's IndexNode, synthesis
// rewrite via SetContentProp): neither may land concept text in
// BM25. The rebuild-path pin above wouldn't catch a regression here
// -- these gates run long before any reopen.
func TestConceptLiveIndexPathsSkipBM25(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	// Emergence path: the curation write batch indexes the template.
	var conceptID string
	err := eng.WithWriteBatch("curation: concept emerge", func(ws *WriteSession) (bool, error) {
		n := ws.AddNode(graph.Properties{
			"content_full":    graph.StringProperty("Concept: durability with emergencetermalpha"),
			"node_type":       graph.StringProperty("concept"),
			"concept_keyword": graph.StringProperty("durability"),
		})
		conceptID = n.ID
		ws.IndexNode(n.ID, "Concept: durability with emergencetermalpha", nil)
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}
	if hits := eng.BM25Full().Search([]string{"emergencetermalpha"}, 5, nil); len(hits) != 0 {
		t.Fatalf("emergence path landed concept text in BM25: %+v", hits)
	}

	// Synthesis-rewrite path: SetContentProp on content_full.
	eng.Lock()
	eng.SetContentProp(conceptID, "content_full", "The synthesized theme with synthesistermbeta.")
	if _, err := eng.Save("curation: enrich concepts"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()
	if hits := eng.BM25Full().Search([]string{"synthesistermbeta"}, 5, nil); len(hits) != 0 {
		t.Fatalf("synthesis rewrite landed concept text in BM25: %+v", hits)
	}
}

// TestConceptSynthesisRewriteInWriteBatchSkipsBM25 pins the batched
// twin of the synthesis-rewrite gate. SetContentProp and its
// WriteSession counterpart write the same index, so a gate present on
// only one of them lets a batched curation pass reinstate exactly
// what the non-batched path refuses -- including shedding the entries
// a pre-exclusion store still carries.
func TestConceptSynthesisRewriteInWriteBatchSkipsBM25(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	var conceptID string
	err := eng.WithWriteBatch("curation: concept emerge", func(ws *WriteSession) (bool, error) {
		n := ws.AddNode(graph.Properties{
			"content_full":    graph.StringProperty("Concept: batching"),
			"node_type":       graph.StringProperty("concept"),
			"concept_keyword": graph.StringProperty("batching"),
		})
		conceptID = n.ID
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}

	// Plant the entry a store written before the exclusion would carry.
	eng.Lock()
	eng.BM25Full().Add(conceptID, "legacytermgamma")
	eng.Unlock()
	if hits := eng.BM25Full().Search([]string{"legacytermgamma"}, 5, nil); len(hits) != 1 {
		t.Fatalf("legacy BM25 entry did not take; the shed assertion would be vacuous: %+v", hits)
	}

	err = eng.WithWriteBatch("curation: enrich concepts", func(ws *WriteSession) (bool, error) {
		ws.SetContentProp(conceptID, "content_full", "The synthesized theme with batchsynthesistermdelta.")
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch rewrite: %v", err)
	}

	if hits := eng.BM25Full().Search([]string{"batchsynthesistermdelta"}, 5, nil); len(hits) != 0 {
		t.Fatalf("batched synthesis rewrite landed concept text in BM25: %+v", hits)
	}
	if hits := eng.BM25Full().Search([]string{"legacytermgamma"}, 5, nil); len(hits) != 0 {
		t.Fatalf("batched synthesis rewrite left the pre-exclusion entry behind: %+v", hits)
	}
	// The rewrite itself must still have landed on the node.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(conceptID)
	if !ok {
		t.Fatal("concept vanished")
	}
	if c, _ := n.Properties.GetString("content_full"); !strings.Contains(c, "batchsynthesistermdelta") {
		t.Fatalf("content_full = %q, want the rewritten synthesis text", c)
	}
}
