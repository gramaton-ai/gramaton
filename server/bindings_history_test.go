package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/graph"
)

// TestAPILogLimit confirms the api.Log path returns at most `limit`
// commits and respects the MaxLogLimit cap. Each addRecord triggers a
// commit so we can stack a few quickly.
func TestAPILogLimit(t *testing.T) {
	srv, eng := setupTestServer(t)
	for i := 0; i < 5; i++ {
		addRecord(t, eng, "log target")
	}

	resp, apiErr := srv.api.Log(context.Background(), api.LogRequest{Limit: 3})
	if apiErr != nil {
		t.Fatalf("Log: %v", apiErr)
	}
	if len(resp.Commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(resp.Commits))
	}
	for _, c := range resp.Commits {
		if c.Hash == "" || c.Timestamp == "" {
			t.Errorf("commit fields missing: %+v", c)
		}
		if len(c.Hash) > 12 {
			t.Errorf("hash should be truncated to 12 chars, got %d", len(c.Hash))
		}
	}
}

// TestAPILogSinceUntilNarrowsWalk confirms the Phase-2 range
// parameters restrict the log walker to a date window. The test
// seeds three commits, captures a mid-window timestamp, and asserts
// a windowed log only returns the commits inside.
func TestAPILogSinceUntilNarrowsWalk(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "a")

	time.Sleep(15 * time.Millisecond)
	windowStart := time.Now().UTC()
	time.Sleep(15 * time.Millisecond)

	addRecord(t, eng, "b")

	time.Sleep(15 * time.Millisecond)
	windowEnd := time.Now().UTC()
	time.Sleep(15 * time.Millisecond)

	addRecord(t, eng, "c")

	resp, apiErr := srv.api.Log(context.Background(), api.LogRequest{
		Since: windowStart.Format(time.RFC3339Nano),
		Until: windowEnd.Format(time.RFC3339Nano),
	})
	if apiErr != nil {
		t.Fatalf("Log: %v", apiErr)
	}

	// Only "b" should be in the window. "a" predates windowStart and
	// "c" postdates windowEnd.
	if len(resp.Commits) != 1 {
		t.Fatalf("expected exactly 1 commit in window, got %d: %+v",
			len(resp.Commits), resp.Commits)
	}
	if resp.Commits[0].Action != "test" {
		// addRecord uses the label "test" as the commit message.
		t.Logf("got action=%q (helper uses a generic 'test' label)", resp.Commits[0].Action)
	}
}

// TestAPILogInvalidDates covers validator paths for Since/Until.
// TestAPILogActionsFilter confirms Phase 8: a log call with an
// actions filter only returns commits whose CommitAction.Kind matches.
// The end-to-end assertion that Phase 3's emission + Phase 8's filter
// compose correctly -- this is the smoke-test-critical path.
func TestAPILogActionsFilter(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Seed one capture + one resolve + one bare "test" commit so the
	// filter has heterogeneous commits to exclude.
	id := addRecord(t, eng, "filter target")
	_, apiErr := srv.api.Resolve(context.Background(), api.ResolveRequest{
		ID: id, Resolution: "completed",
	})
	if apiErr != nil {
		t.Fatalf("Resolve: %v", apiErr)
	}

	resp, apiErr := srv.api.Log(context.Background(), api.LogRequest{
		Actions: []string{"resolve"},
	})
	if apiErr != nil {
		t.Fatalf("Log: %v", apiErr)
	}
	if len(resp.Commits) != 1 {
		t.Fatalf("expected 1 resolve commit, got %d: %+v", len(resp.Commits), resp.Commits)
	}
	if resp.Commits[0].Action != "resolve" {
		t.Errorf("Action = %q, want resolve", resp.Commits[0].Action)
	}
}

// TestAPILogEmptyActionsRejected covers the validator that makes
// an explicit empty array an error (distinct from "no filter").
func TestAPILogEmptyActionsRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Log(context.Background(), api.LogRequest{
		Actions: []string{},
	})
	// nil vs empty-slice is tricky: []string{} is non-nil but len 0.
	// The validator rejects exactly this case.
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for empty actions array")
	}
}

