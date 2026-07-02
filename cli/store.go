package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/gramaton-ai/gramaton/store"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	storeDeleteForce    bool
	storeCreateReadOnly bool
	storeAttachName     string
)

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
	RunE:  runStoreList,
}

var storeCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new named store",
	Args:  cobra.ExactArgs(1),
	RunE:  runStoreCreate,
}

var storeDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a named store and all its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runStoreDelete,
}

var storeRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a store (use 'default' for the unnamed store)",
	Args:  cobra.ExactArgs(2),
	RunE:  runStoreRename,
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
gramaton stop.

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

Reach the attached store with --store or GRAMATON_STORE:

  gramaton --store <name> search "<query>" --top 5

To let an AI harness search it, add a second MCP entry running
'gramaton --store <name> mcp' (for example:
claude mcp add --scope user gramaton-<name> gramaton -- --store <name> mcp).`,
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
	storeCreateCmd.Flags().BoolVar(&storeCreateReadOnly, "read-only", false,
		"freeze the store immediately after creation (owner: the configured author)")
	storeDeleteCmd.Flags().BoolVar(&storeDeleteForce, "force", false, "skip confirmation prompt")
	storeAttachCmd.Flags().StringVar(&storeAttachName, "name", "",
		"local name for the attached store (default: derived from the directory name)")
	storeCmd.AddCommand(storeListCmd, storeCreateCmd, storeDeleteCmd, storeRenameCmd, storeFreezeCmd, storeThawCmd, storeAttachCmd)
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
		Path    string `json:"path"`
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
	}
	var entries []storeEntry
	for _, s := range stores {
		isActive := (s.Default && active == "") || (!s.Default && s.Name == active)
		dir := store.Resolve(base, nameForResolve(s))
		running := isServerRunning(dir)
		readOnly, manifestNote := storeReadOnlyBadge(dir)
		entries = append(entries, storeEntry{
			Name:     s.Name,
			Path:     s.Path,
			Active:   isActive,
			Running:  running,
			ReadOnly: readOnly,
			Manifest: manifestNote,
		})
	}

	return printJSON(entries)
}

func runStoreCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	base := baseConfigDir()

	if err := store.Create(base, name); err != nil {
		return err
	}

	dir := store.Resolve(base, name)
	out := map[string]any{
		"created": name,
		"path":    dir,
	}

	if storeCreateReadOnly {
		// A fresh store has no per-store config, so
		// storeEffectiveConfig resolves the data dir store.Create just
		// made (<dir>/data), while the owner comes from the creating
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

	return printJSON(out)
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

	return printJSON(map[string]any{"deleted": name})
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

	return printJSON(map[string]any{
		"renamed": oldName,
		"to":      newName,
	})
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
	return printJSON(map[string]any{
		"frozen":       display,
		"owner":        m.Owner,
		"published_at": m.PublishedAt,
		"note":         storeFrozenNote,
	})
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
// server; the store being created cannot have a live server yet.
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

	res, err := store.Attach(base, name, srcData)
	if err != nil {
		return err
	}

	out := map[string]any{
		"attached":  res.Name,
		"path":      res.StoreDir,
		"data_dir":  res.DataDir,
		"read_only": true,
		"source":    srcData,
		"note": "the data was copied and the copy's STORE manifest frozen; " +
			"the directory you received was not modified. Reads and search work in full; all writes are rejected.",
		"access": fmt.Sprintf("gramaton --store %s <command>  (or set GRAMATON_STORE=%s)", res.Name, res.Name),
		"mcp": fmt.Sprintf("to let an AI harness search it, add a second MCP entry running: gramaton --store %s mcp "+
			"(e.g. claude mcp add --scope user gramaton-%s gramaton -- --store %s mcp); "+
			"only read tools are registered against a frozen store", res.Name, res.Name, res.Name),
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

// storeEffectiveConfig loads a store's effective config (store
// config overlaid on global, the same merge the engine uses) and
// resolves its data directory: the store's OWN config.yaml's
// data_dir when set, otherwise <cfgDir>/data.
//
// This matches the engine's resolution (core/engine.go: cfg.DataDir,
// defaulting to <cfgDir>/data when empty) for every store `gramaton
// init` produces, because init always writes a per-store data_dir.
// The one divergence is deliberate: for a named store that has no
// config.yaml of its own (bare `store create`), the engine's merge
// lets the GLOBAL config's data_dir bleed through, which resolves
// the DEFAULT store's data dir -- freezing or badging the wrong
// store. The store's own file is read directly (ownDataDir) because
// after the Defaults()+global merge an unset data_dir is
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
