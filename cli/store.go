package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/gramaton-ai/gramaton/store"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	storeDeleteForce          bool
	storeAttachName           string
	storeAttachFreezeOriginal bool
	storeNoHarness            bool
	storeListHarness          bool
	storeSyncPrune            bool
)

// storeHarnessBackend is the MCP backend that store-lifecycle commands
// use to keep each store's harness registration in sync with its
// lifecycle (create/attach/rename/delete). A package var so tests
// inject a no-op fake: the production DefaultMCPBackend shells out to
// the real vendor CLIs (claude/codex) and edits ~/.cursor/mcp.json, so
// an un-injected test run would mutate the developer's actual harness
// config. cli's TestMain points it at a fake.
var storeHarnessBackend setup.MCPBackend = setup.DefaultMCPBackend{}

// syncStoreEntry reconciles a store's MCP entry across detected
// harnesses toward want (present on create/attach, absent on delete),
// folds the structured result into out under "harness", and prints a
// human summary to stderr. storeName is "" for the default store.
// Never returns an error: the on-disk store op already committed, so a
// harness hiccup is a warning, not a command failure.
func syncStoreEntry(ctx context.Context, out map[string]any, storeName string, want setup.EntryState) {
	rep := setup.SyncStoreHarness(ctx, storeHarnessBackend, storeName, want)
	out["harness"] = rep.JSON()
	printHarnessSummary(os.Stderr, rep)
}

// printHarnessSummary narrates a harness sync to w (stderr), so the
// machine-parseable JSON stays on stdout. Mirrors printCarveSummary's
// stderr-summary convention.
func printHarnessSummary(w io.Writer, rep *setup.SyncReport) {
	if len(rep.Clients) == 0 {
		fmt.Fprintln(w, "No supported AI tools detected; the store works via the CLI. "+
			"After installing one, run: gramaton store sync-harness")
		return
	}
	if reg := rep.Registered(); len(reg) > 0 {
		fmt.Fprintf(w, "MCP entry %s registered with %s. Restart your AI client(s) to pick it up.\n",
			rep.Entry, strings.Join(reg, ", "))
	}
	if rm := rep.Removed(); len(rm) > 0 {
		fmt.Fprintf(w, "MCP entry %s removed from %s.\n", rep.Entry, strings.Join(rm, ", "))
	}
	for _, f := range rep.Failures() {
		fmt.Fprintf(w, "warning: could not update %s: %v\n", f.Client, f.Err)
	}
}

var storeCmd = &cobra.Command{
	Use:   "store",
	Short: "Manage named stores",
	Long: `Named stores provide isolated data directories. Each store has its own data
directory, server process, and optional config override. Use --store
or GRAMATON_STORE to select a store for other commands.`,
}

var storeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stores",
	Long: `Prints every registered store as a JSON array, one object per store. Fields
at their zero value are omitted.

Per-store fields: name; path (the store's data directory); active (the store
targeted by --store / GRAMATON_STORE, the default store when unset); running
(whether a server is currently serving it); read_only (resolved live from the
store's STORE manifest on every list, so the badge matches what the engine
enforces); and manifest, set to "(manifest unreadable)" when the manifest --
or the config needed to locate it -- cannot be read. That note is shown
instead of guessing writable, since a corrupt manifest can hide a frozen
store.

A remote-client store, whose data lives on another machine, instead reports
remote and remote_url and carries no local path: its running state and
read-only badge are resolved from the remote at MCP time and do not apply
here.

With --harness, each detected AI tool is surveyed (shelling out to the vendor
CLIs) and every store gains a harness list naming the tools whose MCP entry
points at it, or a harness_note when the store is registered with none.

Examples:
  gramaton store list
  gramaton store list --harness`,
	RunE: runStoreList,
}

var storeSyncHarnessCmd = &cobra.Command{
	Use:   "sync-harness",
	Short: "Reconcile every store's MCP entry with your AI tools",
	Long: `Re-registers each store's MCP entry (gramaton for the default store,
gramaton-<name> for a named one) with every detected AI tool. Idempotent:
run it after installing a new AI tool, or to repair wiring that drifted.

With --prune, MCP entries for named stores that no longer exist are also
removed (the default gramaton entry is never pruned).

Only MCP entries are touched -- session-capture hooks and agent guidance
are owned by 'gramaton init' / 'gramaton uninstall'.`,
	RunE: runStoreSyncHarness,
}

var storeCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new named store",
	Args:  cobra.ExactArgs(1),
	RunE:  runStoreCreate,
}

var storeAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add more records from the active store into an existing store",
	Args:  cobra.ExactArgs(1),
	RunE:  runStoreAdd,
}

var storeDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a named store and all its data",
	Long: `Removes the named store and everything under it -- records, graph,
config, and STORE manifest are deleted from disk. This cannot be
undone; there is no recycle step and no backup unless you made one
yourself.

Refuses while the store's server is running, since a live engine
still holds the data open. Stop it first: gramaton --store <name>
serve --stop. Without --force you must retype the store name at the
prompt to confirm; --force deletes without asking.

The store's MCP entry (gramaton-<name>) is deregistered from every
detected AI tool, so no harness is left pointing at a store that no
longer exists. This is idempotent: a store never registered, or
created with --no-harness, is a clean no-op.

Examples:
  gramaton store delete authwork
  gramaton store delete demo --force`,
	Args: cobra.ExactArgs(1),
	RunE: runStoreDelete,
}

var storeRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a store (use 'default' for the unnamed store)",
	Long: `Renames a store: moves its data directory to the new location and
re-pins the per-store config's data_dir there, so the global
config's data_dir never bleeds through to the renamed store. Use
'default' for the unnamed store as either argument -- renaming it to
a name moves data/ out of the base dir into stores/<new>/, and
renaming a name to 'default' moves data/ back to the base dir.

Refuses while the source store's server is running (a live engine
holds the data directory open); stop it first: gramaton stop. A
remote-client store is moved without a data_dir pin -- its config
carries remote.url, not a local path -- and renaming a remote store
TO default is refused, since it has no local data to move.

The store's MCP entry is re-pointed across every detected AI tool:
the new entry (gramaton-<new>, or the bare "gramaton" for the
default store) is registered first, then the old entry removed, so a
mid-run failure still leaves the agent reachable through the new
entry.

Examples:
  gramaton store rename authwork auth
  gramaton store rename default archive
  gramaton store rename scratch default`,
	Args: cobra.ExactArgs(2),
	RunE: runStoreRename,
}