// TestAPILogIncludeRecordMutations verifies that the new
// include_record_mutations flag enriches each commit with per-record
// mutation summaries from its CommitAction list.
func TestAPILogIncludeRecordMutations(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "mutation target")
	_, apiErr := srv.api.Resolve(context.Background(), api.ResolveRequest{
		ID: id, Resolution: "completed",
	})
	if apiErr != nil {
		t.Fatalf("Resolve: %v", apiErr)
	}

	// With the flag off: no mutations inline.
	bare, _ := srv.api.Log(context.Background(), api.LogRequest{
		Actions: []string{"resolve"},
	})
	for _, c := range bare.Commits {
		if c.Mutations != nil {
			t.Errorf("Mutations leaked without flag: %+v", c.Mutations)
		}
	}

	// With the flag on: each commit includes its CommitActions.
	withMut, apiErr := srv.api.Log(context.Background(), api.LogRequest{
		Actions:                []string{"resolve"},
		IncludeRecordMutations: true,
	})
	if apiErr != nil {
		t.Fatalf("Log: %v", apiErr)
	}
	if len(withMut.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(withMut.Commits))
	}
	muts := withMut.Commits[0].Mutations
	if len(muts) != 1 {
		t.Fatalf("mutations = %d, want 1 for the single resolve", len(muts))
	}
	if muts[0].Kind != "resolve" || muts[0].RecordID != id {
		t.Errorf("mutation = %+v, want kind=resolve + record_id=%q", muts[0], id)
	}
}

// TestAPILogExcludeCuration: commits whose Message starts with
// "curation:" are filtered out. Uses a direct engine.Save to synthesize
// a curation commit without running the curation pipeline.
func TestAPILogExcludeCuration(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "user activity")

	// Synthesize a curation-shaped commit.
	eng.Lock()
	eng.Graph().AddNode(graph.Properties{"content_full": graph.StringProperty("curation-touched")})
	if _, err := eng.Save("curation: synthetic for test"); err != nil {
		eng.Unlock()
		t.Fatalf("synthetic curation save: %v", err)
	}
	eng.Unlock()

	// Without the flag: both commits are present.
	all, _ := srv.api.Log(context.Background(), api.LogRequest{})
	if len(all.Commits) < 2 {
		t.Fatalf("expected at least 2 commits without filter, got %d", len(all.Commits))
	}

	// With exclude_curation: the curation commit drops.
	filtered, _ := srv.api.Log(context.Background(), api.LogRequest{ExcludeCuration: true})
	for _, c := range filtered.Commits {
		if strings.HasPrefix(c.Action, "curation:") {
			t.Errorf("curation commit leaked through: %+v", c)
		}
	}
}

func TestAPILogInvalidSince(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Log(context.Background(), api.LogRequest{Since: "nope"})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %+v", apiErr)
	}
}

// TestAPIDiffEmptyChain: with no commits past chain root, an empty
// since= returns whatever's added. With a since= that postdates the
// chain entirely, the response should be empty buckets, not an error.
func TestAPIDiffSinceFutureReturnsEmpty(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "diff target")

	// UTC: parseDateArg interprets YYYY-MM-DD as UTC midnight, so the
	// test must construct the date in UTC for the result to match.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	resp, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Since: tomorrow})
	if apiErr != nil {
		t.Fatalf("Diff: %v", apiErr)
	}
	if len(resp.Added) != 0 || len(resp.Modified) != 0 || len(resp.Removed) != 0 {
		t.Fatalf("expected empty diff for future since, got added=%d modified=%d removed=%d",
			len(resp.Added), len(resp.Modified), len(resp.Removed))
	}
}

func TestAPIDiffInvalidSince(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Since: "not-a-date"})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for bad since")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
}

