package setup

import (
	"context"
	"fmt"
)

// Keeping a store's harness registration in sync with its lifecycle.
//
// # The problem this solves
//
// `gramaton init` registers a harness MCP entry and `gramaton
// uninstall` removes every entry machine-wide, but nothing in between
// keeps the two in step with the store lifecycle: creating, attaching,
// renaming, or deleting a store never touched the harness config, so a
// freshly created store was invisible to agents until hand-wired, a
// renamed store's entry pointed at a name that no longer resolved, and
// a deleted store left an orphan entry behind. SyncStoreHarness is the
// one primitive every store-lifecycle op calls so the wiring always
// follows the store.
//
// # Scope: the MCP entry, nothing else
//
// SyncStoreHarness reconciles exactly ONE surface -- the store's MCP
// server entry (`gramaton` for the default store, `gramaton-<name>`
// for a named one). It deliberately does NOT touch:
//
//   - Hooks. Session-capture proxies run `gramaton hook <event>` with
//     no --store and target the runtime-resolved default store, so
//     they are per-machine, owned by init/uninstall -- not per-store.
//   - Agent guidance. The guidance block is a single shared per-harness
//     region ("last install wins"); a store-lifecycle op that rewrote
//     it would clobber an existing writable store's guidance. Read-only
//     enforcement does not depend on it anyway: the frozen STORE
//     manifest makes the MCP process register only read tools at
//     startup regardless of guidance (cli/mcp_cmd.go resolveMCPReadOnly).
//
// The wizard's read-only-ONLY attach route still installs guidance
// itself, because there the shared store is the machine's only gramaton.

// EntryState is the desired state of a store's MCP entry across the
// detected harnesses.
type EntryState int

const (
	// EntryPresent registers the store's MCP entry (idempotent:
	// re-registering an existing entry reports "already registered").
	EntryPresent EntryState = iota
	// EntryAbsent removes the store's MCP entry (idempotent: removing
	// an absent entry reports "not present", not an error).
	EntryAbsent
)

// SyncAction classifies what happened for one harness during a sync.
type SyncAction string

const (
	SyncRegistered        SyncAction = "registered"
	SyncAlreadyRegistered SyncAction = "already registered"
	SyncRemoved           SyncAction = "removed"
	SyncNotPresent        SyncAction = "not present"
	SyncFailed            SyncAction = "failed"
)

// ClientSyncResult is one harness's outcome from a sync.
type ClientSyncResult struct {
	// Client is the harness display name ("Claude Code").
	Client string
	// Action is what happened for this harness.
	Action SyncAction
	// Err carries the failure when Action is SyncFailed; nil otherwise.
	// Per-harness failures never abort the whole sync (warn-and-continue,
	// matching the wizard's install posture), so the caller inspects
	// this to report or escalate.
	Err error
}

// SyncReport is the outcome of reconciling one store's MCP entry across
// every detected harness.
type SyncReport struct {
	// Entry is the MCP entry name reconciled ("gramaton" or
	// "gramaton-<store>").
	Entry string
	// Want is the desired state that produced this report.
	Want EntryState
	// Clients holds one result per detected harness, in detection order.
	// Empty when no harnesses were detected.
	Clients []ClientSyncResult
}

// storeEntryName resolves the MCP entry name for a store: the default
// "gramaton" entry for the unnamed store (empty name), else
// "gramaton-<name>".
func storeEntryName(storeName string) string {
	if storeName == "" {
		return "gramaton"
	}
	return storeMCPEntryName(storeName)
}

// StoreEntryName is storeEntryName exported for callers (store list /
// sync-harness) that need a store's expected MCP entry name without
// duplicating the naming convention.
func StoreEntryName(storeName string) string {
	return storeEntryName(storeName)
}

// HarnessRegistrations surveys every detected harness and returns a map
// from gramaton-owned MCP entry name to the display names of the
// harnesses that currently have it registered (enumerated by naming
// convention). Best-effort: a harness whose enumeration fails is
// skipped rather than failing the whole survey. Used by `store list` to
// show which stores are wired and by `store sync-harness` to find
// orphaned entries.
func HarnessRegistrations(ctx context.Context, backend MCPBackend) map[string][]string {
	out := map[string][]string{}
	for _, c := range backend.Detect() {
		entries, err := backend.ListEntries(ctx, c)
		if err != nil {
			continue
		}
		for _, e := range entries {
			out[e] = append(out[e], c.Name)
		}
	}
	return out
}

