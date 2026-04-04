package cli

import (
	"os"
	"path/filepath"

	"github.com/brandonlattin/gramaton/server"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show knowledge store status",
	Long:  `Displays node and edge counts, storage health, and configuration state.`,
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Try the server first.
	dir := configDir()
	info, err := server.ReadServerInfo(dir)
	if err == nil && server.IsProcessAlive(info.PID) {
		resp, err := serverGet("/v1/status")
		if err == nil {
			return printEnvelope(resp)
		}
	}

	// Fallback: report basic config state without the server.
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return printJSON(map[string]any{
			"initialized": false,
			"config_path": cfgPath,
		})
	}

	return printJSON(map[string]any{
		"initialized": true,
		"config_path": cfgPath,
		"server":      "not running",
	})
}
