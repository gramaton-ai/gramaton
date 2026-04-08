package cli

import "github.com/spf13/cobra"

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a running Gramaton server",
	Long: `Sends a graceful shutdown request to the running server.
The server flushes pending access metadata and exits cleanly.

Equivalent to: gramaton serve --stop`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return stopServer()
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
