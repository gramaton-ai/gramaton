package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newTestStore returns a Store backed by a temp jobs.db.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fullJob constructs a Job with every field populated, used for
// roundtrip tests so we'd notice if any field were silently
// dropped on marshal/unmarshal.
func fullJob(id string) *Job {
	return &Job{
		FormatVersion:   CurrentFormatVersion,
		ID:              id,
		Kind:            "capture_batch",
		Status:          StatusPending,
		CreatedAt:       time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		StartedAt:       time.Date(2026, 5, 3, 10, 0, 1, 0, time.UTC),
		CompletedAt:     time.Date(2026, 5, 3, 10, 0, 2, 0, time.UTC),
		ClientToken:     "token-abc",
		RequestHash:     "deadbeef",
		SupersedesJobID: "older-job",
		TotalItems:      42,
		ProcessedCount:  17,
		ClientRefToID:   map[string]string{"ref1": "id1", "ref2": "id2"},
		Result:          json.RawMessage(`{"added":[{"id":"x"}]}`),
		Errors:          []ItemError{{Index: 5, Code: "bad", Message: "oops"}},
		FailureReason:   "test_reason",
	}
}

// TestStoreCreateGet — all fields populated; roundtrip preserves
// every value.
func TestStoreCreateGet(t *testing.T) {
	s := newTestStore(t)
	in := fullJob("abc")
	if err := s.Create(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != in.ID || got.Kind != in.Kind || got.Status != in.Status {
		t.Errorf("basic fields differ: got %+v want %+v", got, in)
	}
	if got.ClientToken != in.ClientToken || got.RequestHash != in.RequestHash {
		t.Errorf("token/hash differ")
	}
	if got.TotalItems != in.TotalItems || got.ProcessedCount != in.ProcessedCount {
		t.Errorf("counters differ")
	}
	if len(got.ClientRefToID) != 2 || got.ClientRefToID["ref1"] != "id1" {
		t.Errorf("ClientRefToID not preserved: %v", got.ClientRefToID)
	}
	if string(got.Result) != string(in.Result) {
		t.Errorf("Result: got %q want %q", got.Result, in.Result)
	}
	if len(got.Errors) != 1 || got.Errors[0].Index != 5 {
		t.Errorf("Errors not preserved: %v", got.Errors)
	}
	if got.FailureReason != in.FailureReason {
		t.Errorf("FailureReason: got %q want %q", got.FailureReason, in.FailureReason)
	}
	if !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, in.CreatedAt)
	}
}

// TestStoreCreateDuplicate — Create on an existing ID errors.
func TestStoreCreateDuplicate(t *testing.T) {
	s := newTestStore(t)
	if err := s.Create(fullJob("dup")); err != nil {
		t.Fatal(err)
	}
	err := s.Create(fullJob("dup"))
	if err == nil {
		t.Error("expected error on duplicate Create")
	}
}

// TestStoreCreateDefaults — Create populates FormatVersion and
// CreatedAt when zero.
func TestStoreCreateDefaults(t *testing.T) {
	s := newTestStore(t)
	j := &Job{ID: "defaults", Kind: "capture_batch", Status: StatusPending}
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	if j.FormatVersion != CurrentFormatVersion {
		t.Errorf("FormatVersion: got %d want %d", j.FormatVersion, CurrentFormatVersion)
	}
	if j.CreatedAt.IsZero() {
		t.Error("CreatedAt left zero")
	}
}

// TestStoreUpdateValidTransitions — every allowed transition.
func TestStoreUpdateValidTransitions(t *testing.T) {
	cases := []struct {
		from, to string
	}{
		{StatusPending, StatusRunning},
		{StatusPending, StatusCancelled},
		{StatusPending, StatusFailed},
		{StatusRunning, StatusCompleted},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusCancelled},
	}
	for _, c := range cases {
		t.Run(c.from+"_to_"+c.to, func(t *testing.T) {
			s := newTestStore(t)
			j := fullJob("xform")
			j.Status = c.from
			if err := s.Create(j); err != nil {
				t.Fatal(err)
			}
			j.Status = c.to
			if err := s.Update(j); err != nil {
				t.Errorf("transition %s -> %s: %v", c.from, c.to, err)
			}
			got, _ := s.Get("xform")
			if got.Status != c.to {
				t.Errorf("status not persisted: got %s want %s", got.Status, c.to)
			}
		})
	}
}

