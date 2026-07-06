package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var stopKeepMCP bool

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a running Gramaton server and its MCP proxies",
	Long: `Stops the registered MCP proxy processes for this store, then sends
a graceful shutdown request to the running server. The server
flushes pending access metadata and exits cleanly.

MCP proxies (spawned by MCP clients via "gramaton mcp") are stopped
first because a surviving proxy silently auto-starts a replacement
server on its next tool call. Use --keep-mcp to leave the proxies
running and only stop the server (equivalent to
"gramaton serve --stop").`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStop(configDir(), stopKeepMCP)
	},
}

func init() {
	stopCmd.Flags().BoolVar(&stopKeepMCP, "keep-mcp", false, "leave registered MCP proxy processes running")
	rootCmd.AddCommand(stopCmd)
}

// runStop reaps registered MCP proxies, then stops the server.
//
// The ordering is load-bearing: proxies first, server second. A
// proxy that outlives the server auto-starts a replacement on its
// next tool call (cli/client.go serverURL respects
// server.auto_start, default true). Stopping the server first would
// open a window where a still-alive proxy resurrects it -- during an
// upgrade that means a stale-binary proxy serving old tool schemas.
// Do not invert this order.
func runStop(dir string, keepMCP bool) error {
	if !keepMCP {
		if n, _ := reapMCPProxies(dir, defaultProcOps(dir), os.Stderr); n > 0 {
			noun := "proxies"
			if n == 1 {
				noun = "proxy"
			}
			fmt.Fprintf(os.Stderr, "%d mcp %s stopped\n", n, noun)
		}
	}
	// If a local server is actually running, stop it -- even for a store
	// whose config also points remote. `remote add` refuses to create
	// that mixed state, so this is belt-and-suspenders against a
	// hand-edited config, and it never orphans a live server.
	if isServerRunning(dir) {
		return stopServer(dir)
	}
	// No live local server. For a remote store that is the expected
	// state: report it and exit 0 rather than the spurious "no running
	// server found".
	if isRemoteStore(dir) {
		fmt.Fprintln(os.Stderr, "no local server to stop (remote store)")
		return nil
	}
	return stopServer(dir)
}
