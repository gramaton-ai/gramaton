package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
)

// newFreezeTestBase builds an isolated base config dir for the store
// freeze/thaw commands: a global config whose data_dir stays inside
// the temp dir (Defaults() would otherwise point at the real
// ~/.gramaton/data) with a configured author, plus the default
// store's data directory. No engine and no server -- freeze/thaw are
// offline primitives.
func newFreezeTestBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Author.Name = "Ada Lovelace"
	cfg.Author.Email = "ada@example.com"
	if err := config.Save(cfg, filepath.Join(base, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// runCmd's --config-dir parse mutates the shared cfgDir package
	// var; restore it for the integration tests that follow.
	oldCfgDir := cfgDir
	t.Cleanup(func() { cfgDir = oldCfgDir })
	t.Setenv("GRAMATON_STORE", "")
	return base
}

// addNamedStore creates a named store under base with the per-store
// config.yaml `gramaton --store <name> init` would write (data_dir
// pointing at the store's own data dir), mirroring how real named
// stores are set up.
func addNamedStore(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, "stores", name)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func parseJSONMap(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("parse output: %v\nraw: %s", err, out)
	}
	return m
}

func TestStoreFreezeThawRoundTrip(t *testing.T) {
	base := newFreezeTestBase(t)
	dataDir := filepath.Join(base, "data")

	// Freeze the default store (no positional name).
	out, err := runCmd(t, "store", "freeze", "--config-dir", base)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	got := parseJSONMap(t, out)
	if got["frozen"] != "(default)" {
		t.Errorf("frozen = %v, want (default)", got["frozen"])
	}
	if got["owner"] != "Ada Lovelace <ada@example.com>" {
		t.Errorf("output owner = %v, want the composed configured author", got["owner"])
	}
	if pub, _ := got["published_at"].(string); pub == "" {
		t.Errorf("output published_at = %v, want a timestamp", got["published_at"])
	}
	if note, _ := got["note"].(string); !strings.Contains(note, "writes are rejected") {
		t.Errorf("note = %q, should say writes are rejected while reads keep working", note)
	}

	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Fatal("manifest not frozen after freeze")
	}
	if m.Owner != "Ada Lovelace <ada@example.com>" {
		t.Errorf("manifest owner = %q, want the configured author", m.Owner)
	}
	if m.PublishedAt.IsZero() {
		t.Error("manifest published_at is zero")
	}

	// Thaw: read-only clears, provenance survives and is reported.
	out, err = runCmd(t, "store", "thaw", "--config-dir", base)
	if err != nil {
		t.Fatalf("thaw: %v", err)
	}
	got = parseJSONMap(t, out)
	if got["thawed"] != "(default)" {
		t.Errorf("thawed = %v, want (default)", got["thawed"])
	}
	if got["was_frozen"] != true {
		t.Errorf("was_frozen = %v, want true", got["was_frozen"])
	}
	if got["owner"] != m.Owner {
		t.Errorf("thaw output owner = %v, want preserved %q", got["owner"], m.Owner)
	}
	if pub, _ := got["published_at"].(string); pub == "" {
		t.Errorf("thaw output published_at = %v, want the preserved timestamp", got["published_at"])
	}
	if note, _ := got["note"].(string); !strings.Contains(note, "gramaton init") {
		t.Errorf("thaw note = %q, should hint at re-running gramaton init", note)
	}

	m2, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		t.Fatalf("read manifest after thaw: %v", err)
	}
	if m2.ReadOnly {
		t.Error("manifest still frozen after thaw")
	}
	if m2.Owner != m.Owner || !m2.PublishedAt.Equal(m.PublishedAt) {
		t.Errorf("thaw dropped provenance: owner %q published %v, want %q / %v",
			m2.Owner, m2.PublishedAt, m.Owner, m.PublishedAt)
	}
}

