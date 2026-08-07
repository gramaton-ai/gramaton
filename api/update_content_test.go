package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// erroringEmbedder always fails -- for pinning that content updates
// fail closed when the vector cannot be produced.
type erroringEmbedder struct{}

func (erroringEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return nil, errors.New("simulated embedder outage")
}
func (erroringEmbedder) ModelID() string    { return "erroring-embedder" }
func (erroringEmbedder) ContextWindow() int { return 512 }

// TestUpdateContentReplace pins the expanded update: content is
// replaced, the prior content is echoed, updated_at is stamped, the
// version token changes, and the record re-embeds (no summary, so the
// vector is content-derived).
func TestUpdateContentReplace(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	const before = "the original decision was to use protocol alpha"
	const after = "the revised decision is to use protocol beta because alpha failed under load"
	saved, apiErr := a.Save(ctx, SaveRequest{Content: before})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}

	resp, apiErr := a.Update(ctx, UpdateRequest{ID: saved.ID, Content: after})
	if apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}
	if !resp.Updated {
		t.Fatal("expected updated=true")
	}
	if resp.PreviousContent != before {
		t.Fatalf("previous_content = %q, want the prior content", resp.PreviousContent)
	}
	if resp.Version == "" {
		t.Fatal("expected a version token")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(saved.ID)
	if c, _ := n.Properties.GetString("content_full"); c != after {
		t.Fatalf("content_full = %q, want the new content", c)
	}
	if _, ok := n.Properties.GetTimestamp("updated_at"); !ok {
		t.Error("updated_at not stamped on content change")
	}
	if _, ok := n.Properties.GetVector("embedding_full"); !ok {
		t.Error("content change without summary must re-embed")
	}
}

// TestUpdateContentAppend pins append semantics: content grows with
// the separator, and once cumulative appends pass the
// summary-refresh ratio the record is flagged (the summary is the
// vector anchor and no longer represents the content).
func TestUpdateContentAppend(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	const base = "reference list of approved suppliers: alpha corp"
	saved, apiErr := a.Save(ctx, SaveRequest{
		Content:      base,
		SummaryShort: "approved supplier reference list",
	})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}

	addition := "beta industries approved after the second audit " + strings.Repeat("with satisfactory findings ", 4)
	resp, apiErr := a.Update(ctx, UpdateRequest{ID: saved.ID, ContentAppend: addition})
	if apiErr != nil {
		t.Fatalf("append update: %v", apiErr)
	}
	if !resp.Updated {
		t.Fatal("expected updated=true")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(saved.ID)
	c, _ := n.Properties.GetString("content_full")
	if c != base+contentAppendSeparator+addition {
		t.Fatalf("content_full = %q, want base+separator+addition", c)
	}
	if _, ok := n.Properties.GetInt64("appended_since_summary"); !ok {
		t.Error("appended_since_summary bookkeeping missing")
	}
	// The addition is large relative to the content: the refresh flag
	// must be set and the response must have said so.
	if pending, _ := n.Properties.GetBool("summary_refresh_pending"); !pending {
		t.Error("summary_refresh_pending not set past the append ratio")
	}
	found := false
	for _, note := range resp.Notes {
		if strings.Contains(note, "summary") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a summary-refresh note, got %v", resp.Notes)
	}
}

// TestUpdateExpectedVersion pins optimistic concurrency: a stale
// token applies nothing and returns the current content; the returned
// token then lets the retry through.
func TestUpdateExpectedVersion(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	const before = "concurrency seed content for the version token"
	saved, apiErr := a.Save(ctx, SaveRequest{Content: before})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}

	conflicted, apiErr := a.Update(ctx, UpdateRequest{
		ID:              saved.ID,
		Content:         "should not apply",
		ExpectedVersion: "stale-token-1",
	})
	if apiErr != nil {
		t.Fatalf("stale update: %v", apiErr)
	}
	if conflicted.VersionConflict == nil {
		t.Fatal("expected version_conflict for a stale token")
	}
	if conflicted.VersionConflict.CurrentContent != before {
		t.Fatalf("conflict must echo current content, got %q", conflicted.VersionConflict.CurrentContent)
	}
	eng.RLock()
	n, _ := eng.Graph().GetNode(saved.ID)
	c, _ := n.Properties.GetString("content_full")
	eng.RUnlock()
	if c != before {
		t.Fatalf("conflicted update mutated content: %q", c)
	}

	applied, apiErr := a.Update(ctx, UpdateRequest{
		ID:              saved.ID,
		Content:         "applied with the correct token",
		ExpectedVersion: conflicted.VersionConflict.CurrentVersion,
	})
	if apiErr != nil {
		t.Fatalf("retry update: %v", apiErr)
	}
	if applied.VersionConflict != nil || !applied.Updated {
		t.Fatalf("retry with current token must apply, got %+v", applied)
	}
}