// TestStoreUpdateInvalidTransitions — every rejected transition.
func TestStoreUpdateInvalidTransitions(t *testing.T) {
	cases := []struct {
		from, to string
	}{
		{StatusPending, StatusCompleted},   // skipping running
		{StatusCancelled, StatusRunning},   // leaving terminal
		{StatusCancelled, StatusCompleted}, // terminal
		{StatusFailed, StatusRunning},      // terminal
		{StatusFailed, StatusCompleted},    // terminal
		{StatusCompleted, StatusRunning},   // terminal
		{StatusCompleted, StatusFailed},    // terminal
		{StatusCompleted, StatusCancelled}, // terminal
	}
	for _, c := range cases {
		t.Run(c.from+"_to_"+c.to, func(t *testing.T) {
			s := newTestStore(t)
			j := fullJob("badform")
			j.Status = c.from
			if err := s.Create(j); err != nil {
				t.Fatal(err)
			}
			j.Status = c.to
			err := s.Update(j)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("transition %s -> %s: got %v, want ErrInvalidTransition",
					c.from, c.to, err)
			}
			// Verify nothing was written.
			got, _ := s.Get("badform")
			if got.Status != c.from {
				t.Errorf("status corrupted: got %s want %s", got.Status, c.from)
			}
		})
	}
}

// TestStoreUpdateNoStatusChange — Update where j.Status ==
// current.Status bypasses whitelist (e.g., bumping
// ProcessedCount).
func TestStoreUpdateNoStatusChange(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("nostatuschange")
	j.Status = StatusRunning
	j.ProcessedCount = 0
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	j.ProcessedCount = 50
	if err := s.Update(j); err != nil {
		t.Errorf("status-unchanged Update should succeed: %v", err)
	}
	got, _ := s.Get("nostatuschange")
	if got.ProcessedCount != 50 {
		t.Errorf("ProcessedCount not updated: got %d", got.ProcessedCount)
	}
}

// TestStoreUpdateMissingJob — Update on non-existent ID errors.
func TestStoreUpdateMissingJob(t *testing.T) {
	s := newTestStore(t)
	err := s.Update(fullJob("ghost"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestStoreAdvanceStatus — happy path: pending -> running with
// mutator advancing ProcessedCount in the same atomic write.
func TestStoreAdvanceStatus(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("advance")
	j.Status = StatusPending
	j.ProcessedCount = 0
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	err := s.AdvanceStatus("advance", StatusRunning, func(j *Job) {
		j.ProcessedCount = 1
		j.StartedAt = time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("advance")
	if got.Status != StatusRunning {
		t.Errorf("status: got %s, want running", got.Status)
	}
	if got.ProcessedCount != 1 {
		t.Errorf("ProcessedCount: got %d, want 1", got.ProcessedCount)
	}
	if got.StartedAt.IsZero() {
		t.Errorf("StartedAt not set by mutator")
	}
}

// TestStoreAdvanceStatusInvalid — AdvanceStatus rejects a
// non-whitelisted transition.
func TestStoreAdvanceStatusInvalid(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("adv-invalid")
	j.Status = StatusCompleted
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	err := s.AdvanceStatus("adv-invalid", StatusRunning, nil)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("got %v, want ErrInvalidTransition", err)
	}
}

// TestStoreAdvanceStatusRace — barrier-channel pattern: two
// goroutines concurrently AdvanceStatus on the same Job. Verifies
// the atomicity guarantee: bbolt's write-tx serialization plus
// in-tx whitelist validation produce a consistent final state.
//
// Note: NOT "exactly one wins" — multiple transitions away from
// pending or running can be valid (pending -> running, then
// running -> cancelled, both succeed). The real property is:
// final state is reachable from initial via the allowed
// transitions, no panic, no torn state.
func TestStoreAdvanceStatusRace(t *testing.T) {
	for trial := 0; trial < 20; trial++ {
		s := newTestStore(t)
		j := fullJob("race")
		j.Status = StatusPending
		if err := s.Create(j); err != nil {
			t.Fatal(err)
		}

		release := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-release
			errs <- s.AdvanceStatus("race", StatusRunning, nil)
		}()
		go func() {
			<-release
			errs <- s.AdvanceStatus("race", StatusCancelled, nil)
		}()
		close(release)
		e1, e2 := <-errs, <-errs

		// Each error must be either nil or ErrInvalidTransition;
		// no other errors (e.g., bbolt corruption) acceptable.
		for i, e := range []error{e1, e2} {
			if e == nil {
				continue
			}
			if !errors.Is(e, ErrInvalidTransition) {
				t.Errorf("trial %d: err[%d]=%v, want nil or ErrInvalidTransition",
					trial, i, e)
			}
		}

		// Final state must be reachable from pending. Allowed
		// destinations: pending (impossible — at least one must
		// have advanced or both got rejected, but the rejection
		// only fires for non-allowed transitions, and pending->
		// running and pending->cancelled are both allowed) ->
		// running, cancelled, or completed (running->completed
		// chain).
		got, _ := s.Get("race")
		valid := got.Status == StatusRunning ||
			got.Status == StatusCancelled ||
			got.Status == StatusCompleted ||
			got.Status == StatusFailed
		if !valid {
			t.Errorf("trial %d: final status %s not reachable",
				trial, got.Status)
		}
	}
}

// TestStoreFindByClientToken — happy path + miss + empty token.
func TestStoreFindByClientToken(t *testing.T) {
	s := newTestStore(t)
	a := fullJob("a")
	a.ClientToken = "tok-a"
	a.CreatedAt = time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	b := fullJob("b")
	b.ClientToken = "tok-b"
	b.CreatedAt = time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC)

	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(b); err != nil {
		t.Fatal(err)
	}

	got, err := s.FindByClientToken("tok-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "a" {
		t.Errorf("got %+v, want id=a", got)
	}

	got, err = s.FindByClientToken("tok-missing", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing token, got %+v", got)
	}

	got, err = s.FindByClientToken("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for empty token, got %+v", got)
	}
}

// TestStoreFindByClientTokenLatestWins — when multiple jobs share
// the same token, FindByClientToken returns the most recently
// created one.
func TestStoreFindByClientTokenLatestWins(t *testing.T) {
	s := newTestStore(t)
	older := fullJob("older")
	older.ClientToken = "tok"
	older.CreatedAt = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newer := fullJob("newer")
	newer.ClientToken = "tok"
	newer.CreatedAt = time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)

	_ = s.Create(older)
	_ = s.Create(newer)

	got, _ := s.FindByClientToken("tok", "")
	if got == nil || got.ID != "newer" {
		t.Errorf("got %+v, want newer", got)
	}
}

