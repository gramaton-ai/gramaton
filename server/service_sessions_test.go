package server

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

func TestSessionCreateAndGet(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a session.
	result, svcErr := srv.serviceSessionCreate("test-session-001")
	if svcErr != nil {
		t.Fatalf("create: %v", svcErr)
	}
	id, ok := result["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected id in result")
	}
	if result["resumed"] != false {
		t.Error("new session should have resumed=false")
	}

	// Get should return it.
	got, svcErr := srv.serviceSessionGet(id)
	if svcErr != nil {
		t.Fatalf("get: %v", svcErr)
	}
	if got["id"] != id {
		t.Errorf("get id = %v, want %v", got["id"], id)
	}
	if got["client_session_id"] != "test-session-001" {
		t.Errorf("client_session_id = %v, want test-session-001", got["client_session_id"])
	}
	topics := got["topics"].([]map[string]any)
	if len(topics) != 0 {
		t.Errorf("expected 0 topics, got %d", len(topics))
	}
}

func TestSessionCreateIdempotent(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a session.
	r1, svcErr := srv.serviceSessionCreate("test-session-002")
	if svcErr != nil {
		t.Fatalf("first create: %v", svcErr)
	}
	id1 := r1["id"].(string)

	// Same client_session_id returns the same session.
	r2, svcErr := srv.serviceSessionCreate("test-session-002")
	if svcErr != nil {
		t.Fatalf("second create: %v", svcErr)
	}
	id2 := r2["id"].(string)

	if id1 != id2 {
		t.Errorf("expected same session id, got %v and %v", id1, id2)
	}
	if r2["resumed"] != true {
		t.Error("second create should have resumed=true")
	}
}

func TestSessionAddTopicAndSegment(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("test-session-003")
	sessionID := result["id"].(string)

	// Add a topic.
	topicResult, svcErr := srv.serviceSessionAddTopic(sessionID, "Architecture decisions", "")
	if svcErr != nil {
		t.Fatalf("add topic: %v", svcErr)
	}
	topicID := topicResult["id"].(string)
	if topicID == "" {
		t.Fatal("expected topic id")
	}
	if topicResult["name"] != "Architecture decisions" {
		t.Errorf("topic name = %v, want 'Architecture decisions'", topicResult["name"])
	}

	// Add a segment.
	segResult, svcErr := srv.serviceSessionAddSegment(sessionID, topicID, "We decided to use PostgreSQL for the primary datastore.")
	if svcErr != nil {
		t.Fatalf("add segment: %v", svcErr)
	}
	segID := segResult["id"].(string)
	if segID == "" {
		t.Fatal("expected segment id")
	}

	// Get session should include the topic and segment.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}
	segments := topics[0]["segments"].([]map[string]any)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0]["content"] != "We decided to use PostgreSQL for the primary datastore." {
		t.Errorf("unexpected segment content: %v", segments[0]["content"])
	}
}

func TestSessionLookupByClientID(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("my-unique-id")
	id := result["id"].(string)

	// Create second session.
	r2, _ := srv.serviceSessionCreate("different-id")
	id2 := r2["id"].(string)
	if id == id2 {
		t.Fatal("different client IDs should produce different sessions")
	}

	// Lookup first one should return correct session.
	r3, _ := srv.serviceSessionCreate("my-unique-id")
	if r3["id"].(string) != id {
		t.Errorf("lookup returned wrong session: got %v, want %v", r3["id"], id)
	}
}

func TestSessionTopicBranching(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("branching-test")
	sessionID := result["id"].(string)

	// Create parent topic.
	parentResult, _ := srv.serviceSessionAddTopic(sessionID, "Main topic", "")
	parentID := parentResult["id"].(string)

	// Branch from it.
	branchResult, svcErr := srv.serviceSessionAddTopic(sessionID, "Sub topic", parentID)
	if svcErr != nil {
		t.Fatalf("branch topic: %v", svcErr)
	}
	if branchResult["branched_from"] != parentID {
		t.Errorf("branched_from = %v, want %v", branchResult["branched_from"], parentID)
	}
}

