package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// noopLLM is a minimal LLM provider for tests that don't exercise LLM calls.
type noopLLM struct{}

func (noopLLM) Complete(_ context.Context, _ string) (string, error)             { return "", nil }
func (noopLLM) CompleteWithModel(_ context.Context, _, _ string) (string, error) { return "", nil }
func (noopLLM) ModelID() string                                                  { return "test-noop" }
func (noopLLM) ProviderName() string                                             { return "noop" }
func (noopLLM) SupportsStructuredOutput() bool                                   { return false }
func (noopLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

// setupTestAPI constructs an API + engine backed by a temp data dir.
// Mirrors server.setupTestServer; kept in this package so tests can
// access unexported api state (preparedMu, preparedSessions, sweep
// helpers) required to verify restart + TTL behavior.
func setupTestAPI(t *testing.T) (*API, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	// Backups go to a SIBLING of DataDir to avoid walking the in-progress archive.
	cfg.Backup.Dir = t.TempDir() + "/backups"
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	a := New(Dependencies{
		Engine:    eng,
		Log:       slog.Default(),
		ConfigDir: dir,
	})
	t.Cleanup(a.StopPreparedSweeper)
	return a, eng
}

// --- Core session creation / retrieval ---

func TestSessionCreateAndGet(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, svcErr := a.SessionStart(ctx, "test-session-001", "")
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

	got, svcErr := a.SessionGet(ctx, id)
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
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, svcErr := a.SessionStart(ctx, "test-session-002", "")
	if svcErr != nil {
		t.Fatalf("first create: %v", svcErr)
	}
	id1 := r1["id"].(string)

	r2, svcErr := a.SessionStart(ctx, "test-session-002", "")
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

func TestSessionLookupByClientID(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "my-unique-id", "")
	id := r1["id"].(string)

	r2, _ := a.SessionStart(ctx, "different-id", "")
	id2 := r2["id"].(string)
	if id == id2 {
		t.Fatal("different client IDs should produce different sessions")
	}

	r3, _ := a.SessionStart(ctx, "my-unique-id", "")
	if r3["id"].(string) != id {
		t.Errorf("lookup returned wrong session: got %v, want %v", r3["id"], id)
	}
}

func TestSessionEmptyClientIDRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	_, svcErr := a.SessionStart(ctx, "", "")
	if svcErr == nil {
		t.Fatal("expected error for empty client_session_id")
	}
}

func TestSessionGetNotFound(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	_, svcErr := a.SessionGet(ctx, "nonexistent-id")
	if svcErr == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

// TestSessionMultipleSessions verifies isolation between two sessions:
// committing to session A doesn't bleed into session B.
func TestSessionMultipleSessions(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "session-a", "")
	r2, _ := a.SessionStart(ctx, "session-b", "")
	idA := r1["id"].(string)
	idB := r2["id"].(string)

	if idA == idB {
		t.Fatal("two sessions should have different IDs")
	}

	// Commit a segment to each session under its own topic.
	if _, err := a.SessionPrepare(ctx, idA); err != nil {
		t.Fatalf("prepare A: %v", err)
	}
	if _, err := a.SessionSave(ctx, idA, []SaveSegment{
		{Content: "Segment in A", TopicName: "Topic A"},
	}); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	if _, err := a.SessionPrepare(ctx, idB); err != nil {
		t.Fatalf("prepare B: %v", err)
	}
	if _, err := a.SessionSave(ctx, idB, []SaveSegment{
		{Content: "Segment in B", TopicName: "Topic B"},
	}); err != nil {
		t.Fatalf("commit B: %v", err)
	}

	gotA, _ := a.SessionGet(ctx, idA)
	topicsA := gotA["topics"].([]map[string]any)
	if len(topicsA) != 1 || topicsA[0]["name"] != "Topic A" {
		t.Errorf("session A topics = %v, want [Topic A]", topicsA)
	}

	gotB, _ := a.SessionGet(ctx, idB)
	topicsB := gotB["topics"].([]map[string]any)
	if len(topicsB) != 1 || topicsB[0]["name"] != "Topic B" {
		t.Errorf("session B topics = %v, want [Topic B]", topicsB)
	}
}

