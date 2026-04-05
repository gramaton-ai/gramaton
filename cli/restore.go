package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var restoreForce bool

var restoreCmd = &cobra.Command{
	Use:   "restore <file>",
	Short: "Restore from a backup archive",
	Long: `Restores the knowledge store from a backup archive. This
overwrites the current store data. Use --force to skip the
interactive confirmation prompt.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !restoreForce {
			fmt.Fprint(os.Stderr, "This will overwrite the current store. Continue? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}

		resp, err := serverPost("/v1/restore", map[string]any{
			"path":  args[0],
			"force": true,
		})
		if err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	restoreCmd.Flags().BoolVar(&restoreForce, "force", false, "skip confirmation prompt")
	rootCmd.AddCommand(restoreCmd)
}
