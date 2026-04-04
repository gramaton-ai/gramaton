package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List unclassified records",
	Long:  `Shows records with processing_status "captured" that have not yet been classified by an agent.`,
	RunE:  runPending,
}

func init() {
	rootCmd.AddCommand(pendingCmd)
}

func runPending(cmd *cobra.Command, args []string) error {
	resp, err := serverGet("/v1/pending")
	if err != nil {
		return fmt.Errorf("pending: %w", err)
	}

	return printEnvelope(resp)
}