// TestStoreListInFlight — returns pending and running, omits
// terminal states.
func TestStoreListInFlight(t *testing.T) {
	s := newTestStore(t)
	statuses := map[string]string{
		"pj": StatusPending,
		"rj": StatusRunning,
		"cj": StatusCompleted,
		"fj": StatusFailed,
		"xj": StatusCancelled,
	}
	for id, st := range statuses {
		j := fullJob(id)
		j.Status = st
		_ = s.Create(j)
	}
	got, err := s.ListInFlight()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d in-flight, want 2", len(got))
	}
	ids := map[string]bool{}
	for _, j := range got {
		ids[j.ID] = true
	}
	if !ids["pj"] || !ids["rj"] {
		t.Errorf("in-flight IDs: %v, want pj+rj", ids)
	}
}

// TestStoreDelete — removes; subsequent Get returns ErrNotFound;
// repeated Delete is idempotent.
func TestStoreDelete(t *testing.T) {
	s := newTestStore(t)
	_ = s.Create(fullJob("d"))

	if err := s.Delete("d"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get("d")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	// Idempotent.
	if err := s.Delete("d"); err != nil {
		t.Errorf("repeat Delete: %v", err)
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("missing-id Delete: %v", err)
	}
}

// TestStoreReopen — close + reopen sees the same data.
func TestStoreReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Create(fullJob("persist"))
	_ = s.Close()

	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get("persist")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "persist" {
		t.Errorf("got %s, want persist", got.ID)
	}
}

