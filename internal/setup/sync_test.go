package setup

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// twoClients is the standard detected-harness fixture for sync tests:
// a CLI harness and a dir-detected one, so both register paths and the
// order-preservation are exercised.
func twoClients() []DetectedClient {
	return []DetectedClient{
		{Name: harnessClaudeCode, Binary: "/usr/bin/claude"},
		{Name: harnessCursor}, // dir-detected: empty Binary
	}
}

func TestSyncStoreHarnessPresentNamed(t *testing.T) {
	b := &fakeMCPBackend{clients: twoClients()}
	rep := SyncStoreHarness(context.Background(), b, "team", EntryPresent)

	if rep.Entry != "gramaton-team" {
		t.Errorf("entry = %q, want gramaton-team", rep.Entry)
	}
	// A named store registers via RegisterStore, never the default
	// Register.
	if got := b.storeCalls; !reflect.DeepEqual(got, []string{"Claude Code:team", "Cursor:team"}) {
		t.Errorf("RegisterStore calls = %v, want both clients for team", got)
	}
	if len(b.calls) != 0 {
		t.Errorf("default Register must not be called for a named store, got %v", b.calls)
	}
	if got := rep.Registered(); !reflect.DeepEqual(got, []string{"Claude Code", "Cursor"}) {
		t.Errorf("Registered() = %v, want both clients", got)
	}
}

func TestSyncStoreHarnessPresentDefault(t *testing.T) {
	b := &fakeMCPBackend{clients: twoClients()}
	rep := SyncStoreHarness(context.Background(), b, "", EntryPresent)

	if rep.Entry != "gramaton" {
		t.Errorf("entry = %q, want gramaton", rep.Entry)
	}
	// The default store registers via Register, never the per-store
	// RegisterStore.
	if !reflect.DeepEqual(b.calls, []string{"Claude Code", "Cursor"}) {
		t.Errorf("Register calls = %v, want both clients", b.calls)
	}
	if len(b.storeCalls) != 0 {
		t.Errorf("RegisterStore must not be called for the default store, got %v", b.storeCalls)
	}
}

func TestSyncStoreHarnessAlreadyRegistered(t *testing.T) {
	b := &fakeMCPBackend{
		clients:   twoClients(),
		registers: []fakeRegisterResult{{already: true}, {already: true}},
	}
	// Default store so the register queue is consumed by Register.
	rep := SyncStoreHarness(context.Background(), b, "", EntryPresent)
	for _, c := range rep.Clients {
		if c.Action != SyncAlreadyRegistered {
			t.Errorf("%s action = %q, want already registered", c.Client, c.Action)
		}
	}
	// Already-registered still counts as present.
	if got := rep.Registered(); len(got) != 2 {
		t.Errorf("Registered() = %v, want both (already-registered is present)", got)
	}
}

func TestSyncStoreHarnessPresentFailureIsolated(t *testing.T) {
	// First client fails; the second must still be attempted (warn-and-
	// continue), and the failure surfaces in the report, not as a
	// top-level abort.
	b := &fakeMCPBackend{
		clients:   twoClients(),
		registers: []fakeRegisterResult{{err: errors.New("boom")}, {}},
	}
	rep := SyncStoreHarness(context.Background(), b, "", EntryPresent)

	if len(b.calls) != 2 {
		t.Fatalf("both clients must be attempted despite the first failing, calls = %v", b.calls)
	}
	fails := rep.Failures()
	if len(fails) != 1 || fails[0].Client != "Claude Code" {
		t.Fatalf("Failures() = %v, want exactly the first client", fails)
	}
	if got := rep.Registered(); !reflect.DeepEqual(got, []string{"Cursor"}) {
		t.Errorf("Registered() = %v, want only the surviving client", got)
	}
}

func TestSyncStoreHarnessAbsentRemovesPresent(t *testing.T) {
	b := &fakeMCPBackend{clients: twoClients(), removePresent: true}
	rep := SyncStoreHarness(context.Background(), b, "team", EntryAbsent)

	if !reflect.DeepEqual(b.removeCalls, []string{"Claude Code:team", "Cursor:team"}) {
		t.Errorf("RemoveStore calls = %v, want both clients for team", b.removeCalls)
	}
	if got := rep.Removed(); !reflect.DeepEqual(got, []string{"Claude Code", "Cursor"}) {
		t.Errorf("Removed() = %v, want both clients", got)
	}
}

func TestSyncStoreHarnessAbsentIdempotentWhenAbsent(t *testing.T) {
	// Entry not present anywhere: a clean no-op, no failures.
	b := &fakeMCPBackend{clients: twoClients(), removePresent: false}
	rep := SyncStoreHarness(context.Background(), b, "team", EntryAbsent)

	if len(rep.Removed()) != 0 {
		t.Errorf("Removed() = %v, want none (entry was absent)", rep.Removed())
	}
	if len(rep.Failures()) != 0 {
		t.Errorf("removing an absent entry must not fail, got %v", rep.Failures())
	}
	for _, c := range rep.Clients {
		if c.Action != SyncNotPresent {
			t.Errorf("%s action = %q, want not present", c.Client, c.Action)
		}
	}
}

