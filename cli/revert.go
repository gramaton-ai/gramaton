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

func runRevert(cmd *cobra.Command, args []string) error {
	hash := args[0]

	resp, err := serverPost("/v1/revert", map[string]string{"hash": hash})
	if err != nil {
		return writeError("revert_error", fmt.Sprintf("revert: %s", err), false)
	}

	return printEnvelope(resp)
}
