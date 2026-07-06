package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/spf13/pflag"
)

// resetStoreCreateFlags clears storeCreateCmd's flag state after a test
// that set a carve seed (--from-id) or --dry-run. runCmd only resets
// the flags of rootCmd's DIRECT children, not the nested storeCreateCmd,
// so a leaked --from-id would flip later offline `store create` calls
// into server-mediated carves (same nested-flag-leak hazard as
// resetStoreAttachFlags). carveRequested gates on flag.Changed, so
// clearing Changed is what actually prevents the leak.
func resetStoreCreateFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		storeCreateCmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			// Slice flags (--from-id, --from-collection) keep an internal
			// "changed" state that makes a later parse APPEND rather than
			// replace, so clearing Changed alone would let one carve
			// test's ids bleed into the next. Replace empties the slice;
			// clearing Changed is enough for scalar seeds (carveRequested
			// and the --dry-run guard both key off Changed, not value).
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			}
		})
	})
}

// fakeStoreHarnessBackend is a no-op/recording MCP backend for the
// store-lifecycle harness-sync tests. The default (empty clients) is
// the suite-wide backend TestMain installs so no test shells out to a
// real vendor CLI; tests that assert wiring swap in one with clients.
type fakeStoreHarnessBackend struct {
	clients []setup.DetectedClient
	// registered records Register ("<client>") and RegisterStore
	// ("<client>:<store>") calls; removed records RemoveStore
	// ("<client>:<store>", "<client>:" for the default entry).
	registered []string
	removed    []string
	// removePresent makes RemoveStore report the entry existed.
	removePresent bool
	// entries is what ListEntries reports for every client (the
	// registered gramaton-owned entry names), for the list column and
	// sync-harness prune tests.
	entries []string
	// registerErr / removeErr, when set, make the register / remove ops
	// fail, for exercising failure surfacing in command output.
	registerErr error
	removeErr   error
}

func (f *fakeStoreHarnessBackend) Detect() []setup.DetectedClient { return f.clients }

func (f *fakeStoreHarnessBackend) Register(_ context.Context, c setup.DetectedClient) (bool, error) {
	f.registered = append(f.registered, c.Name)
	return false, f.registerErr
}

func (f *fakeStoreHarnessBackend) RegisterStore(_ context.Context, c setup.DetectedClient, storeName string) (bool, error) {
	f.registered = append(f.registered, c.Name+":"+storeName)
	return false, f.registerErr
}

func (f *fakeStoreHarnessBackend) RemoveStore(_ context.Context, c setup.DetectedClient, storeName string) (bool, error) {
	f.removed = append(f.removed, c.Name+":"+storeName)
	if f.removeErr != nil {
		return false, f.removeErr
	}
	return f.removePresent, nil
}

func (f *fakeStoreHarnessBackend) ListEntries(_ context.Context, _ setup.DetectedClient) ([]string, error) {
	return f.entries, nil
}

// withStoreHarness swaps the package harness backend for the duration
// of one test and restores the suite default (and the --no-harness
// flag, which runCmd's flag reset does not reach on the nested
// subcommand) afterward.
func withStoreHarness(t *testing.T, b *fakeStoreHarnessBackend) {
	t.Helper()
	prev := storeHarnessBackend
	storeHarnessBackend = b
	t.Cleanup(func() {
		storeHarnessBackend = prev
		// These flags live on nested subcommands, which runCmd's
		// top-level flag reset does not reach, so a --no-harness /
		// --harness / --prune test would otherwise leak the flag into
		// later plain store-command tests.
		storeNoHarness = false
		storeListHarness = false
		storeSyncPrune = false
	})
}

func oneClient() []setup.DetectedClient {
	return []setup.DetectedClient{{Name: "Claude Code", Binary: "/usr/bin/claude"}}
}

func TestStoreCreateRegistersHarness(t *testing.T) {
	base := newFreezeTestBase(t)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "create", "proj", "--config-dir", base)
	if err != nil {
		t.Fatalf("store create: %v", err)
	}
	// A named store registers via RegisterStore for every detected
	// harness, immediately after creation.
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:proj"}) {
		t.Errorf("registered = %v, want [Claude Code:proj]", fake.registered)
	}
	got := parseJSONMap(t, out)
	h, ok := got["harness"].(map[string]any)
	if !ok {
		t.Fatalf("output has no harness report: %v", got["harness"])
	}
	reg, _ := h["registered"].([]any)
	if len(reg) != 1 || reg[0] != "Claude Code" {
		t.Errorf("harness.registered = %v, want [Claude Code]", h["registered"])
	}
}

func TestStoreCreateNoHarnessSkips(t *testing.T) {
	base := newFreezeTestBase(t)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)

	if _, err := runCmd(t, "store", "create", "quiet", "--no-harness", "--config-dir", base); err != nil {
		t.Fatalf("store create --no-harness: %v", err)
	}
	if len(fake.registered) != 0 {
		t.Errorf("--no-harness still registered: %v", fake.registered)
	}
}

