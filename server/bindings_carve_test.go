package server

import (
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