// TestUpdateEmbedFailureFailsClosed pins that a content change whose
// re-embed fails applies NOTHING -- content and vectors move
// together, so a failed vector never leaves a silently stale
// embedding behind.
func TestUpdateEmbedFailureFailsClosed(t *testing.T) {
	a, eng := setupReembedAPI(t, core.WithEmbedder(erroringEmbedder{}), nil)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	ctx := context.Background()

	// Save succeeds with an embedding warning (capture is tolerant).
	const before = "record created during an embedder outage"
	saved, apiErr := a.Save(ctx, SaveRequest{Content: before})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}

	// A content update (no summary -> needs a vector) must fail closed.
	_, apiErr = a.Update(ctx, UpdateRequest{ID: saved.ID, Content: "replacement content"})
	if apiErr == nil {
		t.Fatal("expected the update to fail when embedding fails")
	}
	if apiErr.Code != "unavailable" {
		t.Fatalf("code = %q, want unavailable", apiErr.Code)
	}
	eng.RLock()
	n, _ := eng.Graph().GetNode(saved.ID)
	c, _ := n.Properties.GetString("content_full")
	eng.RUnlock()
	if c != before {
		t.Fatalf("failed update mutated content: %q", c)
	}
}

// TestUpdateContentDeletesObservationChildren pins the observation
// lifecycle: a content change deletes the record's observation
// children (they assert the OLD content verbatim; curation
// re-extracts from the new content).
func TestUpdateContentDeletesObservationChildren(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	saved, apiErr := a.Save(ctx, SaveRequest{Content: "parent record with an observation child attached"})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}

	eng.Lock()
	obs := eng.Graph().AddNode(graph.Properties{
		"node_type":     graph.StringProperty("observation"),
		"content_short": graph.StringProperty("old key sentence"),
	})
	if _, err := eng.Graph().AddEdge(obs.ID, saved.ID, "observation_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge: %v", err)
	}
	if _, err := eng.Save("test seed: observation child"); err != nil {
		eng.Unlock()
		t.Fatalf("engine.Save: %v", err)
	}
	eng.Unlock()

	if _, apiErr := a.Update(ctx, UpdateRequest{ID: saved.ID, Content: "revised parent content"}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(obs.ID); ok {
		t.Error("observation child survived a content change; must be deleted for re-extraction")
	}
}

// TestUpdateContentReopensConflicts pins the conflict-reopening
// contract: a content change deletes the record's contradicts edges
// and clears contested on it, clears contested on a peer left with no
// remaining conflicts, and leaves contested on a peer that still
// conflicts with a third record.
func TestUpdateContentReopensConflicts(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	ctx := context.Background()

	save := func(content string) string {
		t.Helper()
		resp, apiErr := a.Save(ctx, SaveRequest{Content: content})
		if apiErr != nil {
			t.Fatalf("save %q: %v", content, apiErr)
		}
		return resp.ID
	}
	idA := save("record A claims the deployment uses blue-green rollout")
	idB := save("record B claims the deployment is done in place")
	idC := save("record C claims deployments happen only on Fridays")

	eng.Lock()
	if _, err := eng.Graph().AddEdge(idA, idB, "contradicts", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge A-B: %v", err)
	}
	if _, err := eng.Graph().AddEdge(idB, idC, "contradicts", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge B-C: %v", err)
	}
	for _, id := range []string{idA, idB, idC} {
		eng.SetProp(id, "epistemic_status", graph.StringProperty("contested"))
	}
	if _, err := eng.Save("test seed: contradiction triangle"); err != nil {
		eng.Unlock()
		t.Fatalf("engine.Save: %v", err)
	}
	eng.Unlock()

	if _, apiErr := a.Update(ctx, UpdateRequest{ID: idA, Content: "record A revised: rollout strategy is now canary"}); apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	for _, e := range eng.Graph().EdgesFrom(idA) {
		if e.Type == "contradicts" {
			t.Error("contradicts edge from A survived the content change")
		}
	}
	for _, e := range eng.Graph().EdgesTo(idA) {
		if e.Type == "contradicts" {
			t.Error("contradicts edge to A survived the content change")
		}
	}
	status := func(id string) string {
		n, _ := eng.Graph().GetNode(id)
		s, _ := n.Properties.GetString("epistemic_status")
		return s
	}
	if s := status(idA); s == "contested" {
		t.Error("updated record must shed contested")
	}
	if s := status(idB); s != "contested" {
		t.Errorf("peer B still conflicts with C and must stay contested, got %q", s)
	}
	if s := status(idC); s != "contested" {
		t.Errorf("record C's conflict with B is untouched; contested must survive, got %q", s)
	}
}