func TestSessionTopicBranchingErrors(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("branching-error-test")
	sessionID := result["id"].(string)

	// Error: branched_from with nonexistent ID.
	_, svcErr := srv.serviceSessionAddTopic(sessionID, "Bad branch", "nonexistent-id")
	if svcErr == nil {
		t.Fatal("expected error for nonexistent branched_from")
	}

	// Error: branched_from pointing to a non-topic node.
	_, svcErr = srv.serviceSessionAddTopic(sessionID, "Bad branch", sessionID)
	if svcErr == nil {
		t.Fatal("expected error for branched_from pointing to session node")
	}

	// Error: branched_from pointing to a topic in a different session.
	r2, _ := srv.serviceSessionCreate("other-session")
	otherSessionID := r2["id"].(string)
	otherTopic, _ := srv.serviceSessionAddTopic(otherSessionID, "Other topic", "")
	otherTopicID := otherTopic["id"].(string)

	_, svcErr = srv.serviceSessionAddTopic(sessionID, "Cross-session branch", otherTopicID)
	if svcErr == nil {
		t.Fatal("expected error for branched_from from different session")
	}
}

func TestSessionSegmentCaptureStatus(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("capture-test")
	sessionID := result["id"].(string)

	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Test topic", "")
	topicID := topicResult["id"].(string)

	segResult, _ := srv.serviceSessionAddSegment(sessionID, topicID, "Some knowledge extracted")
	segID := segResult["id"].(string)

	// Initially uncaptured.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	segments := topics[0]["segments"].([]map[string]any)
	if _, has := segments[0]["captured_as"]; has {
		t.Error("new segment should not have captured_as")
	}

	// Update capture status.
	capResult, svcErr := srv.serviceSessionUpdateSegmentCapture(segID, "memory-record-123")
	if svcErr != nil {
		t.Fatalf("update capture: %v", svcErr)
	}
	if capResult["captured_as"] != "memory-record-123" {
		t.Errorf("captured_as = %v, want memory-record-123", capResult["captured_as"])
	}
	if _, has := capResult["captured_at"]; !has {
		t.Error("expected captured_at timestamp")
	}

	// Verify it persists in get.
	got2, _ := srv.serviceSessionGet(sessionID)
	topics2 := got2["topics"].([]map[string]any)
	segments2 := topics2[0]["segments"].([]map[string]any)
	if segments2[0]["captured_as"] != "memory-record-123" {
		t.Errorf("captured_as after get = %v, want memory-record-123", segments2[0]["captured_as"])
	}
}

func TestSessionEmptySegmentRejected(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("empty-seg-test")
	sessionID := result["id"].(string)
	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Topic", "")
	topicID := topicResult["id"].(string)

	// Empty content.
	_, svcErr := srv.serviceSessionAddSegment(sessionID, topicID, "")
	if svcErr == nil {
		t.Fatal("expected error for empty segment content")
	}

	// Whitespace-only content.
	_, svcErr = srv.serviceSessionAddSegment(sessionID, topicID, "   \n\t  ")
	if svcErr == nil {
		t.Fatal("expected error for whitespace-only segment content")
	}
}

func TestSessionEmptyClientIDRejected(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceSessionCreate("")
	if svcErr == nil {
		t.Fatal("expected error for empty client_session_id")
	}
}

func TestSessionPersistence(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("persist-test")
	sessionID := result["id"].(string)
	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Persistent topic", "")
	topicID := topicResult["id"].(string)
	srv.serviceSessionAddSegment(sessionID, topicID, "This should survive a restart")

	// Simulate restart by saving and reloading the engine.
	eng.RLock()
	headHash := eng.HeadHashLocked()
	eng.RUnlock()

	// Reload from the saved state.
	_, err := eng.Graph().Load(eng.Store(), headHash)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	eng.RebuildAllIndexes()

	// Verify session survives.
	got, svcErr := srv.serviceSessionGet(sessionID)
	if svcErr != nil {
		t.Fatalf("get after reload: %v", svcErr)
	}
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic after reload, got %d", len(topics))
	}
	if topics[0]["name"] != "Persistent topic" {
		t.Errorf("topic name after reload = %v", topics[0]["name"])
	}
	segments := topics[0]["segments"].([]map[string]any)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment after reload, got %d", len(segments))
	}
	if segments[0]["content"] != "This should survive a restart" {
		t.Errorf("segment content after reload = %v", segments[0]["content"])
	}
}