func TestSyncStoreHarnessAbsentFailureSurfaces(t *testing.T) {
	// A RemoveStore failure must surface as SyncFailed, never be masked
	// as a clean "not present": delete/rename/prune rely on the report to
	// tell the user a deregistration failed, or a stale orphan entry
	// lingers silently. Symmetric with the present-path failure test.
	b := &fakeMCPBackend{clients: twoClients(), removeErr: errors.New("mcp remove exploded")}
	rep := SyncStoreHarness(context.Background(), b, "team", EntryAbsent)

	if fails := rep.Failures(); len(fails) != 2 {
		t.Fatalf("Failures() = %v, want both clients' remove failures surfaced", fails)
	}
	for _, c := range rep.Clients {
		if c.Action != SyncFailed || c.Err == nil {
			t.Errorf("%s action=%q err=%v, want SyncFailed carrying the error", c.Client, c.Action, c.Err)
		}
	}
	if len(rep.Removed()) != 0 {
		t.Errorf("Removed() = %v, want none (the removal failed)", rep.Removed())
	}
}

func TestSyncStoreHarnessAbsentDefaultEntry(t *testing.T) {
	b := &fakeMCPBackend{clients: twoClients(), removePresent: true}
	rep := SyncStoreHarness(context.Background(), b, "", EntryAbsent)
	if rep.Entry != "gramaton" {
		t.Errorf("entry = %q, want gramaton", rep.Entry)
	}
	// The default store's remove targets the empty store name.
	if !reflect.DeepEqual(b.removeCalls, []string{"Claude Code:", "Cursor:"}) {
		t.Errorf("RemoveStore calls = %v, want the default entry for both clients", b.removeCalls)
	}
}

func TestRenameStoreHarnessRegistersNewBeforeRemovingOld(t *testing.T) {
	// A recording backend that logs the interleaving of register and
	// remove so the ordering invariant (register NEW before remove OLD)
	// is asserted, not just the end state.
	b := &orderRecordingBackend{clients: twoClients(), removePresent: true}
	newRep, oldRep := RenameStoreHarness(context.Background(), b, "old", "new")

	if newRep.Entry != "gramaton-new" || oldRep.Entry != "gramaton-old" {
		t.Fatalf("entries = %q / %q, want gramaton-new / gramaton-old", newRep.Entry, oldRep.Entry)
	}
	// Every register of the new entry must precede every removal of the
	// old one.
	lastRegister, firstRemove := -1, len(b.log)
	for i, e := range b.log {
		switch {
		case e == "register:new":
			lastRegister = i
		case e == "remove:old" && i < firstRemove:
			firstRemove = i
		}
	}
	if lastRegister == -1 || firstRemove == len(b.log) {
		t.Fatalf("expected both register:new and remove:old events, log = %v", b.log)
	}
	if lastRegister > firstRemove {
		t.Errorf("register:new must precede remove:old, log = %v", b.log)
	}
}

func TestRenameStoreHarnessDefaultToNamedFlipsEntry(t *testing.T) {
	// default -> named: register gramaton-work, remove the default
	// gramaton entry.
	b := &fakeMCPBackend{clients: twoClients(), removePresent: true}
	newRep, oldRep := RenameStoreHarness(context.Background(), b, "", "work")
	if newRep.Entry != "gramaton-work" {
		t.Errorf("new entry = %q, want gramaton-work", newRep.Entry)
	}
	if oldRep.Entry != "gramaton" {
		t.Errorf("old entry = %q, want the default gramaton", oldRep.Entry)
	}
	// New named registers via RegisterStore; old default removes via the
	// empty store name.
	if !reflect.DeepEqual(b.storeCalls, []string{"Claude Code:work", "Cursor:work"}) {
		t.Errorf("RegisterStore calls = %v, want the new named entry", b.storeCalls)
	}
	if !reflect.DeepEqual(b.removeCalls, []string{"Claude Code:", "Cursor:"}) {
		t.Errorf("RemoveStore calls = %v, want the default entry removed", b.removeCalls)
	}
}