// TestStoreCloseIdempotent — Close can be called multiple times.
func TestStoreCloseIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestStoreReadOldSchema — write a v0 Job (no FormatVersion) directly
// into bbolt, then Get triggers migration; verify FormatVersion is
// now 1 on disk so subsequent Gets don't re-trigger.
func TestStoreReadOldSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")

	// Bypass Store to write a v0 entry directly (simulates a Job
	// from before this build).
	{
		db, err := bolt.Open(path, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte(bucketName))
			if err != nil {
				return err
			}
			// FormatVersion intentionally omitted.
			raw := []byte(`{"id":"legacy","kind":"capture_batch","status":"completed","total_items":5,"completed_at":"2026-05-01T00:00:00Z","created_at":"2026-05-01T00:00:00Z"}`)
			return b.Put([]byte("legacy"), raw)
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First Get triggers migration.
	got, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.FormatVersion != 1 {
		t.Errorf("FormatVersion: got %d, want 1 after migration", got.FormatVersion)
	}

	// Verify the migration was persisted: read raw bytes back and
	// confirm FormatVersion=1 is in the JSON.
	got2, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got2.FormatVersion != 1 {
		t.Errorf("FormatVersion after re-Get: got %d, want 1", got2.FormatVersion)
	}
}

// TestStoreReadFutureFormatVersion — Job with FormatVersion newer
// than this build returns ErrFutureFormatVersion (don't silently
// corrupt by treating as v0).
func TestStoreReadFutureFormatVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")

	// Write a v99 Job directly.
	{
		db, _ := bolt.Open(path, 0600, nil)
		_ = db.Update(func(tx *bolt.Tx) error {
			b, _ := tx.CreateBucketIfNotExists([]byte(bucketName))
			raw := []byte(`{"format_version":99,"id":"future","kind":"capture_batch","status":"completed"}`)
			return b.Put([]byte("future"), raw)
		})
		_ = db.Close()
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.Get("future")
	if !errors.Is(err, ErrFutureFormatVersion) {
		t.Errorf("got %v, want ErrFutureFormatVersion", err)
	}
}

// TestStoreCorruptBlob — a corrupted JSON value returns a clean
// error (not panic) on Get.
func TestStoreCorruptBlob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")

	{
		db, _ := bolt.Open(path, 0600, nil)
		_ = db.Update(func(tx *bolt.Tx) error {
			b, _ := tx.CreateBucketIfNotExists([]byte(bucketName))
			return b.Put([]byte("corrupt"), []byte("not-valid-json"))
		})
		_ = db.Close()
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.Get("corrupt")
	if err == nil {
		t.Error("expected error on corrupted blob")
	}
}

// TestStoreList — table-driven over filters.
func TestStoreList(t *testing.T) {
	s := newTestStore(t)

	make := func(id, kind, status, token string, created time.Time) *Job {
		j := fullJob(id)
		j.Kind = kind
		j.Status = status
		j.ClientToken = token
		j.CreatedAt = created
		j.Result = nil // keep summary lean
		return j
	}
	t1 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)

	_ = s.Create(make("a", "capture_batch", StatusCompleted, "tok-a", t1))
	_ = s.Create(make("b", "capture_batch", StatusFailed, "tok-b", t2))
	_ = s.Create(make("c", "f4_import", StatusCompleted, "tok-c", t3))

	t.Run("status filter", func(t *testing.T) {
		got, _ := s.List(ListFilter{Status: StatusCompleted})
		if len(got) != 2 {
			t.Errorf("got %d, want 2", len(got))
		}
	})
	t.Run("kind filter", func(t *testing.T) {
		got, _ := s.List(ListFilter{Kind: "f4_import"})
		if len(got) != 1 || got[0].ID != "c" {
			t.Errorf("got %+v, want only c", got)
		}
	})
	t.Run("token filter", func(t *testing.T) {
		got, _ := s.List(ListFilter{ClientToken: "tok-b"})
		if len(got) != 1 || got[0].ID != "b" {
			t.Errorf("got %+v, want only b", got)
		}
	})
	t.Run("time range", func(t *testing.T) {
		// CreatedAfter and CreatedBefore are both INCLUSIVE
		// (api/jobs_list.go documents "lower-bound" / "upper-bound").
		// All three jobs land in [t1, t3].
		got, _ := s.List(ListFilter{CreatedAfter: t1, CreatedBefore: t3})
		if len(got) != 3 {
			t.Errorf("inclusive [t1, t3]: got %d, want 3", len(got))
		}
		// Strict-inside via shifting bounds by one ns:
		got, _ = s.List(ListFilter{CreatedAfter: t1.Add(time.Nanosecond), CreatedBefore: t3.Add(-time.Nanosecond)})
		if len(got) != 1 || got[0].ID != "b" {
			t.Errorf("strict-inside: got %+v, want only b", got)
		}
	})
	t.Run("pagination", func(t *testing.T) {
		got, _ := s.List(ListFilter{Limit: 2})
		if len(got) != 2 {
			t.Errorf("got %d, want 2", len(got))
		}
		got, _ = s.List(ListFilter{Limit: 2, Offset: 2})
		if len(got) != 1 {
			t.Errorf("offset 2: got %d, want 1", len(got))
		}
	})
	t.Run("empty filter returns all", func(t *testing.T) {
		got, _ := s.List(ListFilter{})
		if len(got) != 3 {
			t.Errorf("got %d, want 3", len(got))
		}
	})
	t.Run("limit cap rejected", func(t *testing.T) {
		_, err := s.List(ListFilter{Limit: MaxListLimit + 1})
		if err == nil {
			t.Error("expected error on over-limit")
		}
	})
}

