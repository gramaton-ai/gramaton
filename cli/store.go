package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/gramaton-ai/gramaton/server"
	"github.com/gramaton-ai/gramaton/store"
	"github.com/spf13/cobra"
)

var storeDeleteForce bool

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

func init() {
	storeDeleteCmd.Flags().BoolVar(&storeDeleteForce, "force", false, "skip confirmation prompt")
	storeCmd.AddCommand(storeListCmd, storeCreateCmd, storeDeleteCmd, storeRenameCmd)
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
	}
	var entries []storeEntry
	for _, s := range stores {
		isActive := (s.Default && active == "") || (!s.Default && s.Name == active)
		running := isServerRunning(store.Resolve(base, nameForResolve(s)))
		entries = append(entries, storeEntry{
			Name:    s.Name,
			Path:    s.Path,
			Active:  isActive,
			Running: running,
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
	return printJSON(map[string]any{
		"created": name,
		"path":    dir,
	})
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