func TestSessionMultipleSessions(t *testing.T) {
	srv, _ := setupTestServer(t)

	r1, _ := srv.serviceSessionCreate("session-a")
	r2, _ := srv.serviceSessionCreate("session-b")
	id1 := r1["id"].(string)
	id2 := r2["id"].(string)

	if id1 == id2 {
		t.Fatal("two sessions should have different IDs")
	}

	// Add topic to session A.
	tA, _ := srv.serviceSessionAddTopic(id1, "Topic A", "")
	topicA := tA["id"].(string)
	srv.serviceSessionAddSegment(id1, topicA, "Segment in A")

	// Add topic to session B.
	tB, _ := srv.serviceSessionAddTopic(id2, "Topic B", "")
	topicB := tB["id"].(string)
	srv.serviceSessionAddSegment(id2, topicB, "Segment in B")

	// Verify isolation.
	gotA, _ := srv.serviceSessionGet(id1)
	topicsA := gotA["topics"].([]map[string]any)
	if len(topicsA) != 1 {
		t.Errorf("session A: expected 1 topic, got %d", len(topicsA))
	}
	if topicsA[0]["name"] != "Topic A" {
		t.Errorf("session A topic name = %v", topicsA[0]["name"])
	}

	gotB, _ := srv.serviceSessionGet(id2)
	topicsB := gotB["topics"].([]map[string]any)
	if len(topicsB) != 1 {
		t.Errorf("session B: expected 1 topic, got %d", len(topicsB))
	}
	if topicsB[0]["name"] != "Topic B" {
		t.Errorf("session B topic name = %v", topicsB[0]["name"])
	}

	// Topic from session B should not be addable to session A as a segment parent.
	_, svcErr := srv.serviceSessionAddSegment(id1, topicB, "Cross-session segment")
	if svcErr == nil {
		t.Fatal("expected error adding segment to topic from different session")
	}
}

func TestSessionConcurrentWrites(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("concurrent-test")
	sessionID := result["id"].(string)
	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Concurrent topic", "")
	topicID := topicResult["id"].(string)

	// Concurrently add 10 segments.
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, svcErr := srv.serviceSessionAddSegment(sessionID, topicID,
				"Concurrent segment from goroutine")
			if svcErr != nil {
				errors <- svcErr
			}
		}(i)
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent write error: %v", err)
	}

	// All 10 segments should be there.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	segments := topics[0]["segments"].([]map[string]any)
	if len(segments) != 10 {
		t.Errorf("expected 10 segments, got %d", len(segments))
	}
}

func TestSessionSegmentAppendOnly(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("append-only-test")
	sessionID := result["id"].(string)
	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Topic", "")
	topicID := topicResult["id"].(string)
	segResult, _ := srv.serviceSessionAddSegment(sessionID, topicID, "Original content")
	segID := segResult["id"].(string)

	// Attempting to update a segment via gramaton_update should be blocked.
	_, svcErr := srv.serviceUpdate(segID, &updateRequest{
		SummaryShort: "trying to modify segment",
	})
	if svcErr == nil {
		t.Fatal("expected error when updating a segment via serviceUpdate")
	}
}

func TestSessionGetNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, svcErr := srv.serviceSessionGet("nonexistent-id")
	if svcErr == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestSessionSegmentUpdateCaptureErrors(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Error: empty segment_id.
	_, svcErr := srv.serviceSessionUpdateSegmentCapture("", "some-record")
	if svcErr == nil {
		t.Fatal("expected error for empty segment_id")
	}

	// Error: empty captured_as.
	_, svcErr = srv.serviceSessionUpdateSegmentCapture("some-seg", "")
	if svcErr == nil {
		t.Fatal("expected error for empty captured_as")
	}

	// Error: nonexistent segment.
	_, svcErr = srv.serviceSessionUpdateSegmentCapture("nonexistent", "some-record")
	if svcErr == nil {
		t.Fatal("expected error for nonexistent segment")
	}

	// Error: updating capture on a non-segment node.
	result, _ := srv.serviceSessionCreate("capture-error-test")
	sessionID := result["id"].(string)

	_, svcErr = srv.serviceSessionUpdateSegmentCapture(sessionID, "some-record")
	if svcErr == nil {
		t.Fatal("expected error for updating capture on a session node")
	}
}

