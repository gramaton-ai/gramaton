package cli

import (
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/server"
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
	dir := configDir()

	// Registered MCP proxies for this store. ListMCPProxies prunes
	// dead-PID entries as it reads, so only live proxies appear.
	// Included in both the server-backed and fallback outputs:
	// proxies outlive servers, and "a proxy is still connected"
	// matters most when the server is down (a surviving proxy
	// auto-starts a fresh one on its next tool call). The binary
	// path makes proxies that predate an upgrade visible.
	proxies := server.ListMCPProxies(dir)
	if proxies == nil {
		proxies = []server.MCPProxyInfo{}
	}
	// Both keys always present so consumers never branch on absence.
	mcpField := map[string]any{"count": len(proxies), "proxies": proxies}

	// Try the server first.
	info, err := server.ReadServerInfo(dir)
	if err == nil && server.IsProcessAlive(info.PID) {
		resp, err := serverGet("/v1/status")
		if err == nil {
			return printEnvelopeExtra(resp, map[string]any{"mcp_proxies": mcpField})
		}
	}

	// Fallback: report basic config state without the server.
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return printJSON(map[string]any{
			"initialized": false,
			"config_path": cfgPath,
			"mcp_proxies": mcpField,
		})
	}

	status := map[string]any{
		"initialized": true,
		"config_path": cfgPath,
		"server":      "not running",
		"mcp_proxies": mcpField,
	}
	if name := activeStoreName(); name != "" {
		status["store"] = name
	}
	return printJSON(status)
}