var storeFreezeCmd = &cobra.Command{
	Use:   "freeze [name]",
	Short: "Mark a store read-only (a frozen, shareable artifact)",
	Long: `Marks a store read-only via its STORE manifest: every logical write is
rejected while reads and search keep working, and background writers
(curation, access tracking) stay off. The manifest records the owner
(the configured author) and the publication time, and travels with
any copy, tar, or backup of the data directory.

With no name, freezes the active store (--store / GRAMATON_STORE);
use 'default' for the unnamed store. Refuses while the store's
server is running -- a live server's engine reads the read-only flag
at startup and would keep accepting writes. Stop it first:
gramaton stop. A server that starts in the instant between that
check and the manifest write has still read the old state; freeze
warns when it detects one, and that server must be restarted
(gramaton stop) for the change to take effect.

Reverse with: gramaton store thaw [name]`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStoreFreeze,
}

var storeAttachCmd = &cobra.Command{
	Use:   "attach <path>",
	Short: "Attach a shared read-only store as a named store",
	Long: `Registers a store directory someone shared with you as a local named
store, alongside any stores you already have. The directory may be
either a store dir (containing data/) or the data dir itself; its
format version must match this binary.

The data is COPIED under stores/<name>/ -- the directory you received
is never modified -- and the local copy's STORE manifest is frozen,
so every gramaton surface treats the attached store as read-only:
all writes are rejected, no curation runs, and MCP processes serving
it register only the read tools. Reads and search work in full.

A minimal per-store config.yaml is written (data_dir only); all
other settings inherit from your global config. Your own stores and
their configuration are untouched.

With --freeze-original, a source directory that is still writable on
disk is ALSO frozen (owner: your configured author), so anything
else that opens the original sees it as read-only too. The default
leaves the directory you received exactly as it arrived; a source
that is already frozen is never re-stamped either way.

Reach the attached store with --store or GRAMATON_STORE:

  gramaton --store <name> search "<query>" --top 5

The store's MCP entry (gramaton-<name>, running 'gramaton --store
<name> mcp') is registered with every detected AI tool so an agent can
search it right away; only read tools surface against the frozen copy.
Pass --no-harness to skip that and wire the entry yourself.`,
	Args: cobra.ExactArgs(1),
	RunE: runStoreAttach,
}