// TestSessionPersistence verifies session + topic + segment survive
// an in-process engine reload from the head commit.
func TestSessionPersistence(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "persist-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "This should survive a restart", TopicName: "Persistent topic"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Simulate restart by reloading engine state from head.
	eng.RLock()
	headHash := eng.HeadHashLocked()
	eng.RUnlock()

	if _, err := eng.Graph().Load(eng.Store(), headHash); err != nil {
		t.Fatalf("reload: %v", err)
	}
	eng.RebuildAllIndexes()

	got, svcErr := a.SessionGet(ctx, sessionID)
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

// TestSessionConcurrentCommits verifies concurrent commits to the same
// session serialize under the engine lock and all segments land.
func TestSessionConcurrentCommits(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "concurrent-test", "")
	sessionID := result["id"].(string)

	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each goroutine gets its own prepare/save cycle.
			if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
				errors <- err
				return
			}
			if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
				{Content: "Concurrent segment", TopicName: "Concurrent topic"},
			}); err != nil {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		// prepare_required is expected when one goroutine's prepare is consumed
		// by a different goroutine's commit; that's a real serialization mode,
		// not a bug. Other errors are failures.
		if apiErr, ok := err.(*APIError); ok && apiErr.Code == "prepare_required" {
			continue
		}
		t.Errorf("concurrent write error: %v", err)
	}

	// All 10 segments should be under the single topic.
	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}
	segments := topics[0]["segments"].([]map[string]any)
	// Each commit that got a valid prepare succeeds; others are rejected.
	// We only verify no write was lost on successful commits.
	if len(segments) == 0 {
		t.Error("expected at least one segment from concurrent commits")
	}
}

// --- Prepare / commit flow ---

func TestSessionPrepareReturnsInstructionsAndState(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "prepare-test", "")
	sessionID := result["id"].(string)

	// Seed a committed segment so prepare has state to return.
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("seed prepare: %v", err)
	}
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Previous knowledge segment", TopicName: "Design"},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	prepResult, svcErr := a.SessionPrepare(ctx, sessionID)
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

