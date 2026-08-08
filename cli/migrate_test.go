package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// TestMigrateRefusesRemoteMode pins that `gramaton migrate` refuses
// cleanly, before touching any store files, when the target store is
// configured as a remote client. Before the fix, migrate had no
// guardLocalStore call (every sibling local-store command --
// backfill, prune, repair, serve, validate -- has one), so in remote
// mode it proceeded straight to core.MigrateStore and opened a local
// engine against the store's data dir instead of refusing.
//
// remoteMode() memoizes its resolution process-wide via sync.Once
// (cli/remote.go): some earlier CLI command in this shared test
// binary has already resolved it against the local integration
// store, so pointing configDir() at a fresh remote config would not
// re-resolve. This test overrides the cached result directly instead
// -- the same value a real remote.url config at dir would produce --
// and pins DataDir under a throwaway temp dir so that even a
// reintroduced version of this bug could not reach outside it.
func TestMigrateRefusesRemoteMode(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Remote.URL = "https://lab.local:7420"
	cfg.Remote.Pin = "sha256:" + strings.Repeat("a", 64)
	cfg.Remote.Token = "test-token"
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.DataDir = filepath.Join(dir, "data")
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("save config: %v", err)
	}

	_, _ = remoteMode() // ensure the sync.Once has fired before overriding
	origResolved, origErr := remoteResolved, remoteErr
	t.Cleanup(func() { remoteResolved, remoteErr = origResolved, origErr })
	remoteResolved = &remoteEndpoint{url: cfg.Remote.URL, token: "test-token"}
	remoteErr = nil

	origCfgDir, origStoreName := cfgDir, storeName
	t.Cleanup(func() { cfgDir, storeName = origCfgDir, origStoreName })
	cfgDir = dir
	storeName = ""

	err := runMigrate(migrateCmd, nil)
	if err == nil {
		t.Fatal("migrate should refuse when the store is in remote mode")
	}
	if !strings.Contains(err.Error(), "remote mode") {
		t.Fatalf("expected a remote-mode refusal, got: %v", err)
	}
	if _, statErr := os.Stat(cfg.DataDir); statErr == nil {
		t.Error("migrate should not create a local data dir when the store is in remote mode")
	}
}
