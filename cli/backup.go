package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create a backup of the knowledge store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := serverPostSlow("/v1/backup", map[string]any{})
		if err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)
}