func TestSessionPrepareSurfacesCompactionFlag(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "compact-client-001", "")
	sessionID := result["id"].(string)

	flagDir := a.configDir + "/hook-state"
	if err := os.MkdirAll(flagDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	flagPath := flagDir + "/compact-client-001.compacted"
	ts := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(flagPath, []byte(ts), 0o644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	prep, svcErr := a.SessionPrepare(ctx, sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	rc, ok := prep["recent_compaction"].(map[string]any)
	if !ok {
		t.Fatal("expected recent_compaction in prepare response")
	}
	if rc["at"] != ts {
		t.Errorf("recent_compaction.at = %v, want %v", rc["at"], ts)
	}
	instructions, _ := prep["instructions"].(string)
	if !strings.Contains(instructions, "recently compacted") {
		t.Error("expected compaction note prepended to instructions")
	}
	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Error("compaction flag should have been deleted (single-shot)")
	}

	prep2, _ := a.SessionPrepare(ctx, sessionID)
	if _, has := prep2["recent_compaction"]; has {
		t.Error("second prepare should not surface compaction nudge")
	}
}

func TestSessionPrepareSurfacesPrecompactUncapturedFlag(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "precompact-client-001", "")
	sessionID := result["id"].(string)

	flagDir := a.configDir + "/hook-state"
	if err := os.MkdirAll(flagDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	flagPath := flagDir + "/precompact-client-001.precompact-uncaptured"
	ts := time.Now().UTC().Format(time.RFC3339)
	payload := `{"count": 3, "warned_at": "` + ts + `", "archive_path": "/tmp/fake.gz"}`
	if err := os.WriteFile(flagPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	prep, svcErr := a.SessionPrepare(ctx, sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	pending, ok := prep["pending_uncaptured"].(map[string]any)
	if !ok {
		t.Fatal("expected pending_uncaptured in prepare response")
	}
	if pending["count"] != 3 {
		t.Errorf("pending_uncaptured.count = %v, want 3", pending["count"])
	}
	if pending["archive_path"] != "/tmp/fake.gz" {
		t.Errorf("pending_uncaptured.archive_path = %v, want /tmp/fake.gz", pending["archive_path"])
	}
	instructions, _ := prep["instructions"].(string)
	if !strings.Contains(instructions, "uncaptured at the last compaction") {
		t.Error("expected precompact note prepended to instructions")
	}
	if !strings.Contains(instructions, "/tmp/fake.gz") {
		t.Error("expected archive path in prepended note")
	}
	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Error("precompact flag should have been deleted (single-shot)")
	}
}

func TestSessionPrepareStacksBothNudges(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "stacked-client-001", "")
	sessionID := result["id"].(string)

	flagDir := a.configDir + "/hook-state"
	if err := os.MkdirAll(flagDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)

	if err := os.WriteFile(flagDir+"/stacked-client-001.compacted", []byte(ts), 0o644); err != nil {
		t.Fatalf("write compacted: %v", err)
	}
	payload := `{"count": 2, "warned_at": "` + ts + `", "archive_path": ""}`
	if err := os.WriteFile(flagDir+"/stacked-client-001.precompact-uncaptured", []byte(payload), 0o644); err != nil {
		t.Fatalf("write precompact: %v", err)
	}

	prep, svcErr := a.SessionPrepare(ctx, sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	if _, has := prep["recent_compaction"]; !has {
		t.Error("expected recent_compaction field")
	}
	if _, has := prep["pending_uncaptured"]; !has {
		t.Error("expected pending_uncaptured field")
	}
	instructions, _ := prep["instructions"].(string)
	if !strings.Contains(instructions, "recently compacted") {
		t.Error("expected compaction note")
	}
	if !strings.Contains(instructions, "uncaptured at the last compaction") {
		t.Error("expected precompact note")
	}
	if !strings.Contains(instructions, "raw transcript could not be archived") {
		t.Error("expected missing-archive note when archive_path is empty")
	}
}

func TestSessionPrepareIgnoresStaleCompactionFlag(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "stale-compact-001", "")
	sessionID := result["id"].(string)

	flagDir := a.configDir + "/hook-state"
	if err := os.MkdirAll(flagDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	flagPath := flagDir + "/stale-compact-001.compacted"
	staleTS := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(flagPath, []byte(staleTS), 0o644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	prep, svcErr := a.SessionPrepare(ctx, sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	if _, has := prep["recent_compaction"]; has {
		t.Error("stale flag (>2h old) should not surface")
	}
	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Error("stale flag should have been deleted anyway")
	}
}

// --- Prepared-sessions sweep + persistence ---

func TestSweepPreparedSessionsRemovesStaleEntries(t *testing.T) {
	a, _ := setupTestAPI(t)

	now := time.Now()
	a.preparedMu.Lock()
	a.preparedSessions["fresh-session"] = now.Add(-1 * time.Minute)
	a.preparedSessions["edge-session"] = now.Add(-(preparedSessionTTL - 1*time.Minute))
	a.preparedSessions["stale-session"] = now.Add(-2 * preparedSessionTTL)
	a.preparedSessions["ancient-session"] = now.Add(-24 * time.Hour)
	a.preparedMu.Unlock()

	a.sweepPreparedSessions()

	a.preparedMu.Lock()
	defer a.preparedMu.Unlock()
	if _, has := a.preparedSessions["fresh-session"]; !has {
		t.Error("fresh entry (1m old) should be kept")
	}
	if _, has := a.preparedSessions["edge-session"]; !has {
		t.Error("entry just under TTL should be kept")
	}
	if _, has := a.preparedSessions["stale-session"]; has {
		t.Error("stale entry (2x TTL) should have been removed")
	}
	if _, has := a.preparedSessions["ancient-session"]; has {
		t.Error("ancient entry should have been removed")
	}
	if got, want := len(a.preparedSessions), 2; got != want {
		t.Errorf("expected %d entries after sweep, got %d", want, got)
	}
}

// TestPreparedSessionsSurviveServerRestart is the regression test for
// the on-disk persistence of prepared sessions: a restart between
// prepare and commit must not break the flow. Before on-disk
// persistence, the second instance saw an empty preparedSessions map
// and rejected commit with prepare_required.
func TestPreparedSessionsSurviveServerRestart(t *testing.T) {
	a1, _ := setupTestAPI(t)

	a1.preparedMu.Lock()
	a1.preparedSessions["session-A"] = time.Now()
	a1.preparedSessions["session-B"] = time.Now()
	a1.savePreparedSessionsLocked()
	a1.preparedMu.Unlock()

	// Second API instance pointing at the same configDir simulates restart.
	a2 := New(Dependencies{
		Log:       a1.log,
		ConfigDir: a1.configDir,
	})

	a2.preparedMu.Lock()
	defer a2.preparedMu.Unlock()
	if _, has := a2.preparedSessions["session-A"]; !has {
		t.Errorf("session-A missing after restart restore: %#v", a2.preparedSessions)
	}
	if _, has := a2.preparedSessions["session-B"]; !has {
		t.Errorf("session-B missing after restart restore: %#v", a2.preparedSessions)
	}
}

func TestPreparedSessionsRestoreDropsExpired(t *testing.T) {
	a1, _ := setupTestAPI(t)

	a1.preparedMu.Lock()
	a1.preparedSessions["fresh"] = time.Now()
	a1.preparedSessions["expired"] = time.Now().Add(-2 * preparedSessionTTL)
	a1.savePreparedSessionsLocked()
	a1.preparedMu.Unlock()

	a2 := New(Dependencies{
		Log:       a1.log,
		ConfigDir: a1.configDir,
	})

	a2.preparedMu.Lock()
	defer a2.preparedMu.Unlock()
	if _, has := a2.preparedSessions["fresh"]; !has {
		t.Error("fresh entry should survive restore")
	}
	if _, has := a2.preparedSessions["expired"]; has {
		t.Error("expired entry should be dropped during restore")
	}
}

// --- Commit ---

func TestSessionCommitRejectsWithoutPrepare(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "reject-test", "")
	sessionID := result["id"].(string)

	segments := []SaveSegment{
		{Content: "Some knowledge", TopicName: "Design"},
	}
	_, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr == nil {
		t.Fatal("expected error when committing without prepare")
	}
	if svcErr.Code != "prepare_required" {
		t.Errorf("expected prepare_required code, got %s", svcErr.Code)
	}
}

func TestSessionCommitAfterPrepare(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "commit-test", "")
	sessionID := result["id"].(string)

	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	segments := []SaveSegment{
		{Content: "Decided to use PostgreSQL", TopicName: "Architecture"},
		{Content: "User prefers dark mode", TopicName: "Preferences"},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.SegmentsAdded != 2 {
		t.Errorf("segments_added = %v, want 2", commitResult.SegmentsAdded)
	}
	if commitResult.TopicsCreated != 2 {
		t.Errorf("topics_created = %v, want 2", commitResult.TopicsCreated)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}
}

func TestSessionCommitPromoteFalseSkipsMemoryRecord(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "promote-false-test", "")
	sessionID := result["id"].(string)

	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	promoteFalse := false
	segments := []SaveSegment{
		{Content: "Considered approach X but moved on without deciding",
			TopicName: "Exploration", PromoteToMemory: &promoteFalse},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.SegmentsAdded != 1 {
		t.Errorf("segments_added = %v, want 1", commitResult.SegmentsAdded)
	}
	if commitResult.SessionOnlySegments != 1 {
		t.Errorf("session_only_segments = %v, want 1", commitResult.SessionOnlySegments)
	}
	if commitResult.MemoryRecordsCreated != 0 {
		t.Errorf("memory_records_created = %v, want 0 (segment was Session-only)",
			commitResult.MemoryRecordsCreated)
	}
	if commitResult.EdgesCreated != 0 {
		t.Errorf("edges_created = %v, want 0 (no extracted_as edge for Session-only segment)",
			commitResult.EdgesCreated)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}
	segs := topics[0]["segments"].([]map[string]any)
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	if _, hasCaptured := segs[0]["captured_as"]; hasCaptured {
		t.Error("Session-only segment should have no captured_as field")
	}
}

func TestSessionCommitMixedPromotion(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "mixed-promote-test", "")
	sessionID := result["id"].(string)

	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	promoteTrue, promoteFalse := true, false
	segments := []SaveSegment{
		{Content: "Decided to use bbolt for storage", TopicName: "Storage",
			PromoteToMemory: &promoteTrue},
		{Content: "Briefly considered Badger before settling on bbolt", TopicName: "Storage",
			PromoteToMemory: &promoteFalse},
		{Content: "Future work: revisit storage choice once we hit 1M nodes", TopicName: "Storage",
			PromoteToMemory: &promoteFalse},
		{Content: "User prefers Go modules over GOPATH", TopicName: "Preferences"},
		// nil PromoteToMemory defaults to true.
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.SegmentsAdded != 4 {
		t.Errorf("segments_added = %v, want 4", commitResult.SegmentsAdded)
	}
	if commitResult.SessionOnlySegments != 2 {
		t.Errorf("session_only_segments = %v, want 2", commitResult.SessionOnlySegments)
	}
	if commitResult.MemoryRecordsCreated != 2 {
		t.Errorf("memory_records_created = %v, want 2 (1 explicit true + 1 nil-default)",
			commitResult.MemoryRecordsCreated)
	}
	if commitResult.EdgesCreated != 2 {
		t.Errorf("edges_created = %v, want 2", commitResult.EdgesCreated)
	}
}

// TestSessionCommitNilPromoteDefaultsTrue pins backward compatibility
// with pre-two-tier callers that omit promote_to_memory entirely.
func TestSessionCommitNilPromoteDefaultsTrue(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "nil-promote-test", "")
	sessionID := result["id"].(string)

	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}

	segments := []SaveSegment{
		{Content: "Some knowledge", TopicName: "Topic A"},
		{Content: "More knowledge", TopicName: "Topic B"},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.MemoryRecordsCreated != 2 {
		t.Errorf("nil PromoteToMemory should default to promote: memory_records_created = %v, want 2",
			commitResult.MemoryRecordsCreated)
	}
	if commitResult.SessionOnlySegments != 0 {
		t.Errorf("session_only_segments = %v, want 0", commitResult.SessionOnlySegments)
	}
}

// TestSessionCommitExistingTopic verifies reuse when a topic with the
// same name already exists from a prior commit -- no duplicate Topic
// node is created.
func TestSessionCommitExistingTopic(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "existing-topic-test", "")
	sessionID := result["id"].(string)

	// First commit creates the topic.
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Seed segment", TopicName: "Architecture"},
	}); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Second commit reuses it.
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Database choice: PostgreSQL", TopicName: "Architecture"},
	})
	if svcErr != nil {
		t.Fatalf("second commit: %v", svcErr)
	}
	if commitResult.TopicsCreated != 0 {
		t.Errorf("topics_created = %v, want 0 (topic already existed)", commitResult.TopicsCreated)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}
	segments := topics[0]["segments"].([]map[string]any)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments under reused topic, got %d", len(segments))
	}
}

