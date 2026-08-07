package api

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/index"
)

// setupFrozenAPI drives the real publication lifecycle through the
// api layer: seed a record on a writable store via api.Save, close
// the engine, freeze the store offline via core.FreezeStore (the CLI
// primitive), and reopen -- so the STORE manifest, not a test option,
// is what makes the reopened engine read-only. Returns the frozen
// API, its engine, and the seeded record's ID.
func setupFrozenAPI(t *testing.T) (*API, *core.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Dir = t.TempDir() + "/backups"
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	open := func() *core.Engine {
		eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
			core.WithVectorIndex(index.NewFlatIndex()),
			core.WithVolatileStorage(),
		})
		if err != nil {
			t.Fatalf("LoadEngineWithOptions: %v", err)
		}
		return eng
	}

	// Phase 1: writable store; seed through the api surface.
	eng1 := open()
	a1 := New(Dependencies{Engine: eng1, Log: slog.Default(), ConfigDir: dir})
	saved, apiErr := a1.Save(context.Background(), SaveRequest{
		Content:     "frozen knowledge survives publication",
		Temporality: "durable",
	})
	if apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	a1.StopPreparedSweeper()
	if err := eng1.Close(); err != nil {
		t.Fatalf("close writable engine: %v", err)
	}

	// Phase 2: freeze offline, reopen frozen.
	if err := core.FreezeStore(dir, "publisher@example.com"); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	eng2 := open()
	t.Cleanup(func() {
		if err := eng2.Close(); err != nil {
			t.Logf("engine close: %v", err)
		}
	})
	if !eng2.ReadOnly() {
		t.Fatal("reopened store should be read-only after FreezeStore")
	}
	a2 := New(Dependencies{Engine: eng2, Log: slog.Default(), ConfigDir: dir})
	t.Cleanup(a2.StopPreparedSweeper)
	return a2, eng2, saved.ID
}

// TestReadOnlyAPIRejectsWrites drives representative mutating
// operations from every cluster -- records, collections, sessions,
// curation, branches, backup -- against a manifest-frozen store and
// asserts each is rejected with the taxonomy code "forbidden" (not
// just any error -- the code is the wire contract agents branch on).
// This is the behavioral backstop for the AST tripwire in
// readonly_guard_test.go, which only proves a rejectIfReadOnly call
// EXISTS in each write method's body, not that its error is returned.
func TestReadOnlyAPIRejectsWrites(t *testing.T) {
	a, _, recID := setupFrozenAPI(t)
	ctx := context.Background()

	cases := []struct {
		op   string
		call func() *APIError
	}{
		{"Save", func() *APIError {
			_, e := a.Save(ctx, SaveRequest{Content: "must not land"})
			return e
		}},
		{"CollectionAdd", func() *APIError {
			_, e := a.CollectionAdd(ctx, "any-collection", CollectionAddRequest{Fields: map[string]any{"title": "x"}})
			return e
		}},
		{"Update", func() *APIError {
			_, e := a.Update(ctx, UpdateRequest{ID: recID})
			return e
		}},
		{"Link", func() *APIError {
			_, e := a.Link(ctx, LinkRequest{SourceID: recID, TargetID: recID, EdgeType: "related_to"})
			return e
		}},
		{"Resolve", func() *APIError {
			_, e := a.Resolve(ctx, ResolveRequest{ID: recID, Resolution: "completed"})
			return e
		}},
		{"SessionPrepare", func() *APIError {
			_, e := a.SessionPrepare(ctx, "some-session")
			return e
		}},
		{"SessionSave", func() *APIError {
			_, e := a.SessionSave(ctx, "some-session", nil, false)
			return e
		}},
		{"CurationTrigger", func() *APIError {
			_, e := a.CurationTrigger(ctx)
			return e
		}},
		{"BranchCreate", func() *APIError {
			_, e := a.BranchCreate(ctx, BranchCreateRequest{Name: "frozen-branch"})
			return e
		}},
		{"BackupRestore", func() *APIError {
			// A plausible request (absolute path, force set) so that a
			// dropped guard would surface as a non-"forbidden" code
			// from the later validation, not as a masked pass.
			_, e := a.BackupRestore(ctx, RestoreRequest{Path: "/nonexistent/backup.tar.gz", Force: true})
			return e
		}},
		{"BackupImport", func() *APIError {
			_, e := a.BackupImport(ctx, ImportRequest{Records: []backup.ExportRecord{
				{ID: "rec-1", Properties: map[string]any{"content_full": "nope"}},
			}})
			return e
		}},
	}
	for _, tc := range cases {
		apiErr := tc.call()
		if apiErr == nil {
			t.Errorf("%s on a frozen store: expected rejection, got success", tc.op)
			continue
		}
		if apiErr.Code != "forbidden" {
			t.Errorf("%s on a frozen store: code = %q, want \"forbidden\" (message: %s)",
				tc.op, apiErr.Code, apiErr.Message)
		}
		// The rejection must name the read-only condition, not just
		// return an opaque "forbidden": an agent (local or through the
		// remote proxy, which surfaces this message verbatim) needs to
		// learn WHY the write failed so it can adapt.
		if !strings.Contains(apiErr.Message, "read-only") {
			t.Errorf("%s on a frozen store: message = %q, want it to name the read-only condition",
				tc.op, apiErr.Message)
		}
	}
}

