package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// writeRemoteStoreConfig writes a valid remote-client config.yaml under
// dir (remote.url + pin + token, no local server). Returns dir.
func writeRemoteStoreConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := config.Defaults()
	cfg.Remote.URL = "https://lab.local:7420"
	cfg.Remote.Pin = "sha256:" + strings.Repeat("a", 64)
	cfg.Remote.Token = "test-token"
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("save remote config: %v", err)
	}
	return dir
}

func TestRunStopRemoteStoreExitsZero(t *testing.T) {
	// A remote-client store has no local server; stop must reap proxies
	// (none here) and succeed, not error "no running server found".
	storeDir := writeRemoteStoreConfig(t, t.TempDir())
	if err := runStop(storeDir, false); err != nil {
		t.Fatalf("runStop on a remote store should exit 0, got: %v", err)
	}
}

func TestStoreListShowsRemoteStore(t *testing.T) {
	base := newFreezeTestBase(t) // has a local default store
	storeDir := filepath.Join(base, "stores", "team")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRemoteStoreConfig(t, storeDir)

	out, err := runCmd(t, "store", "list", "--config-dir", base)
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("parse list: %v\n%s", err, out)
	}
	var team map[string]any
	for _, e := range list {
		if e["name"] == "team" {
			team = e
		}
	}
	if team == nil {
		t.Fatalf("remote store 'team' missing from store list: %s", out)
	}
	if team["remote"] != true {
		t.Errorf("team.remote = %v, want true", team["remote"])
	}
	if url, _ := team["remote_url"].(string); !strings.Contains(url, "lab.local") {
		t.Errorf("team.remote_url = %v, want the remote URL", team["remote_url"])
	}
	// No misleading local-manifest badge on a remote store.
	if _, present := team["manifest"]; present {
		t.Errorf("remote store should not carry a local manifest note: %v", team["manifest"])
	}
}

func TestRunStopLocalStoreStillStops(t *testing.T) {
	// A local store with no running server keeps the old behavior:
	// stopServer reports there is no server to stop (a real error).
	storeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storeDir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runStop(storeDir, false)
	if err == nil || !strings.Contains(err.Error(), "no running server") {
		t.Fatalf("local store with no server should report no running server, got: %v", err)
	}
}