var storeThawCmd = &cobra.Command{
	Use:   "thaw [name]",
	Short: "Clear a store's read-only flag, making it writable again",
	Long: `Clears the read-only flag set by 'gramaton store freeze'. The manifest
keeps the original owner and published_at as provenance of the
publication; only a later freeze overwrites them.

With no name, thaws the active store (--store / GRAMATON_STORE);
use 'default' for the unnamed store. Refuses while the store's
server is running, for the same reason freeze does.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStoreThaw,
}

func init() {
	addStoreCreateFlags(storeCreateCmd)
	// The carve half of `store create` is exactly api.CarveOut; reference
	// its shared description constant here so the CLI help and the api/MCP
	// surface never drift (no duplicated literal -- api/carveout.go owns
	// the string).
	storeCreateCmd.Long = `Create a new named store.

With no seed flags this creates an empty store (add --read-only to freeze
it immediately into a shareable read-only artifact).

With one or more seed flags (--from-id, --from-collection, --query, or a
query filter such as --keywords/--temporality) it instead carves a subset
of the CURRENTLY ACTIVE store into the new one, server-mediated:

  ` + api.CarveOutDescription + `

Examples:
  gramaton store create shared --from-id 01ABC...,01DEF...
  gramaton store create authwork --keywords auth --temporality durable
  gramaton store create demo --from-collection tasks --read-only
  gramaton store create preview --keywords auth --dry-run`
	addStoreAddFlags(storeAddCmd)
	// `store add` is exactly api.CarveAdd; reference its shared description
	// constant so the CLI help and the api/server surface never drift (no
	// duplicated literal -- api/carveadd.go owns the string).
	storeAddCmd.Long = `Add more records from the CURRENTLY ACTIVE store into an EXISTING store,
server-mediated (it reads the live source under a read lock).

  ` + api.CarveAddDescription + `

The target must already exist (create it with 'gramaton store create'). The
same seed flags as 'store create --from' select what to bring over. There is
deliberately no --read-only flag: a frozen target keeps its frozen state (it
is thawed for the add and re-frozen to its exact prior state).

Examples:
  gramaton store add shared --from-id 01ABC...,01DEF...
  gramaton store add shared --from-collection tasks
  gramaton store add shared --keywords auth --dry-run`
	storeDeleteCmd.Flags().BoolVar(&storeDeleteForce, "force", false, "skip confirmation prompt")
	storeCreateCmd.Flags().BoolVar(&storeNoHarness, "no-harness", false,
		"skip registering the store's MCP entry with detected AI tools")
	storeAttachCmd.Flags().StringVar(&storeAttachName, "name", "",
		"local name for the attached store (default: derived from the directory name)")
	storeAttachCmd.Flags().BoolVar(&storeAttachFreezeOriginal, "freeze-original", false,
		"also freeze the source directory when it is writable on disk (owner: the configured author)")
	storeAttachCmd.Flags().BoolVar(&storeNoHarness, "no-harness", false,
		"skip registering the store's MCP entry with detected AI tools")
	storeListCmd.Flags().BoolVar(&storeListHarness, "harness", false,
		"also report which AI tools each store's MCP entry is registered with")
	storeSyncHarnessCmd.Flags().BoolVar(&storeSyncPrune, "prune", false,
		"also remove MCP entries for named stores that no longer exist")
	storeCmd.AddCommand(storeListCmd, storeCreateCmd, storeAddCmd, storeDeleteCmd, storeRenameCmd, storeFreezeCmd, storeThawCmd, storeAttachCmd, storeSyncHarnessCmd)
	rootCmd.AddCommand(storeCmd)
}

func runStoreList(cmd *cobra.Command, args []string) error {
	base := baseConfigDir()
	stores := store.List(base)

	if len(stores) == 0 {
		fmt.Println("No stores found. Run 'gramaton init' to create the default store.")
		return nil
	}

	active := activeStoreName()

	type storeEntry struct {
		Name    string `json:"name"`
		Path    string `json:"path,omitempty"`
		Active  bool   `json:"active,omitempty"`
		Running bool   `json:"running,omitempty"`
		// ReadOnly is derived live from the store's STORE manifest on
		// every list -- never from stored text -- so the badge cannot
		// drift from the state the engine will actually enforce.
		ReadOnly bool `json:"read_only,omitempty"`
		// Manifest is "(manifest unreadable)" when the STORE manifest
		// (or the config needed to locate it) cannot be read. Shown
		// instead of guessing writable: a corrupted manifest may hide
		// a frozen store.
		Manifest string `json:"manifest,omitempty"`
		// Harness, populated only with --harness, names the AI tools
		// whose MCP entry points at this store; empty means the store
		// is not registered with any detected tool.
		Harness []string `json:"harness,omitempty"`
		// HarnessNote is set with --harness when a store has no MCP
		// entry registered anywhere, so the gap is legible rather than
		// an absent field.
		HarnessNote string `json:"harness_note,omitempty"`
		// Remote/RemoteURL mark a remote-client store: its data lives on
		// another machine, so Path, Running, and the read-only badge
		// (resolved live from the remote at MCP time) do not apply.
		Remote    bool   `json:"remote,omitempty"`
		RemoteURL string `json:"remote_url,omitempty"`
	}

	// With --harness, survey each detected tool's registered entries once
	// (shells out to the vendor CLIs), then cross-check per store.
	var regs map[string][]string
	if storeListHarness {
		regs = setup.HarnessRegistrations(cmd.Context(), storeHarnessBackend)
	}

	var entries []storeEntry
	for _, s := range stores {
		isActive := (s.Default && active == "") || (!s.Default && s.Name == active)
		e := storeEntry{Name: s.Name, Active: isActive}
		if s.Remote {
			// A remote store has no local data/server/manifest; its
			// read-only state is resolved from the remote at MCP time.
			e.Remote = true
			e.RemoteURL = s.RemoteURL
		} else {
			dir := store.Resolve(base, nameForResolve(s))
			e.Path = s.Path
			e.Running = isServerRunning(dir)
			e.ReadOnly, e.Manifest = storeReadOnlyBadge(dir)
		}
		if storeListHarness {
			if h := regs[setup.StoreEntryName(nameForResolve(s))]; len(h) > 0 {
				e.Harness = h
			} else {
				e.HarnessNote = "not registered with any AI tool (run: gramaton store sync-harness)"
			}
		}
		entries = append(entries, e)
	}

	return printJSON(entries)
}

// runStoreSyncHarness reconciles every store's MCP entry with the
// detected AI tools: re-register each existing store's entry
// (idempotent repair), and with --prune remove entries for named stores
// that no longer exist. Only MCP entries are touched; hooks and guidance
// stay owned by init/uninstall.
func runStoreSyncHarness(cmd *cobra.Command, args []string) error {
	base := baseConfigDir()
	stores := store.List(base)

	out := map[string]any{}
	synced := make([]map[string]any, 0, len(stores))
	existing := map[string]bool{}
	for _, s := range stores {
		name := nameForResolve(s)
		existing[setup.StoreEntryName(name)] = true
		rep := setup.SyncStoreHarness(cmd.Context(), storeHarnessBackend, name, setup.EntryPresent)
		synced = append(synced, map[string]any{"store": s.Name, "harness": rep.JSON()})
	}
	out["synced"] = synced

	if storeSyncPrune {
		regs := setup.HarnessRegistrations(cmd.Context(), storeHarnessBackend)
		orphans := make([]string, 0)
		for entry := range regs {
			// Never prune the default "gramaton" entry, and keep entries
			// backing a live store. A degenerate "gramaton-" entry strips
			// to an empty store name, which resolves back to the default
			// entry -- guard it too so a hand-added "gramaton-" can never
			// take the default entry down (gramaton never creates one, as
			// ValidateName rejects empty names).
			if entry == "gramaton" || strings.TrimPrefix(entry, "gramaton-") == "" || existing[entry] {
				continue
			}
			orphans = append(orphans, entry)
		}
		sort.Strings(orphans)
		pruned := make([]map[string]any, 0, len(orphans))
		for _, entry := range orphans {
			storeName := strings.TrimPrefix(entry, "gramaton-")
			rep := setup.SyncStoreHarness(cmd.Context(), storeHarnessBackend, storeName, setup.EntryAbsent)
			pruned = append(pruned, map[string]any{"entry": entry, "harness": rep.JSON()})
		}
		out["pruned"] = pruned
	}

	return printJSON(out)
}

// addStoreCreateFlags registers every flag `store create` accepts. Kept
// as a standalone function (not inline in init) so tests can build an
// isolated command carrying the identical flag set. --read-only is
// create-specific (it also governs the offline empty-store path); the
// shared carve seed/query/option flags come from addCarveSeedFlags.
func addStoreCreateFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("read-only", false,
		"freeze the store immediately after creation (owner: the configured author)")
	addCarveSeedFlags(cmd)
}

// addStoreAddFlags registers every flag `store add` accepts: the shared
// carve seeds/query/options, but NOT --read-only -- a top-up preserves
// the target's existing frozen state (thawed for the add and re-frozen)
// rather than choosing a new one.
func addStoreAddFlags(cmd *cobra.Command) {
	addCarveSeedFlags(cmd)
}

// addCarveSeedFlags registers the seed + query-filter + option flags
// shared by `store create --from` and `store add`. The query-seed filters
// (keywords/temporality/knowledge-type/epistemic-status/resolution/match/
// since, ...) are REUSED from addSearchFlags rather than reinvented; the
// carve-specific seeds/options are added on top.
func addCarveSeedFlags(cmd *cobra.Command) {
	addSearchFlags(cmd)
	// Carve seeds (union with the query above).
	cmd.Flags().StringSlice("from-id", nil,
		"carve seed: record IDs to include verbatim (repeatable or comma-separated)")
	cmd.Flags().StringSlice("from-collection", nil,
		"carve seed: collection names or ids (repeatable or comma-separated)")
	cmd.Flags().String("query", "",
		"carve query seed: vector-similarity query text")
	cmd.Flags().StringToString("meta", nil,
		"carve query seed: meta key=value filter (repeatable)")
	// Carve options.
	cmd.Flags().Bool("heads-only", false,
		"carve: skip the supersedes closure (omit superseded predecessors)")
	cmd.Flags().Bool("dry-run", false,
		"carve: resolve and report the selection without writing anything")
}

// carveSeedFlags are the flags whose presence flips `store create` from
// the offline empty-store path to the server-mediated carve. --read-only,
// --heads-only, and --dry-run are options, NOT seeds: they never trigger a
// carve on their own (in particular `store create <name> --read-only` keeps
// meaning "make an empty frozen store").
var carveSeedFlags = []string{
	"from-id", "from-collection", "query", "keywords", "temporality",
	"knowledge-type", "epistemic-status", "resolution", "match", "meta", "since",
}

// carveRequested reports whether any seed flag was set on the command.
func carveRequested(cmd *cobra.Command) bool {
	for _, name := range carveSeedFlags {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// buildCarveRequest maps the command's flags to an api.CarveOutRequest.
// The destination is computed as an ABSOLUTE path under the base config
// dir (stores/<name>/data); the api's carveMaterialize creates the home,
// config, and data dir there, so the CLI must NOT pre-create them.
func buildCarveRequest(cmd *cobra.Command, name string) api.CarveOutRequest {
	req := api.CarveOutRequest{
		DestName:    name,
		DestDataDir: filepath.Join(store.Resolve(baseConfigDir(), name), "data"),
	}
	req.IDs, _ = cmd.Flags().GetStringSlice("from-id")
	req.Collections, _ = cmd.Flags().GetStringSlice("from-collection")
	req.Text, _ = cmd.Flags().GetString("query")
	req.Match, _ = cmd.Flags().GetString("match")
	req.Temporality, _ = cmd.Flags().GetString("temporality")
	req.KnowledgeType, _ = cmd.Flags().GetString("knowledge-type")
	req.EpistemicStatus, _ = cmd.Flags().GetString("epistemic-status")
	req.Resolution, _ = cmd.Flags().GetString("resolution")
	req.Since, _ = cmd.Flags().GetString("since")
	if kw, _ := cmd.Flags().GetString("keywords"); kw != "" {
		req.Keywords = strings.Split(kw, ",")
	}
	if meta, _ := cmd.Flags().GetStringToString("meta"); len(meta) > 0 {
		req.Meta = meta
	}
	req.ReadOnly, _ = cmd.Flags().GetBool("read-only")
	req.HeadsOnly, _ = cmd.Flags().GetBool("heads-only")
	req.DryRun, _ = cmd.Flags().GetBool("dry-run")
	return req
}

func runStoreCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	// A seed flag routes to the server-mediated carve; nothing else does.
	if carveRequested(cmd) {
		return runStoreCarve(cmd, name)
	}
	// --dry-run / --heads-only are carve-only options; without a seed there
	// is nothing to carve, so fail loudly rather than silently ignore them
	// on the offline path.
	if cmd.Flags().Changed("dry-run") || cmd.Flags().Changed("heads-only") {
		return fmt.Errorf("--dry-run and --heads-only apply only to a carve; " +
			"add a seed (--from-id, --from-collection, --query, or a query filter such as --keywords)")
	}

	base := baseConfigDir()
	readOnly, _ := cmd.Flags().GetBool("read-only")

	if err := store.Create(base, name); err != nil {
		return err
	}

	dir := store.Resolve(base, name)

	// Pin the store's own data_dir in a per-store config.yaml
	// immediately (same mechanics as store.Attach). Without it the
	// engine's global-then-store config merge lets a global data_dir
	// bleed through for a config-less named store, so `gramaton
	// --store <name> ...` would open the DEFAULT store's data dir --
	// and with --read-only, the freeze badge and the engine's actual
	// enforcement would diverge onto two different stores.
	cfgPath, err := store.WriteDataDirConfig(dir, name)
	if err != nil {
		return err
	}

	out := map[string]any{
		"created": name,
		"path":    dir,
		"config":  cfgPath,
	}

	if readOnly {
		// storeEffectiveConfig resolves the data dir the per-store
		// config just pinned (<dir>/data) -- the same dir the engine
		// will open -- while the owner comes from the creating
		// config's author (inherited from the global config).
		cfg, dataDir, err := storeEffectiveConfig(dir)
		if err != nil {
			return err
		}
		if err := core.FreezeStore(dataDir, cfg.Author.String()); err != nil {
			return err
		}
		m, err := core.ReadStoreManifest(dataDir)
		if err != nil {
			return err
		}
		out["read_only"] = true
		out["owner"] = m.Owner
		out["published_at"] = m.PublishedAt
		out["note"] = storeFrozenNote
	}

	// Register the new store's MCP entry with every detected harness so
	// it is reachable by agents immediately, not after a manual step.
	// The frozen-manifest read-only surface (for --read-only) is applied
	// at MCP-process startup, so the entry is identical either way.
	if !storeNoHarness {
		syncStoreEntry(cmd.Context(), out, name, setup.EntryPresent)
	}

	return printJSON(out)
}

