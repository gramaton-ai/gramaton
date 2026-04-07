package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <record-id>",
	Short: "Inspect a knowledge record",
	Long: `Tier 2 retrieval: view full content, all metadata, and related
records for a specific node.`,
	Args: cobra.ExactArgs(1),
	RunE: runInspect,
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	nodeID := args[0]

	resp, err := serverGet(fmt.Sprintf("/v1/records/%s", url.PathEscape(nodeID)))
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	return printEnvelope(resp)
}
