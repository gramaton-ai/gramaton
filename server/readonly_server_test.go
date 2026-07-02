package server

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/index"
)

// setupReadOnlyTestServer mirrors setupTestServer but forces the
// engine into store-level read-only mode via core.WithReadOnly. The
// server layer only consults engine.ReadOnly(), so the runtime option
// exercises the same code paths as a manifest-frozen store (the full
// FreezeStore lifecycle is covered by the api-layer tests in
// api/readonly_api_test.go).
func setupReadOnlyTestServer(t *testing.T) (*Server, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Dir = t.TempDir() + "/backups"
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
		core.WithReadOnly(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	if !eng.ReadOnly() {
		t.Fatal("test precondition: engine should be read-only")
	}

	serverCfg := DefaultConfig()
	serverCfg.ConfigDir = dir
	srv, err := New(eng, serverCfg, slog.Default())
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	return srv, eng
}

// TestReadOnlyEnvelopeFlag pins the envelope contract: every HTTP
// response from a read-only store -- reads included -- carries
// store_readonly=true alongside the curation envelope, and writable
// stores OMIT the field entirely so existing consumers see no change.
func TestReadOnlyEnvelopeFlag(t *testing.T) {
	frozen, _ := setupReadOnlyTestServer(t)

	// Read responses carry the flag.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/stats", nil},
		{"POST", "/v1/search", map[string]any{"top": 5}},
	} {
		w := doRequest(t, frozen, tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s on frozen store: status %d, body %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		resp := parseResponse(t, w)
		if got, ok := resp["store_readonly"].(bool); !ok || !got {
			t.Errorf("%s %s on frozen store: envelope store_readonly = %v, want true", tc.method, tc.path, resp["store_readonly"])
		}
	}

	// Error responses carry it too (the rejected write below).
	w := doRequest(t, frozen, "POST", "/v1/records", map[string]any{"content": "nope"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /v1/records on frozen store: status %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("403 response missing error object: %s", w.Body.String())
	}
	if errObj["code"] != "forbidden" {
		t.Errorf("rejected write error code = %v, want forbidden", errObj["code"])
	}
	if got, ok := resp["store_readonly"].(bool); !ok || !got {
		t.Errorf("403 response envelope store_readonly = %v, want true", resp["store_readonly"])
	}

	// Writable store: the field is omitted, not false.
	writable, _ := setupTestServer(t)
	w = doRequest(t, writable, "GET", "/v1/stats", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/stats on writable store: status %d", w.Code)
	}
	resp = parseResponse(t, w)
	if _, present := resp["store_readonly"]; present {
		t.Errorf("writable store envelope should omit store_readonly, got %v", resp["store_readonly"])
	}
}

// TestReadOnlyInlineHandlersRejected covers the write paths that
// still live in the server layer (not yet migrated to api/): the
// intake service, revert, and ingest. Each must 403 with code
// "forbidden" on a read-only store.
func TestReadOnlyInlineHandlersRejected(t *testing.T) {
	frozen, _ := setupReadOnlyTestServer(t)

	for _, tc := range []struct {
		path string
		body any
	}{
		{"/v1/intake", map[string]any{"content": "nope"}},
		{"/v1/revert", map[string]any{"hash": "abc123"}},
		{"/v1/ingest", map[string]any{"files": []map[string]any{{"filename": "a.md", "content": "nope"}}}},
	} {
		w := doRequest(t, frozen, "POST", tc.path, tc.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s on frozen store: status %d, want 403 (body %s)", tc.path, w.Code, w.Body.String())
			continue
		}
		resp := parseResponse(t, w)
		errObj, ok := resp["error"].(map[string]any)
		if !ok || errObj["code"] != "forbidden" {
			t.Errorf("POST %s on frozen store: error code = %v, want forbidden", tc.path, resp["error"])
		}
	}
}

// TestReadOnlyBackgroundWritersGated pins that none of the server's
// background writers start against a read-only store: the curation
// runner (would classify/link/synthesize), the startup self-heal
// sweep (would rewrite record content), and the access flusher
// (would persist access metadata). Observed via the state each
// starter publishes -- s.runner / s.curationCancel / s.accessCancel
// stay nil and runStartupSelfHeal reports it did not run -- so the
// test never races a goroutine.
func TestReadOnlyBackgroundWritersGated(t *testing.T) {
	frozen, _ := setupReadOnlyTestServer(t)
	if !frozen.engine.Config().Curation.Enabled {
		t.Fatal("test precondition: curation must be enabled in default config for the gate to be meaningful")
	}

	frozen.startCurationRunner()
	frozen.mu.Lock()
	curationCancel := frozen.curationCancel
	frozen.mu.Unlock()
	if frozen.runner != nil || curationCancel != nil {
		t.Error("curation runner started on a read-only store")
	}

	if frozen.runStartupSelfHeal() {
		t.Error("startup self-heal ran on a read-only store")
	}

	frozen.startAccessFlusher()
	frozen.mu.Lock()
	accessCancel := frozen.accessCancel
	frozen.mu.Unlock()
	if accessCancel != nil {
		t.Error("access flusher started on a read-only store")
	}

	// Control: on a writable store the same starters DO start, so
	// the assertions above cannot pass vacuously.
	writable, _ := setupTestServer(t)
	writable.startCurationRunner()
	writable.mu.Lock()
	wCurationCancel := writable.curationCancel
	writable.mu.Unlock()
	if writable.runner == nil || wCurationCancel == nil {
		t.Error("writable store: curation runner should start")
	} else {
		wCurationCancel()
		writable.runner.Stop()
	}

	if !writable.runStartupSelfHeal() {
		t.Error("writable store: startup self-heal should run")
	}

	writable.startAccessFlusher()
	writable.mu.Lock()
	wAccessCancel := writable.accessCancel
	writable.mu.Unlock()
	if wAccessCancel == nil {
		t.Error("writable store: access flusher should start")
	} else {
		// Nothing is access-dirty, so the flusher's final flush is a
		// no-op and cannot race the engine close in cleanup.
		wAccessCancel()
	}
}

// TestAccessFlushTickStopsWhenReadOnly covers the runtime-flip half
// of the access-flusher gate. startAccessFlusher gates at boot, but a
// BackupRestore of a frozen archive flips the live engine read-only
// mid-process; the per-tick seam must then report stop so the flusher
// goroutine quiesces at its next tick instead of ticking until
// process restart. Control: a writable store's tick flushes and
// continues.
func TestAccessFlushTickStopsWhenReadOnly(t *testing.T) {
	frozen, _ := setupReadOnlyTestServer(t)
	if frozen.accessFlushTick() {
		t.Error("accessFlushTick on a read-only store should report stop")
	}

	writable, _ := setupTestServer(t)
	if !writable.accessFlushTick() {
		t.Error("accessFlushTick on a writable store should report continue")
	}
}

// TestCurationCycleSkipsWhenReadOnly covers the runtime-flip half of
// the curation gate. startCurationRunner gates at boot, but a
// BackupRestore of a frozen archive flips the live engine read-only
// after the runner started; the cycle entry must then early-return --
// no deterministic write rejection logged, no LLM spend -- so the
// runner quiesces at its next interval. Observed via Runner.Status():
// an early-returned cycle never sets LastRun, so LastCurated stays
// nil. Control: on a writable store the same trigger runs a cycle and
// stamps LastCurated.
func TestCurationCycleSkipsWhenReadOnly(t *testing.T) {
	_, frozenEng := setupReadOnlyTestServer(t)
	runner := curation.NewRunner(frozenEng, nil, frozenEng.Config(), slog.Default())
	if !runner.Trigger(context.Background()) {
		t.Fatal("Trigger reported a cycle already in progress")
	}
	if got := runner.Status().LastCurated; got != nil {
		t.Errorf("curation cycle ran against a read-only store (LastCurated = %v, want nil)", got)
	}

	_, writableEng := setupTestServer(t)
	wRunner := curation.NewRunner(writableEng, nil, writableEng.Config(), slog.Default())
	if !wRunner.Trigger(context.Background()) {
		t.Fatal("writable store: Trigger reported a cycle already in progress")
	}
	if wRunner.Status().LastCurated == nil {
		t.Error("writable store: a triggered cycle should set LastCurated")
	}
}