// runStoreCarve is the server-mediated carve path of `store create`. The
// op MUST run in the server process so it reads the LIVE source under the
// engine RLock (the CLI never opens its own engine against the source);
// serverURL auto-starts the source store's server the same way export and
// backup do. The destination is a brand-new store; the api creates it on
// disk (home + config + data) at the absolute path we compute here.
func runStoreCarve(cmd *cobra.Command, name string) error {
	base := baseConfigDir()

	// Friendly pre-checks; the api's ValidateName + ErrConflict remain the
	// source of truth (the server re-checks under its own lock).
	if err := store.ValidateName(name); err != nil {
		return err
	}
	if store.Exists(base, name) {
		return fmt.Errorf("store %q already exists", name)
	}

	req := buildCarveRequest(cmd, name)

	resp, err := serverPostSlow("/v1/store/carve", req)
	if err != nil {
		return writeServerError("store create --from", err)
	}

	// Human-readable summary to stderr; the machine-parseable JSON
	// envelope stays on stdout (printEnvelope convention).
	if raw, mErr := json.Marshal(resp.Data); mErr == nil {
		var cr api.CarveOutResponse
		if json.Unmarshal(raw, &cr) == nil {
			printCarveSummary(os.Stderr, name, req.DestDataDir, cr)
			// A committed carve materialized a new named store; register
			// its MCP entry (a dry run wrote nothing, so there is nothing
			// to wire).
			if !cr.DryRun && !storeNoHarness {
				rep := setup.SyncStoreHarness(cmd.Context(), storeHarnessBackend, name, setup.EntryPresent)
				printHarnessSummary(os.Stderr, rep)
			}
		}
	}
	return printEnvelope(resp)
}