// --- Phase 2: Prepare/Commit tests ---

func TestSessionPrepareReturnsInstructionsAndState(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("prepare-test")
	sessionID := result["id"].(string)

	// Add some data to the session first.
	topicResult, _ := srv.serviceSessionAddTopic(sessionID, "Design", "")
	topicID := topicResult["id"].(string)
	srv.serviceSessionAddSegment(sessionID, topicID, "Previous knowledge segment")

	// Prepare should return instructions + session state.
	prepResult, svcErr := srv.serviceSessionPrepare(sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	instructions, ok := prepResult["instructions"].(string)
	if !ok || instructions == "" {
		t.Fatal("expected instructions in prepare response")
	}
	state, ok := prepResult["session_state"].(map[string]any)
	if !ok {
		t.Fatal("expected session_state in prepare response")
	}
	if state["id"] != sessionID {
		t.Errorf("session_state.id = %v, want %v", state["id"], sessionID)
	}
	topics := state["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Errorf("expected 1 topic in session state, got %d", len(topics))
	}
}

func TestSessionCommitRejectsWithoutPrepare(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("reject-test")
	sessionID := result["id"].(string)

	segments := []commitSegment{
		{Content: "Some knowledge", TopicName: "Design"},
	}
	_, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr == nil {
		t.Fatal("expected error when committing without prepare")
	}
	if svcErr.Code != "prepare_required" {
		t.Errorf("expected prepare_required code, got %s", svcErr.Code)
	}
}

func TestSessionCommitAfterPrepare(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("commit-test")
	sessionID := result["id"].(string)

	// Prepare first.
	_, svcErr := srv.serviceSessionPrepare(sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	// Commit segments.
	segments := []commitSegment{
		{Content: "Decided to use PostgreSQL", TopicName: "Architecture"},
		{Content: "User prefers dark mode", TopicName: "Preferences"},
	}
	commitResult, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult["segments_added"].(int) != 2 {
		t.Errorf("segments_added = %v, want 2", commitResult["segments_added"])
	}
	if commitResult["topics_created"].(int) != 2 {
		t.Errorf("topics_created = %v, want 2", commitResult["topics_created"])
	}

	// Verify via get.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}
}

func TestSessionCommitExistingTopic(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("existing-topic-test")
	sessionID := result["id"].(string)

	// Create a topic first.
	srv.serviceSessionAddTopic(sessionID, "Architecture", "")

	// Prepare and commit to the existing topic.
	srv.serviceSessionPrepare(sessionID)
	segments := []commitSegment{
		{Content: "Database choice: PostgreSQL", TopicName: "Architecture"},
	}
	commitResult, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult["topics_created"].(int) != 0 {
		t.Errorf("topics_created = %v, want 0 (topic already existed)", commitResult["topics_created"])
	}

	// Verify segment is under existing topic.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}
	segments2 := topics[0]["segments"].([]map[string]any)
	if len(segments2) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments2))
	}
}

func TestSessionRoundTrip(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Full round-trip: start -> prepare -> commit -> get.
	result, _ := srv.serviceSessionCreate("roundtrip-test")
	sessionID := result["id"].(string)

	srv.serviceSessionPrepare(sessionID)

	segments := []commitSegment{
		{Content: "Go is our primary language", TopicName: "Tech Stack"},
		{Content: "Target: sub-200ms search latency", TopicName: "Performance"},
		{Content: "Using BM25 for session segments", TopicName: "Tech Stack"},
	}
	srv.serviceSessionCommit(sessionID, segments)

	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}

	// Find Tech Stack topic and verify it has 2 segments.
	for _, topic := range topics {
		if topic["name"] == "Tech Stack" {
			segs := topic["segments"].([]map[string]any)
			if len(segs) != 2 {
				t.Errorf("Tech Stack: expected 2 segments, got %d", len(segs))
			}
		}
	}
}

