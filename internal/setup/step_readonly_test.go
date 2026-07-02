package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// buildSharedFixtureStore creates a tiny REAL store dir shaped like a
// shared artifact: <dir>/data holding FORMAT, HEAD, refs, and one
// committed record, produced by an actual engine (not handwritten
// files) so the copy the wizard makes is a store the engine could
// open. frozen controls whether the artifact is published
// (core.FreezeStore) or left writable/manifest-less.
func buildSharedFixtureStore(t *testing.T, frozen bool) (storeDir, dataDir string) {
	t.Helper()
	storeDir = t.TempDir()
	dataDir = filepath.Join(storeDir, "data")

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfg, filepath.Join(storeDir, "config.yaml")); err != nil {
		t.Fatal(err)
	}

	eng, err := core.LoadEngineWithOptions(storeDir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngineWithOptions: %v", err)
	}
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("shared knowledge survives the copy"),
		"temporality":  graph.StringProperty("durable"),
	})
	eng.IndexNode(n.ID, "shared knowledge survives the copy", nil)
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("seed save: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close fixture engine: %v", err)
	}

	if frozen {
		if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
			t.Fatalf("freeze fixture: %v", err)
		}
	}
	return storeDir, dataDir
}

// newWizardForReadOnlyTest builds a wizard driven down the read-only
// attach route. HOME/PATH are sandboxed (stepVerify never runs here,
// but the guidance install writes to $HOME/.claude/CLAUDE.md).
func newWizardForReadOnlyTest(t *testing.T, backend MCPBackend, answers ...string) (*Wizard, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("PATH", "")
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	var buf bytes.Buffer
	prompter := NewScriptedPrompter(answers...)
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(tmpDir, "data")

	wiz := New(prompter, NewWriter(&buf), &cfg, filepath.Join(tmpDir, "config.yaml"), tmpDir)
	wiz.mcpBackend = backend
	return wiz, &buf, tmpDir
}