// printCarveSummary writes a short human summary of a carve to w. On a
// dry run it is an explicit PREVIEW that names the counts and states that
// nothing was written; on a commit it names the created store, its path,
// the counts, and whether it was frozen. The dropped boundary edges are
// summarized by type with the bounded sample.
func printCarveSummary(w io.Writer, name, destDir string, cr api.CarveOutResponse) {
	if cr.DryRun {
		fmt.Fprintf(w, "PREVIEW (dry run -- nothing written): store %q would carve "+
			"%d seed(s) -> %d node(s), %d interior edge(s).\n",
			name, cr.SeedCount, cr.NodeCount, cr.InteriorEdges)
	} else {
		state := "writable"
		if cr.ReadOnly {
			state = "frozen read-only"
		}
		fmt.Fprintf(w, "Created store %q at %s (%s): %d seed(s) -> %d node(s), %d interior edge(s).\n",
			name, destDir, state, cr.SeedCount, cr.NodeCount, cr.InteriorEdges)
	}
	if cr.DroppedTotal > 0 {
		fmt.Fprintf(w, "Dropped %d boundary-crossing edge(s) by type: %v\n", cr.DroppedTotal, cr.DroppedByType)
		for _, d := range cr.DanglingSample {
			fmt.Fprintf(w, "  %s -[%s]-> %s\n", d.SourceID, d.Type, d.TargetID)
		}
		if cr.DroppedTotal > len(cr.DanglingSample) {
			fmt.Fprintf(w, "  ... and %d more (sample capped)\n", cr.DroppedTotal-len(cr.DanglingSample))
		}
	}
}

// buildCarveAddRequest maps the command's flags to an api.CarveAddRequest.
// The destination is computed as the ABSOLUTE path of the EXISTING store's
// data dir (stores/<name>/data). There is no --read-only flag: the target's
// existing frozen state is preserved by the api.
func buildCarveAddRequest(cmd *cobra.Command, name string) api.CarveAddRequest {
	req := api.CarveAddRequest{
		DestName:    name,
		DestDataDir: filepath.Join(store.Resolve(baseConfigDir(), name), "data"),
	}
	req.IDs, _ = cmd.Flags().GetStringSlice("from-id")
	req.Collections, _ = cmd.Flags().GetStringSlice("from-collection")
	req.Text, _ = cmd.Flags().GetString("query")
	req.Match, _ = cmd.Flags().GetString("match")
	req.Temporality, _ = cmd.Flags().GetString("temporality")
	req.KnowledgeType, _ = cmd.Flags().GetString("knowledge-type")
	req.EpistemicStatus, _ = cmd.Flags().GetString("epistemic-status")
	req.Resolution, _ = cmd.Flags().GetString("resolution")
	req.Since, _ = cmd.Flags().GetString("since")
	if kw, _ := cmd.Flags().GetString("keywords"); kw != "" {
		req.Keywords = strings.Split(kw, ",")
	}
	if meta, _ := cmd.Flags().GetStringToString("meta"); len(meta) > 0 {
		req.Meta = meta
	}
	req.HeadsOnly, _ = cmd.Flags().GetBool("heads-only")
	req.DryRun, _ = cmd.Flags().GetBool("dry-run")
	return req
}

// runStoreAdd tops up an EXISTING store with more records from the active
// store, server-mediated (same as the carve half of `store create`): the
// op runs in the source store's server process so it reads the live source
// under the engine RLock. The target must already exist; the api opens it,
// skips records already present, reconnects+dedupes edges, and preserves
// the target's frozen state.
func runStoreAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	base := baseConfigDir()

	if err := store.ValidateName(name); err != nil {
		return err
	}
	if !store.Exists(base, name) {
		return fmt.Errorf("store %q does not exist; create it first with: gramaton store create %s", name, name)
	}
	// Reject a self-add (destination == the active SOURCE store) up front,
	// BEFORE serverPostSlow can auto-start the source's server. Adding a
	// store into itself would have the source's own server LoadEngine its
	// already-locked data dir and block forever on bbolt's file lock; catch
	// the common `gramaton --store foo store add foo` mistake with a clear
	// message rather than the auto-start-then-hang (or 409) dance.
	if self, err := destIsActiveSource(base, name); err != nil {
		return err
	} else if self {
		return fmt.Errorf("cannot add store %q into itself: --store selects the SOURCE store and the argument names the DESTINATION, so they must be different stores", name)
	}
	// Guard against a bbolt lock conflict: the api opens a second engine on
	// the target's data dir, which would collide with the target's own
	// running server (same guard shape as `store delete`).
	if isServerRunning(store.Resolve(base, name)) {
		return fmt.Errorf("store %q has a running server; stop it first with: gramaton --store %s stop", name, name)
	}
	if !carveRequested(cmd) {
		return fmt.Errorf("store add requires at least one seed: --from-id, --from-collection, " +
			"--query, or a query filter such as --keywords")
	}

	req := buildCarveAddRequest(cmd, name)

	resp, err := serverPostSlow("/v1/store/add", req)
	if err != nil {
		return writeServerError("store add", err)
	}

	// Human-readable summary to stderr; the machine-parseable JSON envelope
	// stays on stdout (printEnvelope convention).
	if raw, mErr := json.Marshal(resp.Data); mErr == nil {
		var ar api.CarveAddResponse
		if json.Unmarshal(raw, &ar) == nil {
			printCarveAddSummary(os.Stderr, name, req.DestDataDir, ar)
		}
	}
	return printEnvelope(resp)
}