func TestStoreFreezeNamedStoreByArgument(t *testing.T) {
	base := newFreezeTestBase(t)
	dataDir := addNamedStore(t, base, "pubstore")

	out, err := runCmd(t, "store", "freeze", "pubstore", "--config-dir", base)
	if err != nil {
		t.Fatalf("freeze pubstore: %v", err)
	}
	got := parseJSONMap(t, out)
	if got["frozen"] != "pubstore" {
		t.Errorf("frozen = %v, want pubstore", got["frozen"])
	}
	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("named store manifest not frozen")
	}

	// The default store is untouched.
	dm, err := core.ReadStoreManifest(filepath.Join(base, "data"))
	if err != nil {
		t.Fatalf("read default manifest: %v", err)
	}
	if dm.ReadOnly {
		t.Error("freezing a named store froze the default store")
	}

	// A nonexistent store is a user-facing error.
	if _, err := runCmd(t, "store", "freeze", "no-such-store", "--config-dir", base); err == nil {
		t.Error("freeze of a nonexistent store should fail")
	}
}

// TestStoreFreezeThawRefuseWhileServerAlive exercises the
// server-alive gate with a server.json naming this test process's
// own PID (guaranteed alive without starting a server), the same
// shape as cli/backfill_test.go's gate test. Both commands must
// refuse before touching the manifest.
func TestStoreFreezeThawRefuseWhileServerAlive(t *testing.T) {
	base := newFreezeTestBase(t)

	info := server.ServerInfo{PID: os.Getpid(), Port: 1, StartedAt: time.Now().UTC()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "server.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"freeze", "thaw"} {
		t.Run(sub, func(t *testing.T) {
			_, err := runCmd(t, "store", sub, "--config-dir", base)
			if err == nil {
				t.Fatalf("%s with a live server should refuse", sub)
			}
			if !strings.Contains(err.Error(), "running server") || !strings.Contains(err.Error(), "gramaton stop") {
				t.Errorf("error = %q, want the running-server refusal pointing at gramaton stop", err)
			}
		})
	}

	// The manifest was never written.
	m, err := core.ReadStoreManifest(filepath.Join(base, "data"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.ReadOnly {
		t.Error("refused freeze still flipped the manifest")
	}
}

func TestStoreCreateReadOnlyBornFrozen(t *testing.T) {
	base := newFreezeTestBase(t)
	// --read-only lives on a nested subcommand, which runCmd's
	// top-level flag reset does not cover; restore it explicitly.
	// The writable control runs FIRST for the same reason -- after a
	// --read-only parse the flag stays true until reset.
	t.Cleanup(func() { _ = storeCreateCmd.Flags().Set("read-only", "false") })

	// Control: a plain create stays writable.
	if _, err := runCmd(t, "store", "create", "plain", "--config-dir", base); err != nil {
		t.Fatalf("create: %v", err)
	}
	pm, err := core.ReadStoreManifest(filepath.Join(base, "stores", "plain", "data"))
	if err != nil {
		t.Fatalf("read plain manifest: %v", err)
	}
	if pm.ReadOnly {
		t.Error("plain create produced a frozen store")
	}

	out, err := runCmd(t, "store", "create", "published", "--read-only", "--config-dir", base)
	if err != nil {
		t.Fatalf("create --read-only: %v", err)
	}
	got := parseJSONMap(t, out)
	if got["created"] != "published" {
		t.Errorf("created = %v, want published", got["created"])
	}
	if got["read_only"] != true {
		t.Errorf("read_only = %v, want true", got["read_only"])
	}
	if got["owner"] != "Ada Lovelace <ada@example.com>" {
		t.Errorf("owner = %v, want the creating config's author", got["owner"])
	}

	m, err := core.ReadStoreManifest(filepath.Join(base, "stores", "published", "data"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Fatal("store created with --read-only is not frozen")
	}
	if m.Owner != "Ada Lovelace <ada@example.com>" {
		t.Errorf("manifest owner = %q, want the creating config's author", m.Owner)
	}

	// Regression: the badge must resolve the config-less named
	// store's OWN data dir. With the merged-config resolution, the
	// global data_dir bled through and the badge read the DEFAULT
	// store's (writable) manifest instead.
	_ = storeCreateCmd.Flags().Set("read-only", "false")
	out, err = runCmd(t, "store", "list", "--config-dir", base)
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	var entries []struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("parse list output: %v\nraw: %s", err, out)
	}
	for _, e := range entries {
		switch e.Name {
		case "published":
			if !e.ReadOnly {
				t.Error("store born frozen shows no read_only badge in store list")
			}
		case "plain", "(default)":
			if e.ReadOnly {
				t.Errorf("%s shows a read_only badge but is writable", e.Name)
			}
		}
	}
}

// TestStoreCreateReadOnlyEngineOpensOwnFrozenDataDir pins the
// badge/enforcement alignment through the REAL resolution path: a
// store born with create --read-only must be frozen where the engine
// actually opens it. The global config here carries a data_dir (the
// init default), which is exactly the bleed-through source -- without
// the per-store config `store create` now writes, the engine's
// global-then-store merge would resolve the DEFAULT store's data dir
// and open it writable while every badge said the named store was
// frozen.
func TestStoreCreateReadOnlyEngineOpensOwnFrozenDataDir(t *testing.T) {
	base := newFreezeTestBase(t)
	t.Cleanup(func() { _ = storeCreateCmd.Flags().Set("read-only", "false") })

	out, err := runCmd(t, "store", "create", "pub", "--read-only", "--config-dir", base)
	if err != nil {
		t.Fatalf("create --read-only: %v", err)
	}
	got := parseJSONMap(t, out)
	if cfgPath, _ := got["config"].(string); cfgPath == "" {
		t.Error("create output missing the per-store config path")
	}

	dir := filepath.Join(base, "stores", "pub")
	ownData := filepath.Join(dir, "data")

	// The engine's own resolution (per-store config overlaid on the
	// global) must land on the store's OWN data dir and latch the
	// frozen manifest there.
	eng, err := core.LoadEngine(dir, base)
	if err != nil {
		t.Fatalf("LoadEngine on the created store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := eng.Close(); cerr != nil {
			t.Logf("engine close: %v", cerr)
		}
	})
	if eng.Config().DataDir != ownData {
		t.Errorf("engine data dir = %q, want the store's own %q (global data_dir bled through)",
			eng.Config().DataDir, ownData)
	}
	if !eng.ReadOnly() {
		t.Error("engine opened the store writable; the read_only badge is not enforced where the engine opens")
	}

	// The DEFAULT store was never frozen.
	if _, serr := os.Stat(filepath.Join(base, "data", "STORE")); !os.IsNotExist(serr) {
		t.Errorf("default store gained a STORE manifest, stat err = %v", serr)
	}

	// The "always" half: a plain create also pins its own data_dir.
	_ = storeCreateCmd.Flags().Set("read-only", "false")
	if _, err := runCmd(t, "store", "create", "plain2", "--config-dir", base); err != nil {
		t.Fatalf("plain create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(base, "stores", "plain2", "config.yaml"))
	if err != nil {
		t.Fatalf("plain create wrote no per-store config: %v", err)
	}
	if !strings.Contains(string(raw), "data_dir:") {
		t.Errorf("per-store config missing data_dir:\n%s", raw)
	}
}

// TestStoreServerAppearedWarning exercises the post-write re-probe
// directly: the freeze/thaw guard is check-then-act, so the only
// end-to-end trigger is a server appearing mid-command -- injected
// here as a server.json (own PID, the guard-test pattern) present at
// re-probe time.
func TestStoreServerAppearedWarning(t *testing.T) {
	base := t.TempDir()

	if w := storeServerAppearedWarning("", base, "frozen"); w != "" {
		t.Errorf("no server.json: warning = %q, want empty", w)
	}

	writeInfo := func(pid int) {
		t.Helper()
		info := server.ServerInfo{PID: pid, Port: 1, StartedAt: time.Now().UTC()}
		data, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "server.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeInfo(os.Getpid())
	w := storeServerAppearedWarning("", base, "frozen")
	for _, want := range []string{"frozen", "before the change", "gramaton stop"} {
		if !strings.Contains(w, want) {
			t.Errorf("default-store warning = %q, want it to mention %q", w, want)
		}
	}
	if w := storeServerAppearedWarning("pub", base, "thawed"); !strings.Contains(w, "gramaton --store pub stop") {
		t.Errorf("named-store warning = %q, want the --store stop hint", w)
	}

	// A stale server.json (dead PID) is not a server that appeared.
	writeInfo(1 << 30)
	if w := storeServerAppearedWarning("", base, "frozen"); w != "" {
		t.Errorf("dead PID: warning = %q, want empty", w)
	}
}

func TestStoreListReadOnlyBadge(t *testing.T) {
	base := newFreezeTestBase(t)
	frozenData := addNamedStore(t, base, "frozen")
	addNamedStore(t, base, "writable")
	corruptData := addNamedStore(t, base, "corrupt")

	if err := core.FreezeStore(frozenData, "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	// An unparseable manifest must surface as unreadable, never as
	// writable.
	if err := os.WriteFile(filepath.Join(corruptData, "STORE"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCmd(t, "store", "list", "--config-dir", base)
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	var entries []struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("parse list output: %v\nraw: %s", err, out)
	}

	byName := map[string]struct {
		readOnly bool
		manifest string
	}{}
	for _, e := range entries {
		byName[e.Name] = struct {
			readOnly bool
			manifest string
		}{e.ReadOnly, e.Manifest}
	}

	if got := byName["frozen"]; !got.readOnly || got.manifest != "" {
		t.Errorf("frozen store badge = %+v, want read_only=true and no manifest note", got)
	}
	for _, name := range []string{"(default)", "writable"} {
		if got := byName[name]; got.readOnly || got.manifest != "" {
			t.Errorf("%s badge = %+v, want writable with no manifest note", name, got)
		}
	}
	if got := byName["corrupt"]; got.readOnly || got.manifest != "(manifest unreadable)" {
		t.Errorf("corrupt store badge = %+v, want manifest \"(manifest unreadable)\" and read_only=false", got)
	}
}

// TestStatusFallbackReportsStoreReadonly pins the no-server status
// path: with no server running, `gramaton status` reads the STORE
// manifest directly and carries the same store_readonly field the
// server envelope would, omitted entirely on a writable store.
func TestStatusFallbackReportsStoreReadonly(t *testing.T) {
	base := newFreezeTestBase(t)
	dataDir := filepath.Join(base, "data")

	if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	out, err := runCmd(t, "status", "--config-dir", base)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	got := parseJSONMap(t, out)
	if got["server"] != "not running" {
		t.Fatalf("expected the no-server fallback path, got %v", got["server"])
	}
	if got["store_readonly"] != true {
		t.Errorf("status fallback store_readonly = %v, want true", got["store_readonly"])
	}

	// Thawed: field omitted, matching the envelope's omitempty.
	if err := core.ThawStore(dataDir); err != nil {
		t.Fatalf("thaw: %v", err)
	}
	out, err = runCmd(t, "status", "--config-dir", base)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	got = parseJSONMap(t, out)
	if _, present := got["store_readonly"]; present {
		t.Errorf("writable store status should omit store_readonly, got %v", got["store_readonly"])
	}
}

// TestStoreFreezeThawCommandsRegistered protects the CLI wiring,
// mirroring backfill_test.go's registration test.
func TestStoreFreezeThawCommandsRegistered(t *testing.T) {
	foundFreeze, foundThaw := false, false
	for _, sub := range storeCmd.Commands() {
		switch sub {
		case storeFreezeCmd:
			foundFreeze = true
		case storeThawCmd:
			foundThaw = true
		}
	}
	if !foundFreeze {
		t.Error("freeze subcommand not registered on storeCmd")
	}
	if !foundThaw {
		t.Error("thaw subcommand not registered on storeCmd")
	}
	if f := storeCreateCmd.Flags().Lookup("read-only"); f == nil {
		t.Error("read-only flag not registered on store create")
	} else if f.Value.Type() != "bool" {
		t.Errorf("read-only flag type = %q, want bool", f.Value.Type())
	}
}