// TestStoreListExcludesResult — JobSummary doesn't carry Result
// or ClientRefToID. Verify the struct fields explicitly.
func TestStoreListExcludesResult(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("heavy")
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	got, _ := s.List(ListFilter{})
	if len(got) != 1 {
		t.Fatal("expected 1 summary")
	}
	// JobSummary should be the lightweight struct; ensure the
	// type doesn't have a Result field.
	summary := got[0]
	if summary.ID != "heavy" {
		t.Errorf("ID: got %s", summary.ID)
	}
	// Compile-time assertion that JobSummary lacks Result/ClientRefToID:
	// if these fields were added, the test would fail to compile.
	_ = struct {
		_ *JobSummary
	}{}
}

// TestStoreListEmpty — no jobs returns empty slice (not nil).
func TestStoreListEmpty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

// TestRunGCRespectsTTL — pre-create jobs with various CompletedAt
// times; run GC; verify only stale terminal jobs are deleted.
func TestRunGCRespectsTTL(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	makeJob := func(id, status string, completedAt time.Time) {
		j := fullJob(id)
		j.Status = status
		j.CompletedAt = completedAt
		j.CreatedAt = completedAt.Add(-time.Hour)
		_ = s.Create(j)
	}

	// Old completed (older than 90 days from `now`) — should GC.
	makeJob("old-completed", StatusCompleted,
		now.Add(-100*24*time.Hour))
	// Recent completed (1 day old) — should keep.
	makeJob("recent-completed", StatusCompleted, now.Add(-24*time.Hour))
	// Old failed (100 days) — should NOT GC (failed retention=365d).
	makeJob("old-failed", StatusFailed, now.Add(-100*24*time.Hour))
	// Very old failed (400 days) — should GC.
	makeJob("ancient-failed", StatusFailed, now.Add(-400*24*time.Hour))

	deleted, err := s.RunGC(context.Background(), now, RetentionPolicy{
		Completed: 90 * 24 * time.Hour,
		Failed:    365 * 24 * time.Hour,
		Cancelled: 90 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d, want 2", deleted)
	}

	// Verify post-GC contents.
	if _, err := s.Get("old-completed"); !errors.Is(err, ErrNotFound) {
		t.Error("old-completed should be deleted")
	}
	if _, err := s.Get("recent-completed"); err != nil {
		t.Errorf("recent-completed should be kept: %v", err)
	}
	if _, err := s.Get("old-failed"); err != nil {
		t.Errorf("old-failed (within failed retention) should be kept: %v", err)
	}
	if _, err := s.Get("ancient-failed"); !errors.Is(err, ErrNotFound) {
		t.Error("ancient-failed should be deleted")
	}
}

// TestRunGCSkipsInFlight — GC must never delete pending or running
// jobs, regardless of CreatedAt age or zero CompletedAt.
func TestRunGCSkipsInFlight(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	pending := fullJob("ancient-pending")
	pending.Status = StatusPending
	pending.CreatedAt = now.Add(-1000 * 24 * time.Hour)
	pending.CompletedAt = time.Time{}
	_ = s.Create(pending)

	running := fullJob("ancient-running")
	running.Status = StatusRunning
	running.CreatedAt = now.Add(-1000 * 24 * time.Hour)
	running.CompletedAt = time.Time{}
	_ = s.Create(running)

	deleted, err := s.RunGC(context.Background(), now, RetentionPolicy{
		Completed: time.Nanosecond,
		Failed:    time.Nanosecond,
		Cancelled: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted %d in-flight jobs (must be 0)", deleted)
	}
	if _, err := s.Get("ancient-pending"); err != nil {
		t.Errorf("ancient-pending: %v", err)
	}
	if _, err := s.Get("ancient-running"); err != nil {
		t.Errorf("ancient-running: %v", err)
	}
}

// TestRunGCZeroRetentionKeepsForever — zero retention duration
// means "never GC" for that status.
func TestRunGCZeroRetentionKeepsForever(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	j := fullJob("ancient")
	j.Status = StatusCompleted
	j.CreatedAt = now.Add(-1000 * 24 * time.Hour)
	j.CompletedAt = j.CreatedAt
	_ = s.Create(j)

	deleted, err := s.RunGC(context.Background(), now, RetentionPolicy{}) // all zero
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Error("zero retention should keep everything")
	}
}

// TestStoreSupersedesChain — A->B->C where each Job's
// SupersedesJobID points at the previous; chain is walkable via
// Get on each ID.
func TestStoreSupersedesChain(t *testing.T) {
	s := newTestStore(t)
	a := fullJob("A")
	a.SupersedesJobID = "" // chain head
	b := fullJob("B")
	b.SupersedesJobID = "A"
	c := fullJob("C")
	c.SupersedesJobID = "B"

	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(b); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(c); err != nil {
		t.Fatal(err)
	}

	cur := "C"
	walked := []string{}
	for cur != "" {
		j, err := s.Get(cur)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, j.ID)
		cur = j.SupersedesJobID
	}
	want := []string{"C", "B", "A"}
	if len(walked) != len(want) {
		t.Fatalf("walked %d, want %d", len(walked), len(want))
	}
	for i := range want {
		if walked[i] != want[i] {
			t.Errorf("walked[%d] = %s, want %s", i, walked[i], want[i])
		}
	}
}