// printCarveAddSummary writes a short human summary of a top-up to w. On a
// dry run it is an explicit PREVIEW that names the would-be counts and
// states nothing was written; on a commit it names the target, the added
// vs already-present counts, and whether the target was thawed and
// re-frozen. Dropped boundary edges are summarized by type with the sample.
func printCarveAddSummary(w io.Writer, name, destDir string, ar api.CarveAddResponse) {
	if ar.DryRun {
		fmt.Fprintf(w, "PREVIEW (dry run -- nothing written): store %q would add "+
			"%d new node(s) (%d already present) and %d new edge(s) (%d already present) "+
			"from %d seed(s) -> %d selected node(s).\n",
			name, ar.NodesAdded, ar.NodesSkippedPresent, ar.EdgesAdded, ar.EdgesSkippedPresent,
			ar.SeedCount, ar.SelectedNodes)
	} else {
		fmt.Fprintf(w, "Added to store %q at %s: %d new node(s) (%d already present), "+
			"%d new edge(s) (%d already present).\n",
			name, destDir, ar.NodesAdded, ar.NodesSkippedPresent, ar.EdgesAdded, ar.EdgesSkippedPresent)
		if ar.Thawed {
			fmt.Fprintf(w, "The destination was frozen; it was thawed for the add and re-frozen to its prior state.\n")
		}
	}
	if ar.DroppedTotal > 0 {
		fmt.Fprintf(w, "Dropped %d boundary-crossing edge(s) by type: %v\n", ar.DroppedTotal, ar.DroppedByType)
		for _, d := range ar.DanglingSample {
			fmt.Fprintf(w, "  %s -[%s]-> %s\n", d.SourceID, d.Type, d.TargetID)
		}
		if ar.DroppedTotal > len(ar.DanglingSample) {
			fmt.Fprintf(w, "  ... and %d more (sample capped)\n", ar.DroppedTotal-len(ar.DanglingSample))
		}
	}
}

func runStoreDelete(cmd *cobra.Command, args []string) error {
	name := args[0]
	base := baseConfigDir()

	if err := store.ValidateName(name); err != nil {
		return err
	}
	if !store.Exists(base, name) {
		return fmt.Errorf("store %q does not exist", name)
	}

	// Check for running server.
	dir := store.Resolve(base, name)
	if isServerRunning(dir) {
		return fmt.Errorf("store %q has a running server; stop it first with: gramaton --store %s serve --stop", name, name)
	}

	if !storeDeleteForce {
		fmt.Fprintf(os.Stderr, "Delete store %q and all its data? This cannot be undone.\n", name)
		fmt.Fprintf(os.Stderr, "Type the store name to confirm: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != name {
			return fmt.Errorf("deletion cancelled")
		}
	}

	if err := store.Delete(base, name); err != nil {
		return err
	}

	// Drop the deleted store's MCP entry so no harness is left pointing
	// at a store that no longer exists. Idempotent: a store that was
	// never registered (or created with --no-harness) is a clean no-op.
	out := map[string]any{"deleted": name}
	syncStoreEntry(cmd.Context(), out, name, setup.EntryAbsent)

	return printJSON(out)
}

func runStoreRename(cmd *cobra.Command, args []string) error {
	oldName, newName := args[0], args[1]
	base := baseConfigDir()

	// Check for running server on the source store.
	var sourceDir string
	if oldName == "default" {
		sourceDir = base
	} else {
		sourceDir = store.Resolve(base, oldName)
	}
	if isServerRunning(sourceDir) {
		return fmt.Errorf("store %q has a running server; stop it first", oldName)
	}

	if err := store.Rename(base, oldName, newName); err != nil {
		return err
	}

	out := map[string]any{
		"renamed": oldName,
		"to":      newName,
	}

	// Re-point harness registration: register the new entry, then remove
	// the old one (RenameStoreHarness orders it so a mid-run failure
	// leaves the agent reachable through the new entry). "default" is the
	// CLI alias for the unnamed store, whose entry is the bare
	// "gramaton" -- map it to the empty store name the setup layer keys
	// its default-entry logic on.
	newRep, oldRep := setup.RenameStoreHarness(cmd.Context(), storeHarnessBackend,
		harnessStoreName(oldName), harnessStoreName(newName))
	h := map[string]any{"new_entry": newRep.Entry, "old_entry": oldRep.Entry}
	if reg := newRep.Registered(); len(reg) > 0 {
		h["registered"] = reg
	}
	if rm := oldRep.Removed(); len(rm) > 0 {
		h["removed"] = rm
	}
	if failed := append(newRep.Failures(), oldRep.Failures()...); len(failed) > 0 {
		fm := map[string]string{}
		for _, f := range failed {
			// newRep (the new-entry registration) is appended first;
			// keep its message on a same-client collision -- a failed
			// registration (store unreachable) matters more than a
			// failed removal (a stale orphan entry).
			if _, seen := fm[f.Client]; !seen {
				fm[f.Client] = fmt.Sprintf("%v", f.Err)
			}
		}
		h["failed"] = fm
	}
	out["harness"] = h
	printHarnessSummary(os.Stderr, newRep)
	// Only narrate the removal leg when it acted on a harness; otherwise
	// the two reports share the "no AI tools detected" short-circuit and
	// would print it twice.
	if len(oldRep.Clients) > 0 {
		printHarnessSummary(os.Stderr, oldRep)
	}

	return printJSON(out)
}

// harnessStoreName maps the CLI's "default" store alias to the empty
// store name the setup layer uses for the bare "gramaton" MCP entry;
// any other name passes through unchanged.
func harnessStoreName(name string) string {
	if name == "default" {
		return ""
	}
	return name
}

// storeFrozenNote is the freeze confirmation's semantics reminder.
const storeFrozenNote = "reads and search keep working; all writes are rejected until: gramaton store thaw"

