package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/core"
)

// TestStoreCarveLoopbackOnly is the security guard: the carve route
// materializes a store at a caller-supplied absolute filesystem path, so
// a non-loopback origin must be rejected with 403 before any decode or
// api call. Mirrors the backup/restore/export/import loopback tests.
func TestStoreCarveLoopbackOnly(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequestFrom(t, srv, "POST", "/v1/store/carve",
		api.CarveOutRequest{IDs: []string{"whatever"}}, "203.0.113.7:5555")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback carve, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStoreCarveRoundTrip drives the full server-mediated path: a small
// loopback carve of one explicit-id seed creates a fresh destination
// store on disk with the expected counts, and the copied node is present
// in the destination.
func TestStoreCarveRoundTrip(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "carve me")

	destHome := filepath.Join(t.TempDir(), "carved")
	destData := filepath.Join(destHome, "data")

	w := doRequest(t, srv, "POST", "/v1/store/carve", api.CarveOutRequest{
		IDs:         []string{id},
		DestName:    "carved",
		DestDataDir: destData,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %v", resp)
	}
	if got := data["seed_count"].(float64); got != 1 {
		t.Errorf("seed_count = %v, want 1", got)
	}
	if got := data["node_count"].(float64); got != 1 {
		t.Errorf("node_count = %v, want 1", got)
	}
	if data["dry_run"].(bool) {
		t.Error("dry_run should be false for a committing carve")
	}

	// Destination store exists on disk with the ULID-preserved node.
	dest, err := core.LoadEngine(destHome)
	if err != nil {
		t.Fatalf("open dest store: %v", err)
	}
	t.Cleanup(func() { dest.Close() })
	dest.RLock()
	_, present := dest.Graph().GetNode(id)
	dest.RUnlock()
	if !present {
		t.Errorf("dest store missing carved node %q", id)
	}
}

// TestStoreCarveDryRunWritesNothing asserts a loopback dry-run reports the
// selection but creates no destination on disk -- the request decodes and
// reaches the api, and the api honors DryRun.
func TestStoreCarveDryRunWritesNothing(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "preview me")

	destHome := filepath.Join(t.TempDir(), "preview")
	destData := filepath.Join(destHome, "data")

	w := doRequest(t, srv, "POST", "/v1/store/carve", api.CarveOutRequest{
		IDs:         []string{id},
		DestDataDir: destData,
		DryRun:      true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data := parseResponse(t, w)["data"].(map[string]any)
	if !data["dry_run"].(bool) {
		t.Error("dry_run should be true")
	}
	if got := data["node_count"].(float64); got != 1 {
		t.Errorf("node_count = %v, want 1", got)
	}
	if _, err := os.Stat(destHome); !os.IsNotExist(err) {
		t.Errorf("dry run created %s (err=%v); it must write nothing", destHome, err)
	}
}

// TestStoreCarveRequiresSeedHTTP asserts a loopback request with no seed
// surfaces the api's missing_field error as HTTP 400 (the request decodes
// fine; the api rejects it), proving the route wires the api error path.
func TestStoreCarveRequiresSeedHTTP(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/store/carve", api.CarveOutRequest{
		DestDataDir: filepath.Join(t.TempDir(), "x", "data"),
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a seedless carve, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStoreAddLoopbackOnly is the security guard for the top-up route: it
// opens and writes a store at a caller-supplied absolute filesystem path,
// so a non-loopback origin must be rejected with 403 before any decode or
// api call. Mirrors TestStoreCarveLoopbackOnly.
func TestStoreAddLoopbackOnly(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequestFrom(t, srv, "POST", "/v1/store/add",
		api.CarveAddRequest{IDs: []string{"whatever"}, DestDataDir: "/tmp/whatever/data"},
		"203.0.113.7:5555")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback add, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStoreAddRoundTrip drives the full server-mediated top-up: carve a
// destination with one record, then add a second record into it via the
// add route; the response reports one node added and both records are
// present in the destination.
func TestStoreAddRoundTrip(t *testing.T) {
	srv, eng := setupTestServer(t)
	id1 := addRecord(t, eng, "first record")
	id2 := addRecord(t, eng, "second record added later")

	destHome := filepath.Join(t.TempDir(), "topup")
	destData := filepath.Join(destHome, "data")

	w := doRequest(t, srv, "POST", "/v1/store/carve", api.CarveOutRequest{
		IDs: []string{id1}, DestName: "topup", DestDataDir: destData,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("carve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(t, srv, "POST", "/v1/store/add", api.CarveAddRequest{
		IDs: []string{id2}, DestName: "topup", DestDataDir: destData,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data, ok := parseResponse(t, w)["data"].(map[string]any)
	if !ok {
		t.Fatalf("add response has no data object")
	}
	if got := data["nodes_added"].(float64); got != 1 {
		t.Errorf("nodes_added = %v, want 1", got)
	}
	if data["dry_run"].(bool) {
		t.Error("dry_run should be false for a committing add")
	}

	dest, err := core.LoadEngine(destHome)
	if err != nil {
		t.Fatalf("open dest store: %v", err)
	}
	t.Cleanup(func() { dest.Close() })
	dest.RLock()
	_, has1 := dest.Graph().GetNode(id1)
	_, has2 := dest.Graph().GetNode(id2)
	dest.RUnlock()
	if !has1 || !has2 {
		t.Errorf("dest missing records: has id1=%v has id2=%v", has1, has2)
	}
}

// TestStoreAddServedDestinationRejected pins the anti-hang guard: adding
// into a destination whose store is served by a LIVE server must return a
// prompt 409, never block on bbolt's file lock. Opening a second engine on
// a served store's data dir would hang forever (core.LoadEngine opens bbolt
// with no lock timeout), and this also covers a self-add (the mediating
// server is itself serving the destination). The test plants a server.json
// marker carrying THIS process's own (alive) PID in the dest home; the
// guard reads it and refuses before api.CarveAdd opens the engine. No real
// server is started, so the test cannot hang. Falsifiable: without the
// guard the add opens the (unlocked) dest and returns 200, not 409.
func TestStoreAddServedDestinationRejected(t *testing.T) {
	srv, eng := setupTestServer(t)
	id := addRecord(t, eng, "served-dest record")

	destHome := filepath.Join(t.TempDir(), "served")
	destData := filepath.Join(destHome, "data")

	// Create the destination store first.
	w := doRequest(t, srv, "POST", "/v1/store/carve", api.CarveOutRequest{
		IDs: []string{id}, DestName: "served", DestDataDir: destData,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("carve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Plant a live-server marker in the dest home: server.json with THIS
	// process's PID (guaranteed alive). No real server is started.
	marker, err := json.Marshal(ServerInfo{PID: os.Getpid(), StoreDir: destData})
	if err != nil {
		t.Fatalf("marshal server info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destHome, "server.json"), marker, 0o600); err != nil {
		t.Fatalf("write server.json: %v", err)
	}

	// The add must be refused promptly with 409 (never opens a 2nd engine).
	w = doRequest(t, srv, "POST", "/v1/store/add", api.CarveAddRequest{
		IDs: []string{id}, DestName: "served", DestDataDir: destData,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a served destination, got %d: %s", w.Code, w.Body.String())
	}
}