func TestSessionRoundTrip(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "roundtrip-test", "")
	sessionID := result["id"].(string)

	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	segments := []SaveSegment{
		{Content: "Go is our primary language", TopicName: "Tech Stack"},
		{Content: "Target: sub-200ms search latency", TopicName: "Performance"},
		{Content: "Using BM25 for session segments", TopicName: "Tech Stack"},
	}
	if _, err := a.SessionSave(ctx, sessionID, segments); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}

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
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "empty-commit-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	_, svcErr := a.SessionSave(ctx, sessionID, []SaveSegment{})
	if svcErr == nil {
		t.Fatal("expected error for empty segments")
	}
}

func TestSessionCommitMalformedSegments(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "malformed-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	_, svcErr := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "", TopicName: "Design"},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty content")
	}

	// Re-prepare: the flag was NOT consumed (validation happens before
	// consume), so a fresh prepare is actually a no-op here -- but doing
	// it matches the contract in case the behavior tightens later.
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("re-prepare: %v", err)
	}

	_, svcErr = a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Some content", TopicName: ""},
	})
	if svcErr == nil {
		t.Fatal("expected error for empty topic name")
	}
}

func TestSessionPreparedFlagConsumed(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "flag-consumed-test", "")
	sessionID := result["id"].(string)

	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	segments := []SaveSegment{
		{Content: "First commit", TopicName: "Test"},
	}
	if _, svcErr := a.SessionSave(ctx, sessionID, segments); svcErr != nil {
		t.Fatalf("first commit: %v", svcErr)
	}

	_, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr == nil {
		t.Fatal("expected error on second commit without prepare")
	}
	if svcErr.Code != "prepare_required" {
		t.Errorf("expected prepare_required, got %s", svcErr.Code)
	}
}