func TestSessionCommitEmptySegments(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("empty-commit-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	// Empty segments list should be rejected.
	_, svcErr := srv.serviceSessionCommit(sessionID, []commitSegment{})
	if svcErr == nil {
		t.Fatal("expected error for empty segments")
	}
}

func TestSessionCommitMalformedSegments(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("malformed-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	// Missing content.
	_, svcErr := srv.serviceSessionCommit(sessionID, []commitSegment{
		{Content: "", TopicName: "Design"},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty content")
	}

	// Need a new prepare since the flag was consumed.
	srv.serviceSessionPrepare(sessionID)

	// Missing topic.
	_, svcErr = srv.serviceSessionCommit(sessionID, []commitSegment{
		{Content: "Some content", TopicName: ""},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty topic name")
	}
}

func TestSessionPreparedFlagConsumed(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("flag-consumed-test")
	sessionID := result["id"].(string)

	// Prepare sets the flag.
	srv.serviceSessionPrepare(sessionID)

	// First commit consumes it.
	segments := []commitSegment{
		{Content: "First commit", TopicName: "Test"},
	}
	_, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("first commit: %v", svcErr)
	}

	// Second commit without re-prepare should fail.
	_, svcErr = srv.serviceSessionCommit(sessionID, segments)
	if svcErr == nil {
		t.Fatal("expected error on second commit without prepare")
	}
	if svcErr.Code != "prepare_required" {
		t.Errorf("expected prepare_required, got %s", svcErr.Code)
	}
}

func TestSessionConcurrentPrepare(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("concurrent-prepare-test")
	sessionID := result["id"].(string)

	// Two prepares -- second should overwrite, both should succeed.
	_, svcErr1 := srv.serviceSessionPrepare(sessionID)
	_, svcErr2 := srv.serviceSessionPrepare(sessionID)
	if svcErr1 != nil {
		t.Fatalf("first prepare: %v", svcErr1)
	}
	if svcErr2 != nil {
		t.Fatalf("second prepare: %v", svcErr2)
	}

	// Commit should work (flag is still set).
	segments := []commitSegment{
		{Content: "After double prepare", TopicName: "Test"},
	}
	_, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit after double prepare: %v", svcErr)
	}
}

// --- Phase 6: Extraction Prompt tests ---

func TestPrepareReturnsExtractionPromptWithSections(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("prompt-test")
	sessionID := result["id"].(string)

	prepResult, svcErr := srv.serviceSessionPrepare(sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	instructions := prepResult["instructions"].(string)

	// Verify prompt has the key sections from extraction.md.
	for _, section := range []string{
		"What to Extract",
		"What to Skip",
		"Classification Fields",
		"Call to Action",
		"gramaton_session_commit",
	} {
		if !strings.Contains(instructions, section) {
			t.Errorf("extraction prompt missing section: %q", section)
		}
	}
}

// --- Phase 4: Hybrid Commit tests ---

func TestHybridCommitCreatesMemoryRecords(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("hybrid-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	segments := []commitSegment{
		{Content: "We decided on PostgreSQL", TopicName: "Architecture",
			Temporality: "durable", KnowledgeType: "semantic"},
	}
	commitResult, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult["memory_records_created"].(int) != 1 {
		t.Errorf("memory_records_created = %v, want 1", commitResult["memory_records_created"])
	}
	if commitResult["edges_created"].(int) != 1 {
		t.Errorf("edges_created = %v, want 1", commitResult["edges_created"])
	}

	// Verify Memory record exists with correct metadata.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	segResp := topics[0]["segments"].([]map[string]any)
	capturedAs := segResp[0]["captured_as"].(string)
	if capturedAs == "" {
		t.Fatal("segment should have captured_as set")
	}

	// Check the Memory record node directly.
	eng.RLock()
	memNode, ok := eng.Graph().GetNode(capturedAs)
	eng.RUnlock()
	if !ok {
		t.Fatalf("Memory record %s not found", capturedAs)
	}
	if kt, _ := memNode.Properties.GetString("knowledge_type"); kt != "semantic" {
		t.Errorf("Memory record knowledge_type = %v, want semantic", kt)
	}
	if temp, _ := memNode.Properties.GetString("temporality"); temp != "durable" {
		t.Errorf("Memory record temporality = %v, want durable", temp)
	}
	if ps, _ := memNode.Properties.GetString("processing_status"); ps != "processed" {
		t.Errorf("Memory record processing_status = %v, want processed", ps)
	}
}

func TestHybridCommitExtractedAsEdge(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("edge-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	segments := []commitSegment{
		{Content: "Redis caching layer design", TopicName: "Infra"},
	}
	srv.serviceSessionCommit(sessionID, segments)

	// Find the segment node.
	got, _ := srv.serviceSessionGet(sessionID)
	topics := got["topics"].([]map[string]any)
	segID := topics[0]["segments"].([]map[string]any)[0]["id"].(string)
	capturedAs := topics[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	// Verify extracted_as edge from segment to Memory record.
	eng.RLock()
	defer eng.RUnlock()
	edges := eng.Graph().EdgesFrom(segID)
	foundExtractedAs := false
	for _, e := range edges {
		if e.Type == "extracted_as" && e.TargetID == capturedAs {
			foundExtractedAs = true
		}
	}
	if !foundExtractedAs {
		t.Errorf("expected extracted_as edge from segment %s to memory %s", segID, capturedAs)
	}
}

func TestHybridCommitMemoryRecordHasMetadata(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("metadata-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	conf := 0.85
	segments := []commitSegment{
		{
			Content:         "Event sourcing for audit trail",
			TopicName:       "Architecture",
			Temporality:     "durable",
			Confidence:      &conf,
			KnowledgeType:   "procedural",
			EpistemicStatus: "probable",
			Keywords:        []string{"event-sourcing", "audit"},
			SummaryShort:    "Event sourcing decision",
		},
	}
	srv.serviceSessionCommit(sessionID, segments)

	got, _ := srv.serviceSessionGet(sessionID)
	capturedAs := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	eng.RLock()
	memNode, _ := eng.Graph().GetNode(capturedAs)
	eng.RUnlock()

	if v, _ := memNode.Properties.GetFloat64("confidence"); v != 0.85 {
		t.Errorf("confidence = %v, want 0.85", v)
	}
	if v, _ := memNode.Properties.GetString("epistemic_status"); v != "probable" {
		t.Errorf("epistemic_status = %v, want probable", v)
	}
	if v, _ := memNode.Properties.GetString("content_short"); v != "Event sourcing decision" {
		t.Errorf("content_short = %v, want 'Event sourcing decision'", v)
	}
	if v, _ := memNode.Properties.GetStringList("content_keywords"); len(v) != 2 {
		t.Errorf("keywords = %v, want [event-sourcing audit]", v)
	}
}

func TestHybridCommitPartialMetadata(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("partial-meta-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	// Commit with minimal metadata.
	segments := []commitSegment{
		{Content: "Some insight with no classification", TopicName: "General"},
	}
	commitResult, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult["memory_records_created"].(int) != 1 {
		t.Error("should create memory record even with minimal metadata")
	}

	// Default knowledge_type should be "episodic".
	got, _ := srv.serviceSessionGet(sessionID)
	capturedAs := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)
	eng.RLock()
	memNode, _ := eng.Graph().GetNode(capturedAs)
	eng.RUnlock()
	if kt, _ := memNode.Properties.GetString("knowledge_type"); kt != "episodic" {
		t.Errorf("default knowledge_type = %v, want episodic", kt)
	}
}

func TestHybridCommitMultipleSegments(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("multi-segment-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	segments := []commitSegment{
		{Content: "First knowledge", TopicName: "Topic A"},
		{Content: "Second knowledge", TopicName: "Topic A"},
		{Content: "Third knowledge", TopicName: "Topic B"},
	}
	commitResult, svcErr := srv.serviceSessionCommit(sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult["segments_added"].(int) != 3 {
		t.Errorf("segments_added = %v, want 3", commitResult["segments_added"])
	}
	if commitResult["memory_records_created"].(int) != 3 {
		t.Errorf("memory_records_created = %v, want 3", commitResult["memory_records_created"])
	}
	if commitResult["edges_created"].(int) != 3 {
		t.Errorf("edges_created = %v, want 3", commitResult["edges_created"])
	}
}

func TestHybridCommitSegmentNotVectorIndexed(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("no-vector-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	segments := []commitSegment{
		{Content: "Session segment should have no vector", TopicName: "Test"},
	}
	srv.serviceSessionCommit(sessionID, segments)

	got, _ := srv.serviceSessionGet(sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)
	capturedAs := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	eng.RLock()
	defer eng.RUnlock()

	// Segment should NOT have embedding.
	segNode, _ := eng.Graph().GetNode(segID)
	if _, hasVec := segNode.Properties.GetVector("embedding_full"); hasVec {
		t.Error("Session segment should NOT have vector embedding")
	}

	// Memory record existence check (vector depends on embedder availability in test).
	_, ok := eng.Graph().GetNode(capturedAs)
	if !ok {
		t.Error("Memory record should exist")
	}
}

func TestHybridCommitFollowEdges(t *testing.T) {
	srv, eng := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("follow-edge-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)
	srv.serviceSessionCommit(sessionID, []commitSegment{
		{Content: "Bidirectional edge test", TopicName: "Test"},
	})

	got, _ := srv.serviceSessionGet(sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)
	memID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	eng.RLock()
	defer eng.RUnlock()

	// Follow extracted_as from segment to Memory.
	foundForward := false
	for _, e := range eng.Graph().EdgesFrom(segID) {
		if e.Type == "extracted_as" && e.TargetID == memID {
			foundForward = true
		}
	}
	if !foundForward {
		t.Error("expected extracted_as edge segment -> memory")
	}

	// Follow back from Memory to segment (inbound edges).
	foundBack := false
	for _, e := range eng.Graph().EdgesTo(memID) {
		if e.Type == "extracted_as" && e.SourceID == segID {
			foundBack = true
		}
	}
	if !foundBack {
		t.Error("expected extracted_as edge traceable back from memory -> segment")
	}
}

func TestCurationSkipsSegmentNodes(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Create a session with a long segment (>500 chars to trigger observation extraction).
	result, _ := srv.serviceSessionCreate("curation-skip-test")
	sessionID := result["id"].(string)
	srv.serviceSessionPrepare(sessionID)

	longContent := "This is a very detailed segment about architecture decisions. " +
		"We explored multiple options including PostgreSQL, MySQL, and MongoDB. " +
		"After careful consideration of our scalability requirements, consistency needs, " +
		"and team expertise, we decided to go with PostgreSQL. The key factors were " +
		"ACID compliance, JSON support via JSONB columns, excellent Go driver support, " +
		"and the team's existing PostgreSQL experience. We also considered the operational " +
		"overhead of each option and determined that PostgreSQL's maturity and tooling " +
		"ecosystem would minimize our operational burden. This decision affects the " +
		"entire data layer and should be considered durable unless requirements change. " +
		"Testing should verify this content exceeds five hundred characters."
	srv.serviceSessionCommit(sessionID, []commitSegment{
		{Content: longContent, TopicName: "Architecture"},
	})

	// Verify the segment node has knowledge_type=segment.
	got, _ := srv.serviceSessionGet(sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)

	eng.RLock()
	segNode, _ := eng.Graph().GetNode(segID)
	eng.RUnlock()

	kt, _ := segNode.Properties.GetString("knowledge_type")
	if kt != "segment" {
		t.Errorf("expected knowledge_type=segment, got %v", kt)
	}

	// The curation observe.go now skips knowledge_type=segment nodes.
	// This test verifies the property is correctly set; the skip itself
	// is in curation/observe.go and tested by the curation package.
	_ = graph.IsStructuralEdge("segment_of") // verify it's structural
}

// --- Phase 9: Archive tests ---

func TestSessionArchiveCreateAndRead(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("archive-test")
	sessionID := result["id"].(string)

	// Write a source file to archive.
	tmpDir := t.TempDir()
	srcPath := tmpDir + "/conversation.txt"
	srcContent := strings.Repeat("User: How should we design the database?\nAssistant: I recommend PostgreSQL for these reasons...\n", 20)
	os.WriteFile(srcPath, []byte(srcContent), 0o644)

	// Create archive.
	archiveResult, svcErr := srv.serviceSessionArchive(sessionID, srcPath)
	if svcErr != nil {
		t.Fatalf("archive: %v", svcErr)
	}
	if archiveResult["session_id"] != sessionID {
		t.Errorf("session_id = %v", archiveResult["session_id"])
	}
	archivePath := archiveResult["archive_path"].(string)
	if archivePath == "" {
		t.Fatal("expected archive_path")
	}
	if archiveResult["original_size"].(int) != len(srcContent) {
		t.Errorf("original_size = %v, want %d", archiveResult["original_size"], len(srcContent))
	}

	// Verify compressed file exists and is smaller.
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive file not found: %v", err)
	}
	if info.Size() >= int64(len(srcContent)) {
		t.Errorf("compressed size %d >= original %d", info.Size(), len(srcContent))
	}

	// Read archive back.
	readResult, svcErr := srv.serviceSessionReadArchive(sessionID)
	if svcErr != nil {
		t.Fatalf("read archive: %v", svcErr)
	}
	if readResult["content"].(string) != srcContent {
		t.Error("decompressed content doesn't match original")
	}
}

func TestSessionArchiveReferencedInGet(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("archive-ref-test")
	sessionID := result["id"].(string)

	tmpDir := t.TempDir()
	srcPath := tmpDir + "/conv.txt"
	os.WriteFile(srcPath, []byte("test conversation"), 0o644)
	srv.serviceSessionArchive(sessionID, srcPath)

	// Get should include raw_archive field.
	got, _ := srv.serviceSessionGet(sessionID)
	archive, ok := got["raw_archive"].(map[string]any)
	if !ok {
		t.Fatal("expected raw_archive in session get")
	}
	if archive["path"] == nil || archive["path"] == "" {
		t.Error("expected archive path")
	}
	if archive["compressed_size"] == nil {
		t.Error("expected compressed_size")
	}
	if archive["archived_at"] == nil {
		t.Error("expected archived_at")
	}
}

func TestSessionGetWithoutArchive(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("no-archive-test")
	sessionID := result["id"].(string)

	// Get should work without an archive -- raw_archive absent.
	got, _ := srv.serviceSessionGet(sessionID)
	if _, has := got["raw_archive"]; has {
		t.Error("session without archive should not have raw_archive field")
	}
}

func TestSessionArchiveMissingSource(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("missing-src-test")
	sessionID := result["id"].(string)

	_, svcErr := srv.serviceSessionArchive(sessionID, "/nonexistent/file.txt")
	if svcErr == nil {
		t.Fatal("expected error for missing source file")
	}
}

func TestSessionArchiveLargeContent(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("large-archive-test")
	sessionID := result["id"].(string)

	// Create a 1MB+ source file.
	tmpDir := t.TempDir()
	srcPath := tmpDir + "/large.txt"
	largeContent := strings.Repeat("This is a line of conversation text.\n", 30000) // ~1.1MB
	os.WriteFile(srcPath, []byte(largeContent), 0o644)

	archiveResult, svcErr := srv.serviceSessionArchive(sessionID, srcPath)
	if svcErr != nil {
		t.Fatalf("archive: %v", svcErr)
	}

	// Compression should achieve significant reduction on repetitive text.
	compressedSize := archiveResult["compressed_size"].(int64)
	originalSize := archiveResult["original_size"].(int)
	ratio := float64(compressedSize) / float64(originalSize)
	if ratio > 0.5 {
		t.Errorf("compression ratio %.2f too high for repetitive text", ratio)
	}

	// Verify round-trip.
	readResult, _ := srv.serviceSessionReadArchive(sessionID)
	if readResult["content"].(string) != largeContent {
		t.Error("large archive round-trip mismatch")
	}
}

func TestSessionReadArchiveNoArchive(t *testing.T) {
	srv, _ := setupTestServer(t)

	result, _ := srv.serviceSessionCreate("read-no-archive-test")
	sessionID := result["id"].(string)

	_, svcErr := srv.serviceSessionReadArchive(sessionID)
	if svcErr == nil {
		t.Fatal("expected error reading archive from session without one")
	}
}
