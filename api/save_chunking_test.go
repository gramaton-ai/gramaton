package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// longStructuredContent builds markdown content above the chunking
// threshold with detectable section structure.
func longStructuredContent(threshold int) string {
	var sb strings.Builder
	section := 0
	for sb.Len() <= threshold {
		section++
		fmt.Fprintf(&sb, "## Section %d\n\n", section)
		for j := 0; j < 40; j++ {
			sb.WriteString("A sentence of body prose for the long-document chunking test. ")
		}
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// sectionChildren returns the IDs of section/chunk children attached
// to parentID.
func sectionChildren(t *testing.T, a *API, parentID string) []string {
	t.Helper()
	a.engine.RLock()
	defer a.engine.RUnlock()
	var ids []string
	for _, e := range a.engine.Graph().EdgesTo(parentID) {
		if e.Type != "section_of" && e.Type != "chunk_of" {
			continue
		}
		ids = append(ids, e.SourceID)
	}
	return ids
}

// TestSaveLongContentCreatesChildren pins the save-path wiring: a
// save above chunking.threshold sprouts section/chunk children that
// carry the machine-derived identity, while an ordinary save creates
// none.
func TestSaveLongContentCreatesChildren(t *testing.T) {
	a, eng := setupTestAPI(t)
	threshold := eng.Config().Chunking.Threshold

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	if resp.Held != nil {
		t.Fatalf("unexpected hold: %+v", resp.Held)
	}

	children := sectionChildren(t, a, resp.ID)
	if len(children) == 0 {
		t.Fatal("long save created no section/chunk children")
	}
	a.engine.RLock()
	for _, id := range children {
		child, ok := a.engine.Graph().GetNode(id)
		if !ok {
			t.Fatalf("child %s missing", id)
		}
		if !graph.IsSectionOrChunk(child.Properties) {
			nt, _ := child.Properties.GetString("node_type")
			t.Fatalf("child %s node_type = %q, want section or chunk", id, nt)
		}
	}
	a.engine.RUnlock()

	short, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "an ordinary record well below the chunking threshold",
	})
	if apiErr != nil {
		t.Fatalf("short Save: %v", apiErr)
	}
	if got := sectionChildren(t, a, short.ID); len(got) != 0 {
		t.Fatalf("short save created %d children, want 0", len(got))
	}
}

// TestSaveChunkChildrenSurviveCommit pins persistence: children ride
// the same commit as the parent and reload from disk.
func TestSaveChunkChildrenSurviveCommit(t *testing.T) {
	a, eng := setupTestAPI(t)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(eng.Config().Chunking.Threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	before := len(sectionChildren(t, a, resp.ID))
	if before == 0 {
		t.Fatal("no children created")
	}

	// A later unrelated commit must not disturb the children.
	if _, apiErr := a.Save(context.Background(), SaveRequest{Content: "unrelated follow-up record"}); apiErr != nil {
		t.Fatalf("follow-up Save: %v", apiErr)
	}
	if after := len(sectionChildren(t, a, resp.ID)); after != before {
		t.Fatalf("children changed across commits: %d -> %d", before, after)
	}
}

// TestUpdateContentRechunks pins the re-chunk lifecycle: a content
// replace deletes the stale children and derives a fresh set from
// the new content.
func TestUpdateContentRechunks(t *testing.T) {
	a, eng := setupTestAPI(t)
	threshold := eng.Config().Chunking.Threshold

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	oldChildren := sectionChildren(t, a, resp.ID)
	if len(oldChildren) == 0 {
		t.Fatal("no children created on save")
	}

	// Replace with different long content: children must be a fresh set.
	updated, apiErr := a.Update(context.Background(), UpdateRequest{
		ID:      resp.ID,
		Content: longStructuredContent(threshold + 4000),
	})
	if apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}
	if !updated.Updated {
		t.Fatal("update reported not-updated")
	}

	newChildren := sectionChildren(t, a, resp.ID)
	if len(newChildren) == 0 {
		t.Fatal("re-chunk created no children")
	}
	oldSet := map[string]bool{}
	for _, id := range oldChildren {
		oldSet[id] = true
	}
	a.engine.RLock()
	defer a.engine.RUnlock()
	for _, id := range newChildren {
		if oldSet[id] {
			t.Fatalf("stale child %s survived the re-chunk", id)
		}
	}
	for _, id := range oldChildren {
		if _, ok := a.engine.Graph().GetNode(id); ok {
			t.Fatalf("old child %s still exists after re-chunk", id)
		}
	}
}

// TestUpdateShrinkBelowThresholdDeletesChildren pins the downward
// crossing: children are cleared even when no re-chunk follows.
func TestUpdateShrinkBelowThresholdDeletesChildren(t *testing.T) {
	a, eng := setupTestAPI(t)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(eng.Config().Chunking.Threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	if len(sectionChildren(t, a, resp.ID)) == 0 {
		t.Fatal("no children created on save")
	}

	if _, apiErr := a.Update(context.Background(), UpdateRequest{
		ID:      resp.ID,
		Content: "a short consolidating rewrite",
	}); apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}
	if got := sectionChildren(t, a, resp.ID); len(got) != 0 {
		t.Fatalf("children survived a shrink below the threshold: %d", len(got))
	}
}

