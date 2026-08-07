package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// addConcept seeds a curation-shaped concept node through the engine.
func addConcept(t *testing.T, eng *core.Engine, keyword string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":    graph.StringProperty("Concept: " + keyword + ". Connects records."),
		"content_short":   graph.StringProperty("Concept: " + keyword),
		"node_type":       graph.StringProperty("concept"),
		"concept_keyword": graph.StringProperty(keyword),
		"knowledge_type":  graph.StringProperty("conceptual"),
		"created_at":      graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	if _, err := eng.Save("curation: concept emerge"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return n.ID
}

// TestConceptMachineOwnedGuards pins the machine-owned write
// boundary: manual update/classify/resolve/link on a concept are
// refused with a redirecting error; the member records remain the
// editable surface.
func TestConceptMachineOwnedGuards(t *testing.T) {
	srv, eng := setupTestServer(t)
	ctx := context.Background()
	conceptID := addConcept(t, eng, "observability")
	recordID := addRecord(t, eng, "a plain member record")

	assertRefused := func(op string, apiErr *api.APIError) {
		t.Helper()
		if apiErr == nil {
			t.Fatalf("%s on a concept succeeded; concepts are machine-owned", op)
		}
		if apiErr.Code != "input_error" || !strings.Contains(apiErr.Message, "machine-owned") {
			t.Fatalf("%s error = %+v, want a redirecting machine-owned refusal", op, apiErr)
		}
	}

	_, updErr := srv.api.Update(ctx, api.UpdateRequest{ID: conceptID, Content: "manual rewrite"})
	assertRefused("update", updErr)

	conf := 0.5
	_, clsErr := srv.api.Classify(ctx, api.ClassifyRequest{ID: conceptID, Confidence: &conf})
	assertRefused("classify", clsErr)

	_, resErr := srv.api.Resolve(ctx, api.ResolveRequest{ID: conceptID, Resolution: "obsolete"})
	assertRefused("resolve", resErr)

	_, lnkErr := srv.api.Link(ctx, api.LinkRequest{SourceID: recordID, TargetID: conceptID, EdgeType: "related_to"})
	assertRefused("link", lnkErr)

	// The member record itself stays fully editable.
	if _, apiErr := srv.api.Classify(ctx, api.ClassifyRequest{ID: recordID, Confidence: &conf}); apiErr != nil {
		t.Fatalf("classify on a record: %v", apiErr)
	}

	// History search on a concept id redirects instead of suggesting a
	// backfill that could never index it.
	_, hsErr := srv.api.HistorySearch(ctx, api.HistorySearchRequest{Text: "anything", ID: conceptID})
	if hsErr == nil || hsErr.Code != "input_error" || !strings.Contains(hsErr.Message, "derived data") {
		t.Fatalf("history search on concept = %+v, want a derived-data redirect", hsErr)
	}
}

// TestConceptHistoryReportsDerived pins the timeline boundary: a
// concept's history names it derived data instead of showing
// synthesis churn as knowledge versions.
func TestConceptHistoryReportsDerived(t *testing.T) {
	srv, eng := setupTestServer(t)
	conceptID := addConcept(t, eng, "batching")

	resp, apiErr := srv.api.History(context.Background(), api.HistoryRequest{ID: conceptID})
	if apiErr != nil {
		t.Fatalf("History: %v", apiErr)
	}
	if !strings.Contains(resp.VersionCoverage, "derived node") {
		t.Fatalf("version_coverage = %q, want the derived-node statement", resp.VersionCoverage)
	}
	if len(resp.Versions) != 0 {
		t.Fatalf("concept carries a version timeline: %+v", resp.Versions)
	}
}
