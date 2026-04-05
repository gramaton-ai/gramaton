package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show aggregate statistics for the knowledge store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := serverGet("/v1/stats")
		if err != nil {
			return fmt.Errorf("stats: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	rootCmd.AddCommand(statsCmd)
}