func runStoreFreeze(cmd *cobra.Command, args []string) error {
	name, dir, display, err := storeFreezeTarget(args)
	if err != nil {
		return err
	}
	if err := refuseIfStoreServerAlive(name, dir, display); err != nil {
		return err
	}

	cfg, dataDir, err := storeEffectiveConfig(dir)
	if err != nil {
		return err
	}
	if err := core.FreezeStore(dataDir, cfg.Author.String()); err != nil {
		return err
	}

	// Read the manifest back rather than echoing inputs: a re-freeze
	// of an already-frozen store is a no-op that preserves the
	// ORIGINAL owner and published_at, and the confirmation must show
	// the provenance that is actually on disk.
	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		return err
	}
	out := map[string]any{
		"frozen":       display,
		"owner":        m.Owner,
		"published_at": m.PublishedAt,
		"note":         storeFrozenNote,
	}
	if w := storeServerAppearedWarning(name, dir, "frozen"); w != "" {
		out["warning"] = w
	}
	return printJSON(out)
}

func runStoreThaw(cmd *cobra.Command, args []string) error {
	name, dir, display, err := storeFreezeTarget(args)
	if err != nil {
		return err
	}
	if err := refuseIfStoreServerAlive(name, dir, display); err != nil {
		return err
	}

	_, dataDir, err := storeEffectiveConfig(dir)
	if err != nil {
		return err
	}
	// Read before thawing to report whether anything changed; owner
	// and published_at survive the thaw as provenance of the original
	// publication either way.
	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		return err
	}
	if err := core.ThawStore(dataDir); err != nil {
		return err
	}

	out := map[string]any{
		"thawed":     display,
		"was_frozen": m.ReadOnly,
		"note": "if this machine's install was set up against the read-only store, " +
			"re-run 'gramaton init' to refresh the installed agent guidance",
	}
	if w := storeServerAppearedWarning(name, dir, "thawed"); w != "" {
		out["warning"] = w
	}
	if m.Owner != "" || !m.PublishedAt.IsZero() {
		out["owner"] = m.Owner
		out["published_at"] = m.PublishedAt
		out["provenance"] = "owner and published_at record the original publication and are preserved; a future freeze overwrites them"
	}
	return printJSON(out)
}

// runStoreAttach registers a shared read-only store alongside the
// user's existing stores. The mechanics live in store.Attach, shared
// with the setup wizard's read-only route (which serves the
// read-only-ONLY case: a machine whose only gramaton is the shared
// store). Offline primitive like freeze/thaw -- no engine, no
// server; the store being created cannot have a live server yet,
// and store.Attach itself refuses when one is serving the SOURCE.
func runStoreAttach(cmd *cobra.Command, args []string) error {
	base := baseConfigDir()

	src, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", args[0], err)
	}
	srcData, err := store.ResolveAttachSource(src)
	if err != nil {
		return err
	}

	name := storeAttachName
	if name == "" {
		name = store.DefaultAttachName(srcData, base)
	}
	if err := store.ValidateName(name); err != nil {
		return err
	}

	// Resolve the freeze-original owner BEFORE mutating anything so a
	// malformed config fails the command cleanly rather than after a
	// successful attach.
	freezeOwner := ""
	if storeAttachFreezeOriginal {
		cfg, _, err := storeEffectiveConfig(base)
		if err != nil {
			return err
		}
		freezeOwner = cfg.Author.String()
	}

	res, err := store.Attach(base, name, srcData)
	if err != nil {
		return err
	}

	// The optional freeze-the-original (contract: default no, offered
	// as a flag on the non-interactive command; the wizard's
	// read-only route asks the same question interactively). Runs
	// AFTER the attach so a failed copy never leaves the source
	// changed. An already-frozen source is never re-stamped --
	// core.FreezeStore preserves existing provenance anyway, and the
	// output says so instead of pretending this command froze it.
	sourceNote := "the directory you received was not modified."
	out := map[string]any{}
	if storeAttachFreezeOriginal {
		switch {
		case res.SourceFrozen:
			out["original_frozen"] = true
			out["original_note"] = "the source was already frozen; its manifest and provenance are unchanged"
		default:
			if err := core.FreezeStore(srcData, freezeOwner); err != nil {
				out["warning"] = fmt.Sprintf("couldn't freeze the original at %s: %v (the attached copy is still frozen)", srcData, err)
			} else {
				out["original_frozen"] = true
				if freezeOwner != "" {
					out["original_owner"] = freezeOwner
				}
				sourceNote = "the source directory was also frozen (--freeze-original)."
			}
		}
	}

	out["attached"] = res.Name
	out["path"] = res.StoreDir
	out["data_dir"] = res.DataDir
	out["read_only"] = true
	out["source"] = srcData
	out["note"] = "the data was copied and the copy's STORE manifest frozen; " +
		sourceNote + " Reads and search work in full; all writes are rejected."
	out["access"] = fmt.Sprintf("gramaton --store %s <command>  (or set GRAMATON_STORE=%s)", res.Name, res.Name)
	// Register the attached store's MCP entry with every detected harness
	// so an agent can search it immediately. Only read tools surface --
	// the copy's frozen manifest is resolved at MCP-process startup. With
	// --no-harness we fall back to printing the manual entry instead.
	if storeNoHarness {
		out["mcp"] = fmt.Sprintf("to let an AI harness search it, add an MCP entry running: gramaton --store %s mcp "+
			"(e.g. claude mcp add --scope user gramaton-%s gramaton -- --store %s mcp); "+
			"only read tools are registered against a frozen store", res.Name, res.Name, res.Name)
	} else {
		syncStoreEntry(cmd.Context(), out, res.Name, setup.EntryPresent)
	}
	if res.Manifest.Owner != "" {
		out["owner"] = res.Manifest.Owner
	}
	if !res.Manifest.PublishedAt.IsZero() {
		out["published_at"] = res.Manifest.PublishedAt
	}
	return printJSON(out)
}

// storeFreezeTarget resolves the freeze/thaw target store: the
// positional name when given ('default' aliases the unnamed store),
// otherwise the active store (--store / GRAMATON_STORE, empty for
// the unnamed default). Returns the resolved name (empty for
// default), the store's config directory, and the display name used
// in output.
func storeFreezeTarget(args []string) (name, cfgDirPath, display string, err error) {
	if len(args) == 1 {
		name = args[0]
	} else {
		name = activeStoreName()
	}
	if name == "default" {
		name = ""
	}
	base := baseConfigDir()
	if name != "" {
		if err := store.ValidateName(name); err != nil {
			return "", "", "", err
		}
		if !store.Exists(base, name) {
			return "", "", "", fmt.Errorf("store %q does not exist", name)
		}
		return name, store.Resolve(base, name), name, nil
	}
	if !store.DefaultExists(base) {
		return "", "", "", fmt.Errorf("default store has no data; run 'gramaton init' first")
	}
	return "", base, "(default)", nil
}