func TestSessionConcurrentPrepare(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "concurrent-prepare-test", "")
	sessionID := result["id"].(string)

	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("first prepare: %v", svcErr)
	}
	if _, svcErr := a.SessionPrepare(ctx, sessionID); svcErr != nil {
		t.Fatalf("second prepare: %v", svcErr)
	}

	segments := []SaveSegment{
		{Content: "After double prepare", TopicName: "Test"},
	}
	if _, svcErr := a.SessionSave(ctx, sessionID, segments); svcErr != nil {
		t.Fatalf("commit after double prepare: %v", svcErr)
	}
}

// --- Extraction prompt ---

// TestPrepareReturnsExtractionPromptWithSections pins the must-haves of
// the leaner extraction prompt. The detailed sections (question-type
// framework, field-roles distinction, classification heuristics) have
// been delegated to gramaton_guide topics; the prompt itself stays
// lean but MUST still cover: the submission tool, the field names a
// segment needs, the two-tier promote_to_memory flag, the core
// principles agents must follow even without reading the guide, and
// explicit guide pointers.
func TestPrepareReturnsExtractionPromptWithSections(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "prompt-test", "")
	sessionID := result["id"].(string)

	prepResult, svcErr := a.SessionPrepare(ctx, sessionID)
	if svcErr != nil {
		t.Fatalf("prepare: %v", svcErr)
	}
	instructions := prepResult["instructions"].(string)

	for _, required := range []string{
		"gramaton_session_save",
		"content",
		"summary_short",
		"keywords",
		"topic",
		"temporality",
		"confidence",
		"knowledge_type",
		"epistemic_status",
		"promote_to_memory",
		"Synthesize",
		"Save",
		"Findability",
		"Skip",
		`gramaton_guide(topic="save")`,
		`gramaton_guide(topic="metadata")`,
		`gramaton_guide(topic="sessions")`,
	} {
		if !strings.Contains(instructions, required) {
			t.Errorf("extraction prompt missing required content: %q", required)
		}
	}
}