// TestWizardReadOnlyAttachEndToEnd drives route [3] against a real
// frozen fixture store: path prompt -> name prompt -> attach. The
// scripted answer count pins that identity, LLM, and hooks never
// prompt -- a single extra prompt would drain the script and abort
// the run.
func TestWizardReadOnlyAttachEndToEnd(t *testing.T) {
	srcStoreDir, srcDataDir := buildSharedFixtureStore(t, true)

	backend := &fakeMCPBackend{
		clients: []DetectedClient{{Name: "Claude Code", Binary: "/fake/bin/claude"}},
	}
	// Answers, in order:
	//   [0] Step 0 route: "3" (attach read-only)
	//   [1] shared-store path (the STORE dir; data dir resolution is
	//       exercised by the store package's own tests)
	//   [2] store name: "shared-notes"
	//   [3] MCP confirm: "y"
	//   [4] guidance confirm for Claude Code: "y"
	// No freeze-original offer: the artifact is already frozen.
	wiz, buf, base := newWizardForReadOnlyTest(t, backend,
		"3", srcStoreDir, "shared-notes", "y", "y")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	// The prompter consumed exactly the scripted answers: identity,
	// LLM, and hooks never asked anything.
	if got, want := wiz.prompter.(*ScriptedPrompter).pos, 5; got != want {
		t.Errorf("prompter consumed %d answers, want exactly %d (an extra prompt means a skipped step ran)", got, want)
	}

	// Copied data dir exists and carries the engine's files.
	destData := filepath.Join(base, "stores", "shared-notes", "data")
	for _, f := range []string{"FORMAT", "HEAD"} {
		if _, err := os.Stat(filepath.Join(destData, f)); err != nil {
			t.Errorf("copied data dir missing %s: %v", f, err)
		}
	}

	// STORE manifest frozen on the copy, provenance preserved.
	m, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("read copied manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("copied store manifest not frozen")
	}
	if m.Owner != "Ada Lovelace <ada@example.com>" {
		t.Errorf("copy owner = %q, want the publisher's provenance preserved", m.Owner)
	}

	// Per-store config is minimal: data_dir only, no llm/author.
	cfgRaw, err := os.ReadFile(filepath.Join(base, "stores", "shared-notes", "config.yaml"))
	if err != nil {
		t.Fatalf("per-store config missing: %v", err)
	}
	cfgStr := string(cfgRaw)
	if !strings.Contains(cfgStr, "data_dir:") {
		t.Errorf("per-store config missing data_dir:\n%s", cfgStr)
	}
	for _, banned := range []string{"llm:", "author:", "api_key"} {
		if strings.Contains(cfgStr, banned) {
			t.Errorf("per-store config must not contain %q:\n%s", banned, cfgStr)
		}
	}

	// The GLOBAL config was deliberately not written: a later
	// `gramaton init` must still offer the full wizard.
	if _, err := os.Stat(filepath.Join(base, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("read-only route must not write the global config.yaml, stat err = %v", err)
	}

	// MCP registration went through the per-store seam with the
	// --store form (the fake records client:store).
	if len(backend.storeCalls) != 1 || backend.storeCalls[0] != "Claude Code:shared-notes" {
		t.Errorf("RegisterStore calls = %v, want [Claude Code:shared-notes]", backend.storeCalls)
	}
	if len(backend.calls) != 0 {
		t.Errorf("default-entry Register must not run on the read-only route: %v", backend.calls)
	}
	if !strings.Contains(out, "gramaton-shared-notes") {
		t.Errorf("output should name the per-store MCP entry:\n%s", out)
	}

	// Read-only guidance variant installed into the sandboxed home.
	guidance, err := os.ReadFile(filepath.Join(base, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("guidance not installed: %v", err)
	}
	for _, want := range []string{
		"read-only",
		"gramaton_search",
		"gramaton_inspect",
		"gramaton_explore",
		"gramaton --store shared-notes search",
		instructionsFenceBeginPrefix,
	} {
		if !strings.Contains(string(guidance), want) {
			t.Errorf("installed guidance missing %q", want)
		}
	}
	if strings.Contains(string(guidance), "Gramaton has three save paths") {
		t.Error("installed guidance looks like the writable base template, not the read-only variant")
	}

	// The original artifact is untouched (still frozen, same files).
	if _, err := os.Stat(filepath.Join(srcDataDir, "FORMAT")); err != nil {
		t.Errorf("source data dir damaged: %v", err)
	}

	// Summary content.
	for _, want := range []string{
		"Store attached: shared-notes [read-only]",
		"published by Ada Lovelace <ada@example.com>",
		"Read-only store attached.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

// TestWizardReadOnlyAttachWritableArtifact covers the manifest-less
// artifact: the wizard offers to freeze the original (declined ->
// original untouched) and freezes the LOCAL COPY regardless.
func TestWizardReadOnlyAttachWritableArtifact(t *testing.T) {
	srcStoreDir, srcDataDir := buildSharedFixtureStore(t, false)

	backend := &fakeMCPBackend{} // no clients: no MCP/guidance prompts
	// Answers: route "3", path, freeze-original "n", name enter-through
	// (accept the derived default).
	wiz, buf, base := newWizardForReadOnlyTest(t, backend,
		"3", srcStoreDir, "n", "")

	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "isn't frozen on disk") {
		t.Errorf("writable-artifact explanation missing:\n%s", out)
	}

	// Original directory untouched: no STORE manifest appeared.
	if _, err := os.Stat(filepath.Join(srcDataDir, "STORE")); !os.IsNotExist(err) {
		t.Errorf("declining the freeze offer must leave the original manifest-less, stat err = %v", err)
	}

	// The local copy IS frozen, under the derived default name (the
	// fixture store dir is a TempDir named TestWizardReadOnly...).
	stores, err := os.ReadDir(filepath.Join(base, "stores"))
	if err != nil || len(stores) != 1 {
		t.Fatalf("want exactly one attached store, got %v (err %v)", stores, err)
	}
	destData := filepath.Join(base, "stores", stores[0].Name(), "data")
	m, err := core.ReadStoreManifest(destData)
	if err != nil {
		t.Fatalf("read copied manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("local copy of a writable artifact must be frozen")
	}
	if m.Owner != "" {
		t.Errorf("locally-frozen copy owner = %q, want empty (no identity on this route)", m.Owner)
	}
}

// TestWizardReadOnlyAttachFormatMismatch rejects an artifact whose
// FORMAT version this binary doesn't support, before anything is
// created.
func TestWizardReadOnlyAttachFormatMismatch(t *testing.T) {
	src := t.TempDir()
	dataDir := filepath.Join(src, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "FORMAT"), []byte("999"), 0o600); err != nil {
		t.Fatal(err)
	}

	wiz, buf, base := newWizardForReadOnlyTest(t, &fakeMCPBackend{}, "3", src)
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "store format version 999 is newer") {
		t.Errorf("format-mismatch rejection missing:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(base, "stores")); !os.IsNotExist(err) {
		t.Errorf("rejected attach must not create stores/, stat err = %v", err)
	}
	// The rejected artifact keeps its FORMAT byte-identical; the
	// validation path must never run CheckFormatVersion's write.
	raw, _ := os.ReadFile(filepath.Join(dataDir, "FORMAT"))
	if string(raw) != "999" {
		t.Errorf("source FORMAT modified: %q", raw)
	}
}

// TestWizardReadOnlyAttachMissingFormat rejects a directory that
// isn't a gramaton store at all (no FORMAT at the path or under
// data/).
func TestWizardReadOnlyAttachMissingFormat(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data"), 0o700); err != nil {
		t.Fatal(err)
	}

	wiz, buf, base := newWizardForReadOnlyTest(t, &fakeMCPBackend{}, "3", src)
	if err := wiz.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "doesn't look like a Gramaton store") {
		t.Errorf("missing-FORMAT rejection missing:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(base, "stores")); !os.IsNotExist(err) {
		t.Errorf("rejected attach must not create stores/, stat err = %v", err)
	}
	// A FORMAT file must not have been created anywhere in the
	// rejected source (ReadFormatVersion-only validation).
	if _, err := os.Stat(filepath.Join(src, "data", "FORMAT")); !os.IsNotExist(err) {
		t.Errorf("validation wrote a FORMAT file into the source, stat err = %v", err)
	}
}

// TestSetupRoutePromptPinsReadOnlyClarity pins the route-choice
// screen's two user-demanded properties: the [3] option itself says
// read-only ONLY / no write tools / nothing ever saved, and the
// screen advertises `gramaton store attach` for users who want a
// shared store alongside their own writable one. A future rewording
// that drops either fails here, deliberately.
func TestSetupRoutePromptPinsReadOnlyClarity(t *testing.T) {
	var buf bytes.Buffer
	cfg := config.Defaults()
	wiz := New(NewScriptedPrompter("1"), NewWriter(&buf), &cfg,
		filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())

	route, err := wiz.askSetupRoute()
	if err != nil {
		t.Fatalf("askSetupRoute: %v", err)
	}
	if route != routeFresh {
		t.Errorf("route = %v, want routeFresh", route)
	}

	out := buf.String()
	for _, want := range []string{
		"read-only ONLY",
		"no write",
		"nothing is ever",
		"saved",
		"gramaton store attach",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("route-choice prompt missing %q:\n%s", want, out)
		}
	}
}

// TestReadOnlyGuidanceVariantRendersForEachHarness sanity-checks the
// read-only body for every registry harness: it must mention the
// read-only state, interpolate the store name into the CLI-fallback
// line, and carry Cursor's frontmatter header where one is defined.
func TestReadOnlyGuidanceVariantRendersForEachHarness(t *testing.T) {
	for _, h := range harnesses {
		body := readOnlyBodyForClient(h.Name, "shared")
		if !strings.Contains(body, "read-only") {
			t.Errorf("%s: read-only variant doesn't say read-only", h.Name)
		}
		if !strings.Contains(body, "gramaton --store shared search") {
			t.Errorf("%s: store name not interpolated into the CLI fallback", h.Name)
		}
		if strings.Contains(body, "{{") {
			t.Errorf("%s: unfilled interpolation vars in the read-only variant", h.Name)
		}
		if h.InstructionsHeader != nil && !strings.HasPrefix(body, h.InstructionsHeader()) {
			t.Errorf("%s: InstructionsHeader not prepended", h.Name)
		}
	}
}

// TestWizardReadOnlyAttachFormatMatchesBinary keeps the fixture
// honest: the engine-built store must carry the CURRENT format
// version, otherwise the end-to-end tests above would be exercising
// the mismatch path by accident.
func TestWizardReadOnlyAttachFormatMatchesBinary(t *testing.T) {
	_, dataDir := buildSharedFixtureStore(t, false)
	v, err := core.ReadFormatVersion(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if v != version.StoreFormatVersion {
		t.Fatalf("fixture FORMAT = %d, want %d", v, version.StoreFormatVersion)
	}
}