func TestSyncReportJSONBuckets(t *testing.T) {
	rep := &SyncReport{
		Entry: "gramaton-x",
		Clients: []ClientSyncResult{
			{Client: "Claude Code", Action: SyncRegistered},
			{Client: "Codex", Action: SyncAlreadyRegistered},
			{Client: "Cursor", Action: SyncFailed, Err: errors.New("nope")},
		},
	}
	j := rep.JSON()
	if got, _ := j["registered"].([]string); !reflect.DeepEqual(got, []string{"Claude Code"}) {
		t.Errorf("registered bucket = %v", j["registered"])
	}
	if got, _ := j["already_registered"].([]string); !reflect.DeepEqual(got, []string{"Codex"}) {
		t.Errorf("already_registered bucket = %v", j["already_registered"])
	}
	failed, ok := j["failed"].(map[string]string)
	if !ok || failed["Cursor"] != "nope" {
		t.Errorf("failed bucket = %v, want Cursor->nope", j["failed"])
	}
	// Empty buckets are omitted.
	if _, present := j["removed"]; present {
		t.Errorf("removed bucket should be omitted when empty, got %v", j["removed"])
	}
}

func TestHarnessRegistrationsSkipsEnumerationFailure(t *testing.T) {
	// A harness whose ListEntries errors is skipped, not fatal: the
	// survey must return (empty here) rather than abort/panic, so a
	// future regression turning the best-effort continue into a hard
	// failure is caught.
	b := &fakeMCPBackend{clients: twoClients(), listErr: errors.New("enumeration boom")}
	regs := HarnessRegistrations(context.Background(), b)
	if len(regs) != 0 {
		t.Errorf("regs = %v, want empty when every harness enumeration fails", regs)
	}
}

func TestHarnessRegistrationsBucketsByEntry(t *testing.T) {
	// The happy path: entries reported by each client are bucketed by
	// entry name to the harnesses that have them.
	b := &fakeMCPBackend{clients: twoClients(), entries: []string{"gramaton", "gramaton-team"}}
	regs := HarnessRegistrations(context.Background(), b)
	if got := regs["gramaton-team"]; !reflect.DeepEqual(got, []string{"Claude Code", "Cursor"}) {
		t.Errorf("regs[gramaton-team] = %v, want both clients", got)
	}
}

// --- removeStoreEntry (the store-scoped removal helper) ---

func TestRemoveStoreEntryPresent(t *testing.T) {
	var removedArg []string
	h := &Harness{
		Name:           "Fake",
		ListMCPEntries: func(_ context.Context, _ string) ([]string, error) { return []string{"gramaton", "gramaton-team"}, nil },
		RemoveMCPEntries: func(_ context.Context, _ string, entries []string) ([]string, string, error) {
			removedArg = entries
			return entries, "", nil
		},
	}
	removed, err := removeStoreEntry(context.Background(), h, "", "gramaton-team")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v, want removed with no error", removed, err)
	}
	if !reflect.DeepEqual(removedArg, []string{"gramaton-team"}) {
		t.Errorf("RemoveMCPEntries got %v, want exactly the one target entry", removedArg)
	}
}

func TestRemoveStoreEntryAbsentIsNoOp(t *testing.T) {
	called := false
	h := &Harness{
		Name:           "Fake",
		ListMCPEntries: func(_ context.Context, _ string) ([]string, error) { return []string{"gramaton"}, nil },
		RemoveMCPEntries: func(_ context.Context, _ string, _ []string) ([]string, string, error) {
			called = true
			return nil, "", nil
		},
	}
	removed, err := removeStoreEntry(context.Background(), h, "", "gramaton-team")
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v, want a clean no-op", removed, err)
	}
	if called {
		t.Error("RemoveMCPEntries must not be called when the entry is absent")
	}
}

func TestRemoveStoreEntryListError(t *testing.T) {
	h := &Harness{
		Name:             "Fake",
		ListMCPEntries:   func(_ context.Context, _ string) ([]string, error) { return nil, errors.New("list failed") },
		RemoveMCPEntries: func(_ context.Context, _ string, _ []string) ([]string, string, error) { return nil, "", nil },
	}
	if _, err := removeStoreEntry(context.Background(), h, "", "gramaton-team"); err == nil {
		t.Error("a list failure must surface as an error, not a silent no-op")
	}
}

// orderRecordingBackend records register/remove interleaving for the
// rename ordering assertion. It only distinguishes the "new"/"old"
// store names the rename test uses.
type orderRecordingBackend struct {
	clients       []DetectedClient
	removePresent bool
	log           []string
}

func (b *orderRecordingBackend) Detect() []DetectedClient { return b.clients }
func (b *orderRecordingBackend) Register(_ context.Context, _ DetectedClient) (bool, error) {
	b.log = append(b.log, "register:default")
	return false, nil
}
func (b *orderRecordingBackend) RegisterStore(_ context.Context, _ DetectedClient, storeName string) (bool, error) {
	b.log = append(b.log, "register:"+storeName)
	return false, nil
}
func (b *orderRecordingBackend) RemoveStore(_ context.Context, _ DetectedClient, storeName string) (bool, error) {
	b.log = append(b.log, "remove:"+storeName)
	return b.removePresent, nil
}
func (b *orderRecordingBackend) ListEntries(_ context.Context, _ DetectedClient) ([]string, error) {
	return nil, nil
}
