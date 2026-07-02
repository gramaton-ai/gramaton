package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// newAttachSourceDir fabricates a shared-store artifact for the CLI
// command tests: a store dir with data/FORMAT at the current version
// and a HEAD file, optionally frozen. (The engine-backed fixture
// lives in internal/setup's wizard tests; the command only touches
// the on-disk shape.)
func newAttachSourceDir(t *testing.T, frozen bool) (storeDir, dataDir string) {
	t.Helper()
	storeDir = t.TempDir()
	dataDir = filepath.Join(storeDir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"FORMAT": strconv.Itoa(version.StoreFormatVersion),
		"HEAD":   "abc123",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dataDir, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if frozen {
		if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir, dataDir
}

// resetStoreAttachFlags restores the attach command's flag state:
// --name lives on a nested subcommand, which runCmd's top-level flag
// reset does not cover (same caveat as store create --read-only).
func resetStoreAttachFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { storeAttachName = "" })
}

func TestStoreAttachEndToEnd(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)
	srcStoreDir, _ := newAttachSourceDir(t, true)

	out, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "team-notes", "--config-dir", base)
	if err != nil {
		t.Fatalf("store attach: %v", err)
	}
	got := parseJSONMap(t, out)

	if got["attached"] != "team-notes" {
		t.Errorf("attached = %v, want team-notes", got["attached"])
	}
	if got["read_only"] != true {
		t.Errorf("read_only = %v, want true", got["read_only"])
	}
	if got["owner"] != "Ada Lovelace <ada@example.com>" {
		t.Errorf("owner = %v, want the publisher's provenance", got["owner"])
	}
	// The completion output must show how to reach the store and how
	// a harness gets a second MCP entry pointed at it.
	if access, _ := got["access"].(string); !strings.Contains(access, "--store team-notes") || !strings.Contains(access, "GRAMATON_STORE") {
		t.Errorf("access hint = %q, want the --store / GRAMATON_STORE forms", access)
	}
	if mcp, _ := got["mcp"].(string); !strings.Contains(mcp, "gramaton --store team-notes mcp") {
		t.Errorf("mcp hint = %q, want the --store mcp form", mcp)
	}

	// On disk: copied data, frozen manifest, minimal config.
	destData := filepath.Join(base, "stores", "team-notes", "data")
	for _, f := range []string{"FORMAT", "HEAD"} {
		if _, err := os.Stat(filepath.Join(destData, f)); err != nil {
			t.Errorf("copied data dir missing %s: %v", f, err)
		}
	}
	m, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly || m.Owner != "Ada Lovelace <ada@example.com>" {
		t.Errorf("copied manifest = %+v, want frozen with preserved provenance", m)
	}
	cfgRaw, err := os.ReadFile(filepath.Join(base, "stores", "team-notes", "config.yaml"))
	if err != nil {
		t.Fatalf("per-store config: %v", err)
	}
	for _, banned := range []string{"llm:", "author:"} {
		if strings.Contains(string(cfgRaw), banned) {
			t.Errorf("per-store config must not contain %q:\n%s", banned, cfgRaw)
		}
	}

	// The user's existing (default) store is untouched and writable.
	dm, err := core.ReadStoreManifest(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if dm.ReadOnly {
		t.Error("attach flipped the default store read-only")
	}
}

func TestStoreAttachBadFormat(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "FORMAT"), []byte("999"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runCmd(t, "store", "attach", src, "--name", "bad", "--config-dir", base)
	if err == nil || !strings.Contains(err.Error(), "newer than this gramaton supports") {
		t.Fatalf("err = %v, want the format-version rejection", err)
	}
	if _, serr := os.Stat(filepath.Join(base, "stores", "bad")); !os.IsNotExist(serr) {
		t.Errorf("rejected attach left a store home behind, stat err = %v", serr)
	}

	// Not-a-store rejection, same command surface.
	empty := t.TempDir()
	_, err = runCmd(t, "store", "attach", empty, "--name", "bad2", "--config-dir", base)
	if err == nil || !strings.Contains(err.Error(), "doesn't look like a Gramaton store") {
		t.Fatalf("err = %v, want the missing-FORMAT rejection", err)
	}
}

// TestStoreAttachWritableArtifactFreezesCopy: a source that was never
// frozen still yields a frozen local copy; the source stays
// manifest-less.
func TestStoreAttachWritableArtifactFreezesCopy(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)
	srcStoreDir, srcDataDir := newAttachSourceDir(t, false)

	out, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "loose", "--config-dir", base)
	if err != nil {
		t.Fatalf("store attach: %v", err)
	}
	got := parseJSONMap(t, out)
	if got["read_only"] != true {
		t.Errorf("read_only = %v, want true", got["read_only"])
	}
	if _, present := got["owner"]; present {
		t.Errorf("owner = %v, want omitted for a writable artifact (nothing is guessed)", got["owner"])
	}

	m, err := core.ReadStoreManifest(filepath.Join(base, "stores", "loose", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly {
		t.Error("local copy not frozen")
	}
	if _, serr := os.Stat(filepath.Join(srcDataDir, "STORE")); !os.IsNotExist(serr) {
		t.Errorf("attach froze the SOURCE artifact, stat err = %v", serr)
	}
}

func TestStoreAttachNameCollision(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)
	srcStoreDir, _ := newAttachSourceDir(t, true)

	if _, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "dup", "--config-dir", base); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	_, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "dup", "--config-dir", base)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second attach err = %v, want the already-exists rejection", err)
	}
}

// TestStoreAttachCommandRegistered protects the CLI wiring, matching
// the freeze/thaw registration test.
func TestStoreAttachCommandRegistered(t *testing.T) {
	found := false
	for _, sub := range storeCmd.Commands() {
		if sub == storeAttachCmd {
			found = true
		}
	}
	if !found {
		t.Error("attach subcommand not registered on storeCmd")
	}
	if f := storeAttachCmd.Flags().Lookup("name"); f == nil {
		t.Error("name flag not registered on store attach")
	}
}