// TestReadOnlyAPIReadsWorkWithoutAccessBump pins the two halves of
// the read contract on a frozen store: (a) Search, Inspect, and
// BackupExport all succeed; (b) serving those reads leaves the
// records' access bookkeeping (access_count, last_accessed) UNCHANGED
// -- the bump-skip in api/search.go and api/inspect.go.
func TestReadOnlyAPIReadsWorkWithoutAccessBump(t *testing.T) {
	a, eng, recID := setupFrozenAPI(t)
	ctx := context.Background()

	accessState := func() (int64, bool) {
		eng.RLock()
		defer eng.RUnlock()
		n, ok := eng.Graph().GetNode(recID)
		if !ok {
			t.Fatal("seeded record missing from frozen store")
		}
		count, _ := n.Properties.GetInt64("access_count")
		_, hasLastAccessed := n.Properties.GetTimestamp("last_accessed")
		return count, hasLastAccessed
	}
	countBefore, lastAccessedBefore := accessState()

	// Search finds the record.
	sr, apiErr := a.Search(ctx, SearchRequest{Match: "frozen knowledge"})
	if apiErr != nil {
		t.Fatalf("Search on frozen store: %v", apiErr)
	}
	foundInSearch := false
	for _, r := range sr.Results {
		if r.ID == recID {
			foundInSearch = true
		}
	}
	if !foundInSearch {
		t.Fatalf("Search on frozen store should return the seeded record; got %d results", len(sr.Results))
	}

	// Inspect returns the record.
	ir, apiErr := a.Inspect(ctx, InspectRequest{ID: recID})
	if apiErr != nil {
		t.Fatalf("Inspect on frozen store: %v", apiErr)
	}
	if ir.ID != recID {
		t.Fatalf("Inspect returned %q, want %q", ir.ID, recID)
	}
	if _, ok := ir.Properties["content_full"]; !ok {
		t.Error("Inspect on frozen store should include content_full")
	}

	// Export streams -- this is how a frozen store is shared.
	var buf bytes.Buffer
	contentType, apiErr := a.BackupExport(ctx, ExportRequest{}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport on frozen store: %v", apiErr)
	}
	if contentType != "application/x-ndjson" {
		t.Errorf("BackupExport content type = %q, want application/x-ndjson", contentType)
	}
	if buf.Len() == 0 {
		t.Error("BackupExport on frozen store produced no output")
	}

	// The critical half: the reads above must not have bumped access
	// bookkeeping. access_count and last_accessed are knowledge-graph
	// state; a frozen store serves reads without mutating them.
	countAfter, lastAccessedAfter := accessState()
	if countAfter != countBefore {
		t.Errorf("access_count changed on a frozen store: %d -> %d (bump not skipped)",
			countBefore, countAfter)
	}
	if lastAccessedAfter != lastAccessedBefore {
		t.Errorf("last_accessed presence changed on a frozen store: %v -> %v (bump not skipped)",
			lastAccessedBefore, lastAccessedAfter)
	}
}

// TestWritableStoreStillBumpsAccess is the control for the bump-skip
// test above: on a writable store the same Search + Inspect calls DO
// advance access bookkeeping. Without this control, a regression that
// dropped the bump everywhere would leave the frozen-store assertion
// vacuously green.
func TestWritableStoreStillBumpsAccess(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	saved, apiErr := a.Save(ctx, SaveRequest{
		Content:     "writable knowledge gets bumped",
		Temporality: "durable",
	})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}

	if _, apiErr := a.Search(ctx, SearchRequest{Match: "writable knowledge"}); apiErr != nil {
		t.Fatalf("Search: %v", apiErr)
	}
	if _, apiErr := a.Inspect(ctx, InspectRequest{ID: saved.ID}); apiErr != nil {
		t.Fatalf("Inspect: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(saved.ID)
	if !ok {
		t.Fatal("record missing")
	}
	count, _ := n.Properties.GetInt64("access_count")
	if count == 0 {
		t.Error("writable store: access_count should advance on Search/Inspect")
	}
	if _, ok := n.Properties.GetTimestamp("last_accessed"); !ok {
		t.Error("writable store: last_accessed should be set on Search/Inspect")
	}
}