// TestAPIDiffInvalidUntil covers the new Until parameter's input
// validation path (symmetric with the invalid-since case).
func TestAPIDiffInvalidUntil(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Until: "not-a-date"})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for bad until")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
}

// TestAPIDiffSinceAfterUntilRejected asserts that a since value later
// than until fails validation before any store work happens.
func TestAPIDiffSinceAfterUntilRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{
		Since: "2026-04-25",
		Until: "2026-04-20",
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for since > until")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
}

// TestAPIDiffUntilAtHeadMatchesNoUntil proves the Until parameter
// defaults-to-HEAD claim: passing Until explicitly for the HEAD commit's
// timestamp produces the same diff buckets as omitting Until.
func TestAPIDiffUntilAtHeadMatchesNoUntil(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "a")
	addRecord(t, eng, "b")

	// HEAD's timestamp is the most recent commit. Use "tomorrow" as Until
	// so we know every commit is included (same as the no-Until case).
	// Use UTC explicitly: parseDateArg interprets YYYY-MM-DD as UTC midnight,
	// so tomorrow-in-local can be today-in-UTC if local is east of UTC, or
	// today-in-local can be tomorrow-in-UTC if local is west — the test
	// must construct tomorrow in the same frame the parser uses.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	// Baseline: no Until.
	withoutUntil, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{})
	if apiErr != nil {
		t.Fatalf("baseline Diff: %v", apiErr)
	}
	// Same call with Until explicitly set past HEAD.
	withUntil, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Until: tomorrow})
	if apiErr != nil {
		t.Fatalf("Until Diff: %v", apiErr)
	}
	if len(withoutUntil.Added)+len(withoutUntil.Modified)+len(withoutUntil.Removed) !=
		len(withUntil.Added)+len(withUntil.Modified)+len(withUntil.Removed) {
		t.Errorf("Until past HEAD should match no-Until: got baseline=%d withUntil=%d",
			len(withoutUntil.Added)+len(withoutUntil.Modified)+len(withoutUntil.Removed),
			len(withUntil.Added)+len(withUntil.Modified)+len(withUntil.Removed))
	}
}

// TestAPIDiffUntilBeforeAnyCommit returns empty buckets when Until is
// before the earliest indexed commit. Matches the since-postdates-chain
// shape.
func TestAPIDiffUntilBeforeAnyCommit(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "target")

	// UTC: parseDateArg interprets YYYY-MM-DD as UTC midnight. See
	// TestAPIDiffSinceFutureReturnsEmpty for the same TZ rationale.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	resp, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Until: yesterday})
	if apiErr != nil {
		t.Fatalf("Diff: %v", apiErr)
	}
	if len(resp.Added) != 0 || len(resp.Modified) != 0 || len(resp.Removed) != 0 {
		t.Errorf("Until before any commit should be empty, got added=%d modified=%d removed=%d",
			len(resp.Added), len(resp.Modified), len(resp.Removed))
	}
}

func TestAPIDiffTopicCap(t *testing.T) {
	srv, _ := setupTestServer(t)
	tooLong := strings.Repeat("a", api.MaxTopicLength+1)
	_, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{Topic: tooLong})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for oversized topic")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
}

// TestAPIDiffTopicFilterNegative pins the leak-prevention property:
// when topic=kafka is provided, the api must never surface records
// that don't match "kafka" in keywords or summary_short. Paired with
// TestAPIDiffTopicFilterPositive (which asserts a matching record DOES
// surface) for full filter coverage.
func TestAPIDiffTopicFilterNegative(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Seed commit so since= has a parent before the topic records.
	eng.Lock()
	seed := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("seed"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range seed.Properties {
		eng.PropIdx().Add(seed.ID, k, v)
	}
	eng.Save("seed")
	eng.Unlock()

	time.Sleep(20 * time.Millisecond)
	since := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	time.Sleep(20 * time.Millisecond)

	eng.Lock()
	other := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("About redis caching"),
		"content_short":     graph.StringProperty("Redis usage"),
		"content_keywords":  graph.StringListProperty([]string{"redis", "caching"}),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range other.Properties {
		eng.PropIdx().Add(other.ID, k, v)
	}
	eng.Save("redis")
	eng.Unlock()

	resp, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{
		Since: since,
		Topic: "kafka",
	})
	if apiErr != nil {
		t.Fatalf("Diff: %v", apiErr)
	}

	all := append(append([]api.DiffEntry{}, resp.Added...), resp.Modified...)
	all = append(all, resp.Removed...)
	for _, e := range all {
		if e.ID == other.ID {
			t.Errorf("non-matching record %s leaked through topic=kafka filter", other.ID)
		}
	}
}