// TestUpdateGrowAboveThresholdCreatesChildren pins the upward
// crossing via content_append on a previously unchunked record.
func TestUpdateGrowAboveThresholdCreatesChildren(t *testing.T) {
	a, eng := setupTestAPI(t)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "## Seed\n\na short record that will grow",
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	if len(sectionChildren(t, a, resp.ID)) != 0 {
		t.Fatal("short save unexpectedly chunked")
	}

	if _, apiErr := a.Update(context.Background(), UpdateRequest{
		ID:            resp.ID,
		ContentAppend: longStructuredContent(eng.Config().Chunking.Threshold),
	}); apiErr != nil {
		t.Fatalf("Update append: %v", apiErr)
	}
	if got := sectionChildren(t, a, resp.ID); len(got) == 0 {
		t.Fatal("append across the threshold created no children")
	}
}

// TestUpdateRefusesSectionChild pins the machine-owned refusal: a
// child ID is not a valid update/classify/resolve target.
func TestUpdateRefusesSectionChild(t *testing.T) {
	a, eng := setupTestAPI(t)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(eng.Config().Chunking.Threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	children := sectionChildren(t, a, resp.ID)
	if len(children) == 0 {
		t.Fatal("no children created")
	}
	child := children[0]

	if _, apiErr := a.Update(context.Background(), UpdateRequest{ID: child, Content: "overwrite attempt"}); apiErr == nil {
		t.Fatal("Update accepted a section child")
	}
	if _, apiErr := a.Classify(context.Background(), ClassifyRequest{ID: child, Temporality: "durable"}); apiErr == nil {
		t.Fatal("Classify accepted a section child")
	}
	if _, apiErr := a.Resolve(context.Background(), ResolveRequest{ID: child, Resolution: "obsolete"}); apiErr == nil {
		t.Fatal("Resolve accepted a section child")
	}
}

// TestSearchFoldsSectionHitToParent pins the fold-up contract end to
// end: a query matching one section of a long document returns the
// PARENT record's ID carrying matched-section provenance, and child
// ULIDs never appear as result identities.
func TestSearchFoldsSectionHitToParent(t *testing.T) {
	a, eng := setupTestAPI(t)
	threshold := eng.Config().Chunking.Threshold

	// A long document whose final section carries a unique token no
	// other record shares.
	var sb strings.Builder
	sb.WriteString(longStructuredContent(threshold))
	sb.WriteString("## Zanzibar appendix\n\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("The zanzibar protocol governs the appendix payload. ")
	}
	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: sb.String()})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	children := sectionChildren(t, a, resp.ID)
	if len(children) == 0 {
		t.Fatal("no children created")
	}
	childSet := map[string]bool{}
	for _, id := range children {
		childSet[id] = true
	}

	res, apiErr := a.Search(context.Background(), SearchRequest{Text: "zanzibar protocol", Top: 10})
	if apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	if len(res.Results) == 0 {
		t.Fatal("no results for a token present in a section")
	}

	foundParent := false
	for _, r := range res.Results {
		if childSet[r.ID] {
			t.Fatalf("child %s surfaced as a result identity", r.ID)
		}
		if r.ID != resp.ID {
			continue
		}
		foundParent = true
		if r.MatchedSectionID == "" || !childSet[r.MatchedSectionID] {
			t.Fatalf("folded row lacks child provenance: matched_section_id=%q", r.MatchedSectionID)
		}
		if r.MatchedSection == "" {
			t.Fatal("folded row lacks a matched_section label")
		}
	}
	if !foundParent {
		t.Fatalf("parent %s not in results: %+v", resp.ID, res.Results)
	}
}