func TestStoreDeleteDeregistersHarness(t *testing.T) {
	base := newFreezeTestBase(t)
	// Create with the suite default (no-op) backend so nothing is wired,
	// then delete with a fake that reports the entry present.
	if _, err := runCmd(t, "store", "create", "gone", "--config-dir", base); err != nil {
		t.Fatalf("store create: %v", err)
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient(), removePresent: true}
	withStoreHarness(t, fake)

	if _, err := runCmd(t, "store", "delete", "gone", "--force", "--config-dir", base); err != nil {
		t.Fatalf("store delete: %v", err)
	}
	if !reflect.DeepEqual(fake.removed, []string{"Claude Code:gone"}) {
		t.Errorf("removed = %v, want [Claude Code:gone]", fake.removed)
	}
}

func TestStoreRenameRepointsHarness(t *testing.T) {
	base := newFreezeTestBase(t)
	if _, err := runCmd(t, "store", "create", "a", "--config-dir", base); err != nil {
		t.Fatalf("store create: %v", err)
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient(), removePresent: true}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "rename", "a", "b", "--config-dir", base)
	if err != nil {
		t.Fatalf("store rename: %v", err)
	}
	// New entry registered, old entry removed.
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:b"}) {
		t.Errorf("registered = %v, want [Claude Code:b]", fake.registered)
	}
	if !reflect.DeepEqual(fake.removed, []string{"Claude Code:a"}) {
		t.Errorf("removed = %v, want [Claude Code:a]", fake.removed)
	}
	got := parseJSONMap(t, out)
	h, _ := got["harness"].(map[string]any)
	if h["new_entry"] != "gramaton-b" || h["old_entry"] != "gramaton-a" {
		t.Errorf("harness entries = %v/%v, want gramaton-b/gramaton-a", h["new_entry"], h["old_entry"])
	}
}

func TestStoreRenameDefaultToNamedFlipsEntry(t *testing.T) {
	base := newFreezeTestBase(t) // base already has a default store with data
	fake := &fakeStoreHarnessBackend{clients: oneClient(), removePresent: true}
	withStoreHarness(t, fake)

	if _, err := runCmd(t, "store", "rename", "default", "work", "--config-dir", base); err != nil {
		t.Fatalf("store rename default->work: %v", err)
	}
	// default -> named: register the named entry, remove the bare
	// default "gramaton" entry (empty store name).
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:work"}) {
		t.Errorf("registered = %v, want [Claude Code:work]", fake.registered)
	}
	if !reflect.DeepEqual(fake.removed, []string{"Claude Code:"}) {
		t.Errorf("removed = %v, want the default entry [Claude Code:]", fake.removed)
	}
}

func TestStoreAttachRegistersHarness(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)
	srcStoreDir, _ := newAttachSourceDir(t, true)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "shared", "--config-dir", base)
	if err != nil {
		t.Fatalf("store attach: %v", err)
	}
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:shared"}) {
		t.Errorf("registered = %v, want [Claude Code:shared]", fake.registered)
	}
	got := parseJSONMap(t, out)
	// A real registration replaces the old manual-hint field.
	if _, present := got["mcp"]; present {
		t.Errorf("attach registered the entry; it must not also print a manual mcp hint: %v", got["mcp"])
	}
	if _, present := got["harness"]; !present {
		t.Error("attach output should carry a harness report")
	}
}

func TestStoreAttachNoHarnessKeepsManualHint(t *testing.T) {
	base := newFreezeTestBase(t)
	resetStoreAttachFlags(t)
	srcStoreDir, _ := newAttachSourceDir(t, true)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "attach", srcStoreDir, "--name", "manual", "--no-harness", "--config-dir", base)
	if err != nil {
		t.Fatalf("store attach --no-harness: %v", err)
	}
	if len(fake.registered) != 0 {
		t.Errorf("--no-harness still registered: %v", fake.registered)
	}
	got := parseJSONMap(t, out)
	if mcp, _ := got["mcp"].(string); !strings.Contains(mcp, "gramaton --store manual mcp") {
		t.Errorf("mcp hint = %q, want the manual --store form under --no-harness", got["mcp"])
	}
}

func TestStoreListHarnessColumn(t *testing.T) {
	base := newFreezeTestBase(t)
	// Two named stores; the fake reports only "wired" as registered.
	for _, n := range []string{"wired", "loose"} {
		if _, err := runCmd(t, "store", "create", n, "--config-dir", base); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient(), entries: []string{"gramaton-wired"}}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "list", "--harness", "--config-dir", base)
	if err != nil {
		t.Fatalf("store list --harness: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("parse list: %v\n%s", err, out)
	}
	byName := map[string]map[string]any{}
	for _, e := range list {
		byName[e["name"].(string)] = e
	}
	if h, _ := byName["wired"]["harness"].([]any); len(h) != 1 || h[0] != "Claude Code" {
		t.Errorf("wired harness = %v, want [Claude Code]", byName["wired"]["harness"])
	}
	if _, present := byName["loose"]["harness_note"]; !present {
		t.Errorf("loose store should carry a harness_note, got %v", byName["loose"])
	}
}