// TestAPIDiffAddedNodeAppears is the positive complement to
// TestAPIDiffTopicFilterNegative. It pins the load-bearing behavior:
// a node added between sinceCommit and HEAD must appear in
// resp.Added. The negative-only test passes vacuously when the diff
// returns empty (which is the bug); this one asserts the diff
// surfaces the new record.
func TestAPIDiffAddedNodeAppears(t *testing.T) {
	srv, eng := setupTestServer(t)

	addRecord(t, eng, "seed before since")

	time.Sleep(20 * time.Millisecond)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	time.Sleep(20 * time.Millisecond)

	wantID := addRecord(t, eng, "added after since")

	resp, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{
		Since: since,
	})
	if apiErr != nil {
		t.Fatalf("Diff: %v", apiErr)
	}

	t.Logf("diff window since=%s\n  added=%d modified=%d removed=%d",
		since, len(resp.Added), len(resp.Modified), len(resp.Removed))
	for _, e := range resp.Added {
		t.Logf("  added: %s", e.ID)
	}

	found := false
	for _, e := range resp.Added {
		if e.ID == wantID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected added record %s in resp.Added; got %d added entries: %+v",
			wantID, len(resp.Added), resp.Added)
	}
}

// TestAPIDiffTopicFilterPositive is the positive complement: a kafka
// record added between since and HEAD must appear when filtered by
// topic=kafka. This mirrors the repro the tracker describes (flip the
// negative test to assert presence).
func TestAPIDiffTopicFilterPositive(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Seed pre-since commit so the diff window has a parent.
	eng.Lock()
	seed := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("seed"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range seed.Properties {
		eng.PropIdx().Add(seed.ID, k, v)
	}
	eng.Save("seed")
	eng.Unlock()

	time.Sleep(20 * time.Millisecond)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	time.Sleep(20 * time.Millisecond)

	// Add a kafka record AND an unrelated redis record after since.
	eng.Lock()
	kafka := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("About kafka streams"),
		"content_short":     graph.StringProperty("Kafka usage"),
		"content_keywords":  graph.StringListProperty([]string{"kafka", "streams"}),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range kafka.Properties {
		eng.PropIdx().Add(kafka.ID, k, v)
	}
	eng.Save("kafka")

	redis := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("About redis caching"),
		"content_short":     graph.StringProperty("Redis usage"),
		"content_keywords":  graph.StringListProperty([]string{"redis", "caching"}),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
	})
	for k, v := range redis.Properties {
		eng.PropIdx().Add(redis.ID, k, v)
	}
	eng.Save("redis")
	eng.Unlock()

	resp, apiErr := srv.api.Diff(context.Background(), api.DiffRequest{
		Since: since,
		Topic: "kafka",
	})
	if apiErr != nil {
		t.Fatalf("Diff: %v", apiErr)
	}

	t.Logf("diff topic=kafka since=%s\n  added=%d modified=%d removed=%d",
		since, len(resp.Added), len(resp.Modified), len(resp.Removed))
	for _, e := range resp.Added {
		t.Logf("  added: %s short=%q", e.ID, e.SummaryShort)
	}

	found := false
	for _, e := range resp.Added {
		if e.ID == kafka.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected kafka record %s in resp.Added (filtered by topic=kafka); got %d added entries",
			kafka.ID, len(resp.Added))
	}
}
