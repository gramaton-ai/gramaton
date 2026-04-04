package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var branchCmd = &cobra.Command{
	Use:   "branch",
	Short: "Manage branches",
	Long: `Branches enable speculative reasoning and safe curation.
Subcommands: create, list, merge, discard, checkout.`,
}

var branchCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new branch from current HEAD",
	Args:  cobra.ExactArgs(1),
	RunE:  runBranchCreate,
}

var branchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all branches",
	RunE:  runBranchList,
}

var branchMergeCmd = &cobra.Command{
	Use:   "merge <name>",
	Short: "Merge a branch into main",
	Args:  cobra.ExactArgs(1),
	RunE:  runBranchMerge,
}

var branchDiscardCmd = &cobra.Command{
	Use:   "discard <name>",
	Short: "Discard a branch and its changes",
	Args:  cobra.ExactArgs(1),
	RunE:  runBranchDiscard,
}

var branchCheckoutCmd = &cobra.Command{
	Use:   "checkout <name>",
	Short: "Switch to a branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runBranchCheckout,
}

func init() {
	branchCmd.AddCommand(branchCreateCmd)
	branchCmd.AddCommand(branchListCmd)
	branchCmd.AddCommand(branchMergeCmd)
	branchCmd.AddCommand(branchDiscardCmd)
	branchCmd.AddCommand(branchCheckoutCmd)
	rootCmd.AddCommand(branchCmd)
}

// Branch ref layout:
//   <data_dir>/refs/<branch_name>  -- file containing commit hash
//   <data_dir>/HEAD                -- commit hash of current state
//   <data_dir>/BRANCH              -- name of active branch (default: "main")

func refsDir(dataDir string) string {
	return filepath.Join(dataDir, "refs")
}

func activeBranch(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(dataDir, "BRANCH"))
	if err != nil {
		return "main"
	}
	return strings.TrimSpace(string(data))
}

func setActiveBranch(dataDir, name string) error {
	return os.WriteFile(filepath.Join(dataDir, "BRANCH"), []byte(name), 0o644)
}

func readRef(dataDir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(refsDir(dataDir), name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeRef(dataDir, name, hash string) error {
	dir := refsDir(dataDir)
	os.MkdirAll(dir, 0o755)
	return os.WriteFile(filepath.Join(dir, name), []byte(hash), 0o644)
}

func deleteRef(dataDir, name string) error {
	return os.Remove(filepath.Join(refsDir(dataDir), name))
}

// --- Command implementations ---

func runBranchCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "main" {
		return writeError("invalid_name", "cannot create a branch named 'main'", false)
	}

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	// Check branch doesn't already exist.
	if _, err := readRef(eng.cfg.DataDir, name); err == nil {
		return writeError("exists", fmt.Sprintf("branch %q already exists", name), false)
	}

	// Create branch pointing to current HEAD.
	if err := writeRef(eng.cfg.DataDir, name, eng.headHash); err != nil {
		return writeError("write_error", err.Error(), false)
	}

	return printJSON(map[string]string{
		"branch":  name,
		"commit":  eng.headHash[:12],
		"message": fmt.Sprintf("branch %q created from HEAD", name),
	})
}

func runBranchList(cmd *cobra.Command, args []string) error {
	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	dir := refsDir(eng.cfg.DataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No refs dir = no branches.
		return printJSON([]map[string]string{})
	}

	active := activeBranch(eng.cfg.DataDir)

	var branches []map[string]string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hash, _ := readRef(eng.cfg.DataDir, e.Name())
		b := map[string]string{
			"name":   e.Name(),
			"commit": truncHash(hash),
		}
		if e.Name() == active {
			b["active"] = "true"
		}
		branches = append(branches, b)
	}

	return printJSON(branches)
}

func runBranchCheckout(cmd *cobra.Command, args []string) error {
	name := args[0]

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	hash, err := readRef(eng.cfg.DataDir, name)
	if err != nil {
		return writeError("not_found", fmt.Sprintf("branch %q not found", name), false)
	}

	// Update HEAD to point to branch's commit.
	headPath := filepath.Join(eng.cfg.DataDir, "HEAD")
	if err := os.WriteFile(headPath, []byte(hash), 0o644); err != nil {
		return writeError("write_error", err.Error(), false)
	}

	if err := setActiveBranch(eng.cfg.DataDir, name); err != nil {
		return writeError("write_error", err.Error(), false)
	}

	return printJSON(map[string]string{
		"branch": name,
		"commit": truncHash(hash),
	})
}

func runBranchMerge(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "main" {
		return writeError("invalid", "cannot merge main into itself", false)
	}

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	// Ensure we're on main.
	if err := setActiveBranch(eng.cfg.DataDir, "main"); err != nil {
		return writeError("write_error", err.Error(), false)
	}

	branchHash, err := readRef(eng.cfg.DataDir, name)
	if err != nil {
		return writeError("not_found", fmt.Sprintf("branch %q not found", name), false)
	}

	// Load the branch state as current state.
	// For v0.1, merge = fast-forward (adopt the branch's state as the new main).
	// Proper three-way merge is a future feature.
	_, err = eng.graph.Load(eng.store, branchHash)
	if err != nil {
		return writeError("load_error", err.Error(), false)
	}

	commit, err := eng.save(fmt.Sprintf("merge branch %q", name))
	if err != nil {
		return writeError("save_error", err.Error(), false)
	}

	// Update main ref.
	writeRef(eng.cfg.DataDir, "main", commit.Hash)

	// Clean up branch ref.
	deleteRef(eng.cfg.DataDir, name)

	return printJSON(map[string]string{
		"merged":     name,
		"new_commit": commit.Hash[:12],
	})
}

func runBranchDiscard(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "main" {
		return writeError("invalid", "cannot discard main", false)
	}

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	if _, err := readRef(eng.cfg.DataDir, name); err != nil {
		return writeError("not_found", fmt.Sprintf("branch %q not found", name), false)
	}

	// If we're on the discarded branch, switch back to main.
	if activeBranch(eng.cfg.DataDir) == name {
		mainHash, err := readRef(eng.cfg.DataDir, "main")
		if err == nil {
			headPath := filepath.Join(eng.cfg.DataDir, "HEAD")
			os.WriteFile(headPath, []byte(mainHash), 0o644)
		}
		setActiveBranch(eng.cfg.DataDir, "main")
	}

	deleteRef(eng.cfg.DataDir, name)

	return printJSON(map[string]string{
		"discarded": name,
	})
}