func TestStoreSyncHarnessRegistersAll(t *testing.T) {
	base := newFreezeTestBase(t) // includes the default store
	for _, n := range []string{"one", "two"} {
		if _, err := runCmd(t, "store", "create", n, "--config-dir", base); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)

	if _, err := runCmd(t, "store", "sync-harness", "--config-dir", base); err != nil {
		t.Fatalf("store sync-harness: %v", err)
	}
	// store.List returns the default store first, then names sorted: so
	// the default registers via Register ("Claude Code"), then the two
	// named stores via RegisterStore.
	want := []string{"Claude Code", "Claude Code:one", "Claude Code:two"}
	if !reflect.DeepEqual(fake.registered, want) {
		t.Errorf("registered = %v, want %v", fake.registered, want)
	}
}

func TestStoreSyncHarnessPrunesOrphan(t *testing.T) {
	base := newFreezeTestBase(t) // default store exists; no named stores
	fake := &fakeStoreHarnessBackend{
		clients:       oneClient(),
		removePresent: true,
		// A live default entry plus an orphan for a since-deleted store.
		entries: []string{"gramaton", "gramaton-ghost"},
	}
	withStoreHarness(t, fake)

	if _, err := runCmd(t, "store", "sync-harness", "--prune", "--config-dir", base); err != nil {
		t.Fatalf("store sync-harness --prune: %v", err)
	}
	found := false
	for _, r := range fake.removed {
		if r == "Claude Code:ghost" {
			found = true
		}
		if r == "Claude Code:" {
			t.Error("prune must never remove the default gramaton entry")
		}
	}
	if !found {
		t.Errorf("prune should remove orphan gramaton-ghost, removed = %v", fake.removed)
	}
}

func TestStoreCreateCarveRegistersHarness(t *testing.T) {
	// A committed carve (`store create --from-id`) materializes a new
	// named store server-side; its MCP entry must be registered too, the
	// same as the offline create path. Uses the shared suite server as
	// the carve source; cleans up the materialized store dir.
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	resetStoreCreateFlags(t)
	t.Cleanup(func() { os.RemoveAll(filepath.Join(testCfgDir, "stores", "carvedstore")) })

	if _, err := runCmd(t, "store", "create", "carvedstore", "--from-id", testStore.HealthAllergy); err != nil {
		t.Fatalf("store create --from-id: %v", err)
	}
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:carvedstore"}) {
		t.Errorf("registered = %v, want [Claude Code:carvedstore] after a committed carve", fake.registered)
	}
}

func TestStoreCreateCarveDryRunSkipsHarness(t *testing.T) {
	// A dry-run carve writes nothing, so it must NOT register an entry.
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	resetStoreCreateFlags(t)

	if _, err := runCmd(t, "store", "create", "previewstore", "--from-id", testStore.HealthAllergy, "--dry-run"); err != nil {
		t.Fatalf("dry-run carve: %v", err)
	}
	if len(fake.registered) != 0 {
		t.Errorf("a dry-run carve must not register anything, got %v", fake.registered)
	}
}

func TestStoreDeleteHarnessFailureSurfaces(t *testing.T) {
	base := newFreezeTestBase(t)
	if _, err := runCmd(t, "store", "create", "doomed", "--config-dir", base); err != nil {
		t.Fatalf("create: %v", err)
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient(), removeErr: errors.New("mcp remove failed")}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "delete", "doomed", "--force", "--config-dir", base)
	// The on-disk delete committed; a harness hiccup is a warning, not a
	// command failure.
	if err != nil {
		t.Fatalf("delete should succeed on disk despite a harness failure: %v", err)
	}
	got := parseJSONMap(t, out)
	h, _ := got["harness"].(map[string]any)
	failed, _ := h["failed"].(map[string]any)
	if _, ok := failed["Claude Code"]; !ok {
		t.Errorf("a RemoveStore failure must surface in output.harness.failed, got %v", h)
	}
}

func TestStoreRenameHarnessFailurePrefersRegister(t *testing.T) {
	base := newFreezeTestBase(t)
	if _, err := runCmd(t, "store", "create", "r1", "--config-dir", base); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A client that fails BOTH legs: the new-entry register and the
	// old-entry remove.
	fake := &fakeStoreHarnessBackend{
		clients:     oneClient(),
		registerErr: errors.New("register boom"),
		removeErr:   errors.New("remove boom"),
	}
	withStoreHarness(t, fake)

	out, err := runCmd(t, "store", "rename", "r1", "r2", "--config-dir", base)
	if err != nil {
		t.Fatalf("rename should succeed on disk despite a harness failure: %v", err)
	}
	got := parseJSONMap(t, out)
	h, _ := got["harness"].(map[string]any)
	failed, _ := h["failed"].(map[string]any)
	msg, _ := failed["Claude Code"].(string)
	// On a same-client collision the register (new-entry) failure is the
	// one kept -- a store left unreachable matters more than a stale old
	// entry.
	if !strings.Contains(msg, "register boom") {
		t.Errorf("failed[Claude Code] = %q, want the register failure preserved over the remove failure", msg)
	}
}
