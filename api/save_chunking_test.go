package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
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
