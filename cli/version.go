package cli

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/internal/version"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.Version = version.Version
	rootCmd.SetVersionTemplate("gramaton {{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and build information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("gramaton %s\n", version.Version)
		fmt.Printf("  commit: %s\n", version.Commit)
		fmt.Printf("  built:  %s\n", version.Date)
		fmt.Printf("  store format: %d\n", version.StoreFormatVersion)
	},
}
