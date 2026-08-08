package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// seedSegmentNode plants a node shaped the way api/sessions.go shapes
// a Session segment (knowledge_type=segment, `content`, created_at, no
// processing_status) so the append-only guards can be exercised
// without running a full prepare/save cycle.
func seedSegmentNode(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	var id string
	err := eng.WithWriteBatch("seed segment", func(ws *core.WriteSession) (bool, error) {
		n := ws.AddNode(graph.Properties{
			"knowledge_type": graph.StringProperty("segment"),
			"content":        graph.StringProperty(content),
			"created_at":     graph.TimestampProperty(time.Now().UTC()),
		})
		id = n.ID
		ws.IndexNode(n.ID, content, nil)
		return true, nil
	})
	if err != nil {
		t.Fatalf("seed segment: %v", err)
	}
	return id
}

// TestResolveRefusesSessionSegments pins the append-only contract on
// the resolve path. A segment is a verbatim slice of a conversation;
// stamping resolution + valid_until on one expires a piece of the
// record of what was said, which no later session save can undo.
func TestResolveRefusesSessionSegments(t *testing.T) {
	a, eng := setupTestAPI(t)
	id := seedSegmentNode(t, eng, "a session segment that must stay append-only")

	_, apiErr := a.Resolve(context.Background(), ResolveRequest{
		ID:         id,
		Resolution: "completed",
	})
	if apiErr == nil {
		t.Fatal("Resolve accepted a session segment; segments are append-only")
	}
	if !strings.Contains(apiErr.Message, "append-only") {
		t.Errorf("refusal message = %q, want the append-only explanation", apiErr.Message)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("segment vanished")
	}
	if v, ok := n.Properties.GetString("resolution"); ok {
		t.Errorf("refused resolve still stamped resolution=%q", v)
	}
	if _, ok := n.Properties.GetTimestamp("valid_until"); ok {
		t.Error("refused resolve still expired the segment")
	}
}

// TestClassifyRefusesSessionSegments pins the same contract on the
// classify path: segments carry the session extractor's own metadata,
// and a caller reclassifying one would silently diverge it from the
// Memory record promoted alongside it.
func TestClassifyRefusesSessionSegments(t *testing.T) {
	a, eng := setupTestAPI(t)
	id := seedSegmentNode(t, eng, "another append-only session segment")

	conf := 0.9
	_, apiErr := a.Classify(context.Background(), ClassifyRequest{
		ID:            id,
		Temporality:   "durable",
		Confidence:    &conf,
		KnowledgeType: "semantic",
	})
	if apiErr == nil {
		t.Fatal("Classify accepted a session segment; segments are append-only")
	}
	if !strings.Contains(apiErr.Message, "append-only") {
		t.Errorf("refusal message = %q, want the append-only explanation", apiErr.Message)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("segment vanished")
	}
	if v, ok := n.Properties.GetString("temporality"); ok {
		t.Errorf("refused classify still stamped temporality=%q", v)
	}
	if v, ok := n.Properties.GetString("processing_status"); ok {
		t.Errorf("refused classify still stamped processing_status=%q", v)
	}
	if v, ok := n.Properties.GetString("knowledge_type"); !ok || v != "segment" {
		t.Errorf("knowledge_type = %q, want it left as segment", v)
	}
}

// TestDeleteRecordAcceptsConcepts pins the deliberate asymmetry the
// other three guards do not share: update/classify/resolve refuse
// concepts because they would fight curation over a derived summary,
// but discarding a bad concept is supported -- the next synthesis
// pass regenerates it from its members.
func TestDeleteRecordAcceptsConcepts(t *testing.T) {
	a, eng := setupTestAPI(t)

	var id string
	err := eng.WithWriteBatch("seed concept", func(ws *core.WriteSession) (bool, error) {
		n := ws.AddNode(graph.Properties{
			"node_type":       graph.StringProperty("concept"),
			"concept_keyword": graph.StringProperty("retrieval"),
			"content_full":    graph.StringProperty("Records converge on retrieval quality."),
			"created_at":      graph.TimestampProperty(time.Now().UTC()),
		})
		id = n.ID
		return true, nil
	})
	if err != nil {
		t.Fatalf("seed concept: %v", err)
	}

	resp, apiErr := a.DeleteRecord(context.Background(), DeleteRecordRequest{
		ID:     id,
		Reason: "bad synthesis",
	})
	if apiErr != nil {
		t.Fatalf("DeleteRecord on a concept: %v", apiErr.Message)
	}
	if !resp.Deleted {
		t.Fatal("DeleteRecord reported the concept was not deleted")
	}

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("concept vanished; delete is a soft-delete")
	}
	if ps, _ := n.Properties.GetString("processing_status"); ps != "deleted" {
		t.Errorf("processing_status = %q, want deleted", ps)
	}
}