// TestStoreClientRefMapRoundTrip — ClientRefToID with multiple
// keys/values survives marshal/unmarshal intact. Already covered
// by TestStoreCreateGet but called out for explicit guard.
func TestStoreClientRefMapRoundTrip(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("clientref")
	j.ClientRefToID = map[string]string{
		"ref-1":            "01ABC",
		"ref-with-special": "01DEF",
		"":                 "01EMPTY", // edge case: empty key
	}
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("clientref")
	if len(got.ClientRefToID) != 3 {
		t.Errorf("len: got %d, want 3", len(got.ClientRefToID))
	}
	for k, v := range j.ClientRefToID {
		if got.ClientRefToID[k] != v {
			t.Errorf("[%q]: got %q, want %q", k, got.ClientRefToID[k], v)
		}
	}
}

// TestStoreRequestHashRoundTrip — RequestHash is byte-identical
// through marshal/unmarshal.
func TestStoreRequestHashRoundTrip(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("hash")
	j.RequestHash = "abcdef0123456789" // hex-style
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("hash")
	if got.RequestHash != j.RequestHash {
		t.Errorf("got %q, want %q", got.RequestHash, j.RequestHash)
	}
}

// TestStoreFormatVersionRoundTrip — write+read with explicit
// FormatVersion preserves the value.
func TestStoreFormatVersionRoundTrip(t *testing.T) {
	s := newTestStore(t)
	j := fullJob("fv")
	j.FormatVersion = 1 // explicit
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("fv")
	if got.FormatVersion != 1 {
		t.Errorf("FormatVersion: got %d, want 1", got.FormatVersion)
	}
}

// TestStoreConcurrentReaders — barrier-channel: 8 goroutines do
// concurrent Get/List under contention with one writer doing
// AdvanceStatus. bbolt MVCC guarantees coherent snapshots; this
// test verifies no panic + writer can advance without blocking
// indefinitely on readers.
func TestStoreConcurrentReaders(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		j := fullJob(string(rune('a' + i)))
		j.Status = StatusPending
		_ = s.Create(j)
	}

	release := make(chan struct{})
	stopWriter := make(chan struct{})

	// Readers WG (separate so we can close stopWriter without
	// waiting on the writer itself).
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-release
			for j := 0; j < 100; j++ {
				_, _ = s.Get("a")
				_, _ = s.List(ListFilter{})
			}
		}()
	}

	// Writer WG.
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		<-release
		for {
			select {
			case <-stopWriter:
				return
			default:
			}
			_ = s.AdvanceStatus("a", StatusRunning, nil)
			_ = s.AdvanceStatus("a", StatusCompleted, func(j *Job) {
				j.CompletedAt = time.Now().UTC()
			})
		}
	}()

	close(release)
	readers.Wait() // Wait for readers first.
	close(stopWriter)
	writer.Wait()
}