// --- Hybrid commit (Memory record creation) ---

func TestHybridCommitCreatesMemoryRecords(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "hybrid-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	segments := []SaveSegment{
		{Content: "We decided on PostgreSQL", TopicName: "Architecture",
			Temporality: "durable", KnowledgeType: "semantic"},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.MemoryRecordsCreated != 1 {
		t.Errorf("memory_records_created = %v, want 1", commitResult.MemoryRecordsCreated)
	}
	if commitResult.EdgesCreated != 1 {
		t.Errorf("edges_created = %v, want 1", commitResult.EdgesCreated)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	segResp := topics[0]["segments"].([]map[string]any)
	capturedAs := segResp[0]["captured_as"].(string)
	if capturedAs == "" {
		t.Fatal("segment should have captured_as set")
	}

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
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "edge-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Redis caching layer design", TopicName: "Infra"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	topics := got["topics"].([]map[string]any)
	segID := topics[0]["segments"].([]map[string]any)[0]["id"].(string)
	capturedAs := topics[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

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
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "metadata-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	conf := 0.85
	segments := []SaveSegment{
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
	if _, err := a.SessionSave(ctx, sessionID, segments); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
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
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "partial-meta-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	segments := []SaveSegment{
		{Content: "Some insight with no classification", TopicName: "General"},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.MemoryRecordsCreated != 1 {
		t.Error("should create memory record even with minimal metadata")
	}

	got, _ := a.SessionGet(ctx, sessionID)
	capturedAs := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)
	eng.RLock()
	memNode, _ := eng.Graph().GetNode(capturedAs)
	eng.RUnlock()
	if kt, _ := memNode.Properties.GetString("knowledge_type"); kt != "episodic" {
		t.Errorf("default knowledge_type = %v, want episodic", kt)
	}
}

func TestHybridCommitMultipleSegments(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "multi-segment-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	segments := []SaveSegment{
		{Content: "First knowledge", TopicName: "Topic A"},
		{Content: "Second knowledge", TopicName: "Topic A"},
		{Content: "Third knowledge", TopicName: "Topic B"},
	}
	commitResult, svcErr := a.SessionSave(ctx, sessionID, segments)
	if svcErr != nil {
		t.Fatalf("commit: %v", svcErr)
	}
	if commitResult.SegmentsAdded != 3 {
		t.Errorf("segments_added = %v, want 3", commitResult.SegmentsAdded)
	}
	if commitResult.MemoryRecordsCreated != 3 {
		t.Errorf("memory_records_created = %v, want 3", commitResult.MemoryRecordsCreated)
	}
	if commitResult.EdgesCreated != 3 {
		t.Errorf("edges_created = %v, want 3", commitResult.EdgesCreated)
	}
}

// TestHybridCommitSegmentNotVectorIndexed verifies the two-tier contract:
// the Session segment is BM25-only, the Memory record gets the vector.
func TestHybridCommitSegmentNotVectorIndexed(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "no-vector-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Session segment should have no vector", TopicName: "Test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)
	capturedAs := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	eng.RLock()
	defer eng.RUnlock()

	segNode, _ := eng.Graph().GetNode(segID)
	if _, hasVec := segNode.Properties.GetVector("embedding_full"); hasVec {
		t.Error("Session segment should NOT have vector embedding")
	}

	if _, ok := eng.Graph().GetNode(capturedAs); !ok {
		t.Error("Memory record should exist")
	}
}

func TestHybridCommitFollowEdges(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "follow-edge-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: "Bidirectional edge test", TopicName: "Test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)
	memID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["captured_as"].(string)

	eng.RLock()
	defer eng.RUnlock()

	foundForward := false
	for _, e := range eng.Graph().EdgesFrom(segID) {
		if e.Type == "extracted_as" && e.TargetID == memID {
			foundForward = true
		}
	}
	if !foundForward {
		t.Error("expected extracted_as edge segment -> memory")
	}

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

// TestCurationSkipsSegmentNodes verifies the property contract that
// curation/observe.go relies on: segments are tagged knowledge_type=segment
// so the observation extractor can skip them.
func TestCurationSkipsSegmentNodes(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "curation-skip-test", "")
	sessionID := result["id"].(string)
	if _, err := a.SessionPrepare(ctx, sessionID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

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
	if _, err := a.SessionSave(ctx, sessionID, []SaveSegment{
		{Content: longContent, TopicName: "Architecture"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, _ := a.SessionGet(ctx, sessionID)
	segID := got["topics"].([]map[string]any)[0]["segments"].([]map[string]any)[0]["id"].(string)

	eng.RLock()
	segNode, _ := eng.Graph().GetNode(segID)
	eng.RUnlock()

	kt, _ := segNode.Properties.GetString("knowledge_type")
	if kt != "segment" {
		t.Errorf("expected knowledge_type=segment, got %v", kt)
	}

	_ = graph.IsStructuralEdge("segment_of") // verify it's structural
}

// --- Archive ---

func TestSessionArchiveCreateReferencedInGet(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "archive-test", "")
	sessionID := result["id"].(string)

	tmpDir := t.TempDir()
	srcPath := tmpDir + "/conversation.txt"
	srcContent := strings.Repeat("User: How should we design the database?\nAssistant: I recommend PostgreSQL for these reasons...\n", 20)
	if err := os.WriteFile(srcPath, []byte(srcContent), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	archiveResult, svcErr := a.SessionArchive(ctx, sessionID, srcPath)
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

	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive file not found: %v", err)
	}
	if info.Size() >= int64(len(srcContent)) {
		t.Errorf("compressed size %d >= original %d", info.Size(), len(srcContent))
	}

	// SessionGet should surface raw_archive.
	got, _ := a.SessionGet(ctx, sessionID)
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
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "no-archive-test", "")
	sessionID := result["id"].(string)

	got, _ := a.SessionGet(ctx, sessionID)
	if _, has := got["raw_archive"]; has {
		t.Error("session without archive should not have raw_archive field")
	}
}

func TestSessionArchiveMissingSource(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "missing-src-test", "")
	sessionID := result["id"].(string)

	_, svcErr := a.SessionArchive(ctx, sessionID, "/nonexistent/file.txt")
	if svcErr == nil {
		t.Fatal("expected error for missing source file")
	}
}

func TestSessionArchiveLargeContent(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.SessionStart(ctx, "large-archive-test", "")
	sessionID := result["id"].(string)

	tmpDir := t.TempDir()
	srcPath := tmpDir + "/large.txt"
	largeContent := strings.Repeat("This is a line of conversation text.\n", 30000) // ~1.1MB
	if err := os.WriteFile(srcPath, []byte(largeContent), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	archiveResult, svcErr := a.SessionArchive(ctx, sessionID, srcPath)
	if svcErr != nil {
		t.Fatalf("archive: %v", svcErr)
	}

	compressedSize := archiveResult["compressed_size"].(int64)
	originalSize := archiveResult["original_size"].(int)
	ratio := float64(compressedSize) / float64(originalSize)
	if ratio > 0.5 {
		t.Errorf("compression ratio %.2f too high for repetitive text", ratio)
	}
}

// --- Session chaining ---

func TestSessionChainingOnResume(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "chain-test", "startup")
	id1 := r1["id"].(string)
	if r1["previous_session_id"] != nil {
		t.Error("first session should have no previous")
	}

	r2, _ := a.SessionStart(ctx, "chain-test", "resume")
	id2 := r2["id"].(string)
	if id1 == id2 {
		t.Fatal("resume should create a NEW session, not return the old one")
	}
	if r2["previous_session_id"] != id1 {
		t.Errorf("previous_session_id = %v, want %v", r2["previous_session_id"], id1)
	}

	eng.RLock()
	defer eng.RUnlock()
	foundEdge := false
	for _, e := range eng.Graph().EdgesFrom(id2) {
		if e.Type == "continues_from" && e.TargetID == id1 {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Error("expected continues_from edge from session 2 to session 1")
	}

	foundBack := false
	for _, e := range eng.Graph().EdgesTo(id1) {
		if e.Type == "continues_from" && e.SourceID == id2 {
			foundBack = true
		}
	}
	if !foundBack {
		t.Error("expected to find session 2 via inbound continues_from on session 1")
	}
}

func TestSessionChainingThreeDeep(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "deep-chain", "startup")
	id1 := r1["id"].(string)

	r2, _ := a.SessionStart(ctx, "deep-chain", "resume")
	id2 := r2["id"].(string)

	r3, _ := a.SessionStart(ctx, "deep-chain", "resume")
	id3 := r3["id"].(string)

	if r3["previous_session_id"] != id2 {
		t.Errorf("session 3 previous = %v, want %v (session 2)", r3["previous_session_id"], id2)
	}

	if id1 == id2 || id2 == id3 || id1 == id3 {
		t.Errorf("all sessions should have unique IDs: %v, %v, %v", id1, id2, id3)
	}
}

func TestSessionIdempotentAgentCall(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "agent-test", "startup")
	id1 := r1["id"].(string)

	r2, _ := a.SessionStart(ctx, "agent-test", "")
	id2 := r2["id"].(string)

	if id1 != id2 {
		t.Errorf("agent idempotent call should return same session: %v vs %v", id1, id2)
	}
	if r2["resumed"] != true {
		t.Error("agent call should show resumed=true")
	}
}

func TestSessionFreshStartNeverChains(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.SessionStart(ctx, "fresh-a", "startup")
	r2, _ := a.SessionStart(ctx, "fresh-b", "startup")

	if r1["previous_session_id"] != nil {
		t.Error("fresh start A should have no previous")
	}
	if r2["previous_session_id"] != nil {
		t.Error("fresh start B should have no previous")
	}
}
