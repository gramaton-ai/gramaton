package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var revertCmd = &cobra.Command{
	Use:   "revert <commit-hash>",
	Short: "Revert to a previous commit",
	Long: `Loads the graph state from the specified commit and saves it as a
new commit. The reverted commit becomes the new HEAD. History is
preserved -- the revert itself is a new commit pointing to the
current HEAD as its parent.`,
	Args: cobra.ExactArgs(1),
	RunE: runRevert,
}

func init() {
	rootCmd.AddCommand(revertCmd)
}

type revertOutput struct {
	RevertedTo string `json:"reverted_to"`
	NewCommit  string `json:"new_commit"`
}

func runRevert(cmd *cobra.Command, args []string) error {
	targetHash := args[0]

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	// Resolve short hash.
	fullHash, err := resolveHash(eng.store, targetHash)
	if err != nil {
		return writeError("hash_error", err.Error(), false)
	}

	// Load the target commit's state into the graph.
	_, err = eng.graph.Load(eng.store, fullHash)
	if err != nil {
		return writeError("load_error", fmt.Sprintf("failed to load commit %s: %s", targetHash, err), false)
	}

	// Save as new commit with current HEAD as parent.
	commit, err := eng.save(fmt.Sprintf("revert to %s", fullHash[:12]))
	if err != nil {
		return writeError("save_error", err.Error(), false)
	}

	return printJSON(revertOutput{
		RevertedTo: fullHash[:12],
		NewCommit:  commit.Hash[:12],
	})
}