// TestRandomSearchExcludesChildren pins the random-mode leak: the
// sample must contain documents, never fragment ULIDs.
func TestRandomSearchExcludesChildren(t *testing.T) {
	a, eng := setupTestAPI(t)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(eng.Config().Chunking.Threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	children := sectionChildren(t, a, resp.ID)
	if len(children) == 0 {
		t.Fatal("no children created")
	}
	childSet := map[string]bool{}
	for _, id := range children {
		childSet[id] = true
	}

	res, apiErr := a.Search(context.Background(), SearchRequest{Random: true, Top: 50})
	if apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	for _, r := range res.Results {
		if childSet[r.ID] {
			t.Fatalf("random search leaked child %s", r.ID)
		}
	}
}

// TestFoldRechecksParentAgainstFilters pins the filter-bypass leak: a
// child whose inherited metadata (frozen at chunk time) passes the
// query filter must not smuggle in a parent that no longer does.
func TestFoldRechecksParentAgainstFilters(t *testing.T) {
	a, eng := setupTestAPI(t)

	var sb strings.Builder
	sb.WriteString(longStructuredContent(eng.Config().Chunking.Threshold))
	sb.WriteString("## Xylophone appendix\n\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("The xylophone calibration procedure lives here. ")
	}
	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     sb.String(),
		Temporality: "durable",
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	if len(sectionChildren(t, a, resp.ID)) == 0 {
		t.Fatal("no children created")
	}

	// Metadata-only update: the parent moves to temporal, children
	// keep their frozen durable inheritance.
	if _, apiErr := a.Update(context.Background(), UpdateRequest{
		ID: resp.ID, Temporality: "temporal",
	}); apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}

	res, apiErr := a.Search(context.Background(), SearchRequest{
		Text: "xylophone calibration", Temporality: "durable", Top: 10,
	})
	if apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	for _, r := range res.Results {
		if r.ID == resp.ID {
			t.Fatalf("temporal parent leaked into a temporality=durable query via its durable-stamped child")
		}
	}

	// The unfiltered query still folds it in.
	res, apiErr = a.Search(context.Background(), SearchRequest{Text: "xylophone calibration", Top: 10})
	if apiErr != nil {
		t.Fatalf("unfiltered Search: %v", apiErr)
	}
	found := false
	for _, r := range res.Results {
		if r.ID == resp.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("unfiltered query no longer folds the parent in")
	}
}

// chunkTestEmbedder is a deterministic embed.Provider whose failure
// mode is switchable mid-test.
type chunkTestEmbedder struct{ fail bool }

func (e *chunkTestEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.fail {
		return nil, fmt.Errorf("injected embed outage")
	}
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = []float32{float32(len(texts[i]) % 97), 1, 0.5}
	}
	return vecs, nil
}
func (e *chunkTestEmbedder) ModelID() string    { return "chunk-test-model" }
func (e *chunkTestEmbedder) ContextWindow() int { return 512 }

func setupChunkAPIWithEmbedder(t *testing.T, emb *chunkTestEmbedder) (*API, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Dir = t.TempDir() + "/backups"
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithEmbedder(emb),
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	a := New(Dependencies{Engine: eng, Log: slog.Default(), ConfigDir: dir})
	t.Cleanup(a.StopPreparedSweeper)
	return a, eng
}

// TestSaveChunksCarryVectors pins the embedder-backed path: children
// get their own embedding_full and the parent's vector is the
// purpose-sized replacement, stamped with the model.
func TestSaveChunksCarryVectors(t *testing.T) {
	emb := &chunkTestEmbedder{}
	a, eng := setupChunkAPIWithEmbedder(t, emb)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content: longStructuredContent(eng.Config().Chunking.Threshold),
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	children := sectionChildren(t, a, resp.ID)
	if len(children) == 0 {
		t.Fatal("no children created")
	}

	a.engine.RLock()
	defer a.engine.RUnlock()
	for _, id := range children {
		child, _ := a.engine.Graph().GetNode(id)
		if _, ok := child.Properties.GetVector("embedding_full"); !ok {
			t.Fatalf("child %s has no embedding_full", id)
		}
		if m, _ := child.Properties.GetString("embedding_model"); m != "chunk-test-model" {
			t.Fatalf("child %s embedding_model = %q", id, m)
		}
	}
	parent, _ := a.engine.Graph().GetNode(resp.ID)
	if _, ok := parent.Properties.GetVector("embedding_full"); !ok {
		t.Fatal("parent lost its embedding")
	}
}

// TestUpdateRechunkFailsClosedOnEmbedOutage pins the update path's
// extended fail-closed contract: a degraded prechunk (embed outage)
// rejects the update before any mutation.
func TestUpdateRechunkFailsClosedOnEmbedOutage(t *testing.T) {
	emb := &chunkTestEmbedder{}
	a, eng := setupChunkAPIWithEmbedder(t, emb)

	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:      "seed record",
		SummaryShort: "seed summary",
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	emb.fail = true
	long := longStructuredContent(eng.Config().Chunking.Threshold)
	_, apiErr = a.Update(context.Background(), UpdateRequest{ID: resp.ID, Content: long})
	if apiErr == nil {
		t.Fatal("update with a failing embedder must fail closed")
	}

	// Nothing mutated: content unchanged, no children.
	a.engine.RLock()
	n, _ := a.engine.Graph().GetNode(resp.ID)
	content, _ := n.Properties.GetString("content_full")
	a.engine.RUnlock()
	if content != "seed record" {
		t.Fatalf("content mutated on a failed update: %q", content[:40])
	}
	if got := sectionChildren(t, a, resp.ID); len(got) != 0 {
		t.Fatalf("failed update created %d children", len(got))
	}

	// Outage over: the same update succeeds and chunks.
	emb.fail = false
	if _, apiErr := a.Update(context.Background(), UpdateRequest{ID: resp.ID, Content: long}); apiErr != nil {
		t.Fatalf("post-outage update: %v", apiErr)
	}
	if got := sectionChildren(t, a, resp.ID); len(got) == 0 {
		t.Fatal("post-outage update did not chunk")
	}
}
