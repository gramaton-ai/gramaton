package cli

import (
	"fmt"
	"net/url"
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

// Branch ref helpers used by engine.go for save operations.

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

func writeRef(dataDir, name, hash string) error {
	dir := refsDir(dataDir)
	os.MkdirAll(dir, 0o755)
	return atomicWriteFile(filepath.Join(dir, name), []byte(hash), 0o600)
}

// --- Command implementations ---

func runBranchCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	resp, err := serverPost("/v1/branches", map[string]string{"name": name})
	if err != nil {
		return writeError("branch_error", fmt.Sprintf("create branch: %s", err), false)
	}

	return printEnvelope(resp)
}

func runBranchList(cmd *cobra.Command, args []string) error {
	resp, err := serverGet("/v1/branches")
	if err != nil {
		return writeError("branch_error", fmt.Sprintf("list branches: %s", err), false)
	}

	return printEnvelope(resp)
}

func runBranchCheckout(cmd *cobra.Command, args []string) error {
	name := args[0]
	path := fmt.Sprintf("/v1/branches/%s/checkout", url.PathEscape(name))

	resp, err := serverPost(path, nil)
	if err != nil {
		return writeError("branch_error", fmt.Sprintf("checkout branch: %s", err), false)
	}

	return printEnvelope(resp)
}

func runBranchMerge(cmd *cobra.Command, args []string) error {
	name := args[0]
	path := fmt.Sprintf("/v1/branches/%s/merge", url.PathEscape(name))

	resp, err := serverPost(path, nil)
	if err != nil {
		return writeError("branch_error", fmt.Sprintf("merge branch: %s", err), false)
	}

	return printEnvelope(resp)
}

func runBranchDiscard(cmd *cobra.Command, args []string) error {
	name := args[0]
	path := fmt.Sprintf("/v1/branches/%s", url.PathEscape(name))

	resp, err := serverDelete(path)
	if err != nil {
		return writeError("branch_error", fmt.Sprintf("discard branch: %s", err), false)
	}

	return printEnvelope(resp)
}
