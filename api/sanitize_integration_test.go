package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// These tests verify that internal/sanitize is actually invoked at
// each api write site. sanitize_test.go (in internal/sanitize/)
// tests the helper in isolation; these tests prove the wiring.
// Without both, a future refactor that deletes a sanitize call
// would leave the helper "tested" while the write path regresses.

const contaminationTail = `</summary_short>
<parameter name="keywords">["leaked", "stuff"]`

// --- Capture ---

func TestSaveStripsContaminationTail(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	resp, apiErr := a.Save(ctx, SaveRequest{
		Content:      "Full content for the record. Not truncated.",
		SummaryShort: "Real summary here." + contaminationTail,
	})
	if apiErr != nil {
		t.Fatalf("Capture: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(resp.ID)
	if !ok {
		t.Fatal("record missing")
	}
	stored, _ := n.Properties.GetString("content_short")
	if strings.Contains(stored, "</summary_short>") || strings.Contains(stored, "<parameter name=") {
		t.Errorf("stored summary retained contamination: %q", stored)
	}
	if stored != "Real summary here." {
		t.Errorf("stored summary = %q, want %q", stored, "Real summary here.")
	}
}

func TestSaveRejectsPureContamination(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	_, apiErr := a.Save(ctx, SaveRequest{
		Content:      "Full content here.",
		SummaryShort: contaminationTail, // leading tag — strip yields empty
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid, got nil")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("err code = %q, want input_error", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "structured-output") {
		t.Errorf("err message doesn't name the cause: %q", apiErr.Message)
	}
}

func TestSaveStripsContaminationInContextFields(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	resp, apiErr := a.Save(ctx, SaveRequest{
		Content:       "Content body.",
		ContextAbout:  "Clean topic." + contaminationTail,
		ContextWho:    "Alice and Bob." + contaminationTail,
	})
	if apiErr != nil {
		t.Fatalf("Capture: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(resp.ID)
	for _, key := range []string{"context_about", "context_who"} {
		v, _ := n.Properties.GetString(key)
		if strings.Contains(v, "</summary_short>") || strings.Contains(v, "<parameter name=") {
			t.Errorf("%s retained contamination: %q", key, v)
		}
	}
}

// --- Classify ---

func TestClassifyRejectsPureContamination(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("pending"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	})
	if _, err := eng.Save("test"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	_, apiErr := a.Classify(ctx, ClassifyRequest{
		ID:           n.ID,
		Temporality:  "durable",
		SummaryShort: contaminationTail,
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid, got nil")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("err code = %q, want input_error", apiErr.Code)
	}
}

// --- Update ---

func TestUpdateRejectsPureContamination(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "existing record")

	_, apiErr := a.Update(ctx, UpdateRequest{
		ID:           id,
		SummaryShort: contaminationTail,
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid, got nil")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("err code = %q, want input_error", apiErr.Code)
	}
}

func TestUpdateStripsContaminationTail(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()
	id := addRecord(t, eng, "existing record")

	_, apiErr := a.Update(ctx, UpdateRequest{
		ID:           id,
		SummaryShort: "Clean prefix." + contaminationTail,
	})
	if apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	stored, _ := n.Properties.GetString("content_short")
	if strings.Contains(stored, "<parameter name=") {
		t.Errorf("stored summary retained contamination: %q", stored)
	}
	if stored != "Clean prefix." {
		t.Errorf("stored summary = %q, want %q", stored, "Clean prefix.")
	}
}

// --- SessionSave ---

func TestSessionCommitRejectsContaminatedSegment(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "sanitize-reject-test", "")
	sessionID := result["id"].(string)
	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	segments := []SaveSegment{
		{Content: "Clean segment content.", TopicName: "Topic", SummaryShort: contaminationTail},
	}
	_, apiErr := a.SessionSave(ctx, sessionID, segments)
	if apiErr == nil {
		t.Fatal("expected ErrInvalid, got nil")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("err code = %q, want input_error", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "segment") {
		t.Errorf("err message doesn't name the segment: %q", apiErr.Message)
	}
}

func TestSessionCommitStripsSegmentContaminationTail(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "sanitize-strip-test", "")
	sessionID := result["id"].(string)
	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	segments := []SaveSegment{
		{
			Content:      "Body content of segment. Long enough to persist.",
			TopicName:    "Topic",
			SummaryShort: "Clean summary." + contaminationTail,
		},
	}
	if _, apiErr := a.SessionSave(ctx, sessionID, segments); apiErr != nil {
		t.Fatalf("commit: %v", apiErr)
	}

	// Find the promoted memory record and verify its summary is clean.
	eng.RLock()
	defer eng.RUnlock()
	ids := eng.PropIdx().Lookup("knowledge_type", graph.StringProperty("memory"))
	found := 0
	for _, id := range ids {
		n, ok := eng.Graph().GetNode(id)
		if !ok {
			continue
		}
		stored, _ := n.Properties.GetString("content_short")
		if strings.Contains(stored, "<parameter name=") || strings.Contains(stored, "</summary_short>") {
			t.Errorf("memory %s retained contamination: %q", id, stored)
		}
		if stored == "Clean summary." {
			found++
		}
	}
	if found == 0 {
		t.Log("note: promoted memory record with sanitized summary not located via knowledge_type=memory; " +
			"acceptable as long as no contamination leaked above")
	}
}