// SyncStoreHarness reconciles the MCP entry for storeName (empty =
// default store) across all detected harnesses toward want. It is
// non-interactive: per-harness failures are captured in the returned
// report rather than aborting, so one broken or best-effort harness
// never strands the rest. Detection, registration, and removal all go
// through backend, so tests inject a fake and production passes
// DefaultMCPBackend{}.
func SyncStoreHarness(ctx context.Context, backend MCPBackend, storeName string, want EntryState) *SyncReport {
	return syncClients(ctx, backend, backend.Detect(), storeName, want)
}

// syncClients is SyncStoreHarness over a pre-detected client slice, so
// the wizard (which detects once to display the checkbox list before
// prompting) and the CLI share one registration loop without detecting
// twice.
func syncClients(ctx context.Context, backend MCPBackend, clients []DetectedClient, storeName string, want EntryState) *SyncReport {
	rep := &SyncReport{Entry: storeEntryName(storeName), Want: want}
	for _, c := range clients {
		res := ClientSyncResult{Client: c.Name}
		switch want {
		case EntryPresent:
			var already bool
			var err error
			if storeName == "" {
				already, err = backend.Register(ctx, c)
			} else {
				already, err = backend.RegisterStore(ctx, c, storeName)
			}
			switch {
			case err != nil:
				res.Action, res.Err = SyncFailed, err
			case already:
				res.Action = SyncAlreadyRegistered
			default:
				res.Action = SyncRegistered
			}
		case EntryAbsent:
			removed, err := backend.RemoveStore(ctx, c, storeName)
			switch {
			case err != nil:
				res.Action, res.Err = SyncFailed, err
			case removed:
				res.Action = SyncRemoved
			default:
				res.Action = SyncNotPresent
			}
		}
		rep.Clients = append(rep.Clients, res)
	}
	return rep
}

// RenameStoreHarness moves harness registration for a store rename:
// registers the NEW entry FIRST, then removes the OLD one. Ordering is
// load-bearing -- a mid-run failure after the register but before the
// remove leaves the agent reachable through the new entry rather than
// orphaned. oldName/newName use the empty string for the default store,
// so a default<->named rename correctly flips "gramaton" <->
// "gramaton-<name>". Returns both sub-reports (new first) for the
// caller to render and to detect failures.
func RenameStoreHarness(ctx context.Context, backend MCPBackend, oldName, newName string) (newRep, oldRep *SyncReport) {
	clients := backend.Detect()
	newRep = syncClients(ctx, backend, clients, newName, EntryPresent)
	oldRep = syncClients(ctx, backend, clients, oldName, EntryAbsent)
	return newRep, oldRep
}

// Registered returns the harnesses whose entry is present after the
// sync (newly registered or already registered).
func (r *SyncReport) Registered() []string {
	var out []string
	for _, c := range r.Clients {
		if c.Action == SyncRegistered || c.Action == SyncAlreadyRegistered {
			out = append(out, c.Client)
		}
	}
	return out
}

// Removed returns the harnesses whose entry was removed by the sync.
func (r *SyncReport) Removed() []string {
	var out []string
	for _, c := range r.Clients {
		if c.Action == SyncRemoved {
			out = append(out, c.Client)
		}
	}
	return out
}

// Failures returns the per-harness results that failed. Empty when the
// sync was clean.
func (r *SyncReport) Failures() []ClientSyncResult {
	var out []ClientSyncResult
	for _, c := range r.Clients {
		if c.Action == SyncFailed {
			out = append(out, c)
		}
	}
	return out
}

// JSON buckets the report by action for a command's structured output.
// Only non-empty buckets are included; a report with no detected
// harnesses yields an empty map.
func (r *SyncReport) JSON() map[string]any {
	out := map[string]any{}
	bucket := func(key string, names []string) {
		if len(names) > 0 {
			out[key] = names
		}
	}
	var registered, already, removed, notPresent []string
	failed := map[string]string{}
	for _, c := range r.Clients {
		switch c.Action {
		case SyncRegistered:
			registered = append(registered, c.Client)
		case SyncAlreadyRegistered:
			already = append(already, c.Client)
		case SyncRemoved:
			removed = append(removed, c.Client)
		case SyncNotPresent:
			notPresent = append(notPresent, c.Client)
		case SyncFailed:
			failed[c.Client] = fmt.Sprintf("%v", c.Err)
		}
	}
	bucket("registered", registered)
	bucket("already_registered", already)
	bucket("removed", removed)
	bucket("not_present", notPresent)
	if len(failed) > 0 {
		out["failed"] = failed
	}
	return out
}