// refuseIfStoreServerAlive rejects freeze/thaw while the target
// store's server is running (same guard shape as cli/backfill.go's
// server-alive gate). The engine reads the STORE manifest once at
// open time, so flipping it under a live server leaves that engine
// enforcing a stale read-only state until restart.
func refuseIfStoreServerAlive(name, cfgDirPath, display string) error {
	info, err := server.ReadServerInfo(cfgDirPath)
	if err != nil || !server.IsProcessAlive(info.PID) {
		return nil
	}
	hint := "gramaton stop"
	if name != "" {
		hint = fmt.Sprintf("gramaton --store %s stop", name)
	}
	return fmt.Errorf("store %s has a running server (pid %d); its engine reads the read-only flag at startup and would not honor the change. Stop it first: %s", display, info.PID, hint)
}

// storeServerAppearedWarning re-probes for a live server immediately
// AFTER a freeze/thaw manifest write. refuseIfStoreServerAlive is
// check-then-act: a server that opens its engine between the alive
// check and the manifest write (CLI auto-start racing the command, or
// `gramaton serve` mid startup, which loads the engine before writing
// server.json) has latched the OLD read-only state and enforces it
// until restart. The window is seconds on a single-user tool, so no
// locking -- this warning closes the confusion, not the window.
// Returns "" when no server appeared; verb is the past participle for
// the message ("frozen", "thawed").
func storeServerAppearedWarning(name, cfgDirPath, verb string) string {
	info, err := server.ReadServerInfo(cfgDirPath)
	if err != nil || !server.IsProcessAlive(info.PID) {
		return ""
	}
	hint := "gramaton stop"
	if name != "" {
		hint = fmt.Sprintf("gramaton --store %s stop", name)
	}
	return fmt.Sprintf(
		"a server (pid %d) started while the store was being %s; it opened the store before the change and will not enforce it. Restart it for the change to take effect: %s",
		info.PID, verb, hint)
}

// storeEffectiveConfig loads a store's effective config (store
// config overlaid on global, the same merge the engine uses) and
// resolves its data directory: the store's OWN config.yaml's
// data_dir when set, otherwise <cfgDir>/data.
//
// This matches the engine's resolution (core/engine.go: cfg.DataDir,
// defaulting to <cfgDir>/data when empty) for every store `gramaton
// init`, `store create`, or `store attach` produces, because all
// three write a per-store data_dir. The one divergence is deliberate:
// for a named store that has no config.yaml of its own (hand-built,
// or created before store create pinned data_dir), the engine's
// merge lets the GLOBAL config's data_dir bleed through, which
// resolves the DEFAULT store's data dir -- freezing or badging the
// wrong store. The store's own file is read directly (ownDataDir)
// because after the Defaults()+global merge an unset data_dir is
// indistinguishable from an inherited one.
func storeEffectiveConfig(cfgDirPath string) (config.Config, string, error) {
	cfgPath := filepath.Join(cfgDirPath, "config.yaml")
	globalPath := filepath.Join(baseConfigDir(), "config.yaml")
	cfg, err := config.LoadWithFallback(cfgPath, globalPath)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("load config: %w", err)
	}
	dataDir, err := ownDataDir(cfgPath)
	if err != nil {
		return config.Config{}, "", err
	}
	if dataDir == "" {
		dataDir = filepath.Join(cfgDirPath, "data")
	}
	return cfg, dataDir, nil
}

// ownDataDir reads just the data_dir key out of a single config
// file, with no defaults and no global fallback. A missing file is
// "" (caller applies the <cfgDir>/data default); a malformed file is
// an error, matching Load's fail-loud behavior.
func ownDataDir(cfgPath string) (string, error) {
	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	var probe struct {
		DataDir string `yaml:"data_dir"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return "", fmt.Errorf("parse config %s: %w", cfgPath, err)
	}
	return strings.TrimSpace(probe.DataDir), nil
}

// storeReadOnlyBadge derives a store's read-only indicator live from
// its STORE manifest. Error-tolerant for listing: when the manifest
// (or the config needed to locate the data dir) cannot be read it
// returns the "(manifest unreadable)" note instead of guessing
// writable.
func storeReadOnlyBadge(cfgDirPath string) (readOnly bool, note string) {
	_, dataDir, err := storeEffectiveConfig(cfgDirPath)
	if err != nil {
		return false, "(manifest unreadable)"
	}
	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		return false, "(manifest unreadable)"
	}
	return m.ReadOnly, ""
}

// destIsActiveSource reports whether the top-up destination store resolves
// to the SAME data directory as the currently-active source store (the one
// --store / GRAMATON_STORE selects, which the server-mediated add reads
// live under a read lock). Adding a store into itself deadlocks on bbolt's
// single-writer file lock, so runStoreAdd refuses it before auto-starting
// the source's server. Compares the effective data dirs (honoring any
// per-store config data_dir override) so it catches the case regardless of
// how each store's directory is configured.
func destIsActiveSource(base, destName string) (bool, error) {
	_, srcData, err := storeEffectiveConfig(store.Resolve(base, activeStoreName()))
	if err != nil {
		return false, err
	}
	_, destData, err := storeEffectiveConfig(store.Resolve(base, destName))
	if err != nil {
		return false, err
	}
	return sameDir(srcData, destData), nil
}

// sameDir reports whether two filesystem paths name the same directory,
// canonicalizing separators, "." / ".." and (best-effort) symlinks. Falls
// back to a lexical compare when a path cannot be resolved on disk.
func sameDir(a, b string) bool {
	return canonDir(a) == canonDir(b)
}

func canonDir(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// isServerRunning checks if a server is running for a config directory.
func isServerRunning(cfgDir string) bool {
	info, err := server.ReadServerInfo(cfgDir)
	if err != nil {
		return false
	}
	return server.IsProcessAlive(info.PID)
}

// nameForResolve converts a StoreInfo to a name suitable for
// store.Resolve (empty string for default).
func nameForResolve(s store.StoreInfo) string {
	if s.Default {
		return ""
	}
	return s.Name
}
