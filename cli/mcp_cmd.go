package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gramaton-ai/gramaton/internal/version"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as an MCP server (stdio transport)",
	Long: `Starts a Gramaton MCP server communicating over stdin/stdout.
This is used by MCP clients like Claude Code that spawn the server
as a child process.

The MCP process is a stateless proxy that forwards tool calls to
the gramaton HTTP server. If no server is running, one is
auto-started. Multiple MCP processes can run simultaneously --
they all share the same server and engine instance.

Each proxy registers itself in the store's config dir so that
"gramaton stop" can stop it and "gramaton status" can list it.`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	// Ensure the HTTP server is running (auto-starts if needed).
	if _, err := serverURL(); err != nil {
		return fmt.Errorf("server: %w", err)
	}

	// Register this proxy so `gramaton stop` can reap it and
	// `gramaton status` can list it. The proxy is spawned by the MCP
	// client, not by the server, so this file is the only handle the
	// rest of the CLI has on it. Registration is bookkeeping: if it
	// fails, the proxy still runs (stderr is safe to write to --
	// stdout is the MCP transport). The deferred remove covers every
	// clean exit: stdin close and SIGINT/SIGTERM both surface as a
	// return from Run below, since NotifyContext turns signals into
	// context cancellation.
	dir := configDir()
	if err := server.RegisterMCPProxy(dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: mcp proxy registration failed: %v\n", err)
	} else {
		defer server.RemoveMCPProxy(dir, os.Getpid())
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: version.Version,
	}, nil)

	registerProxyTools(mcpServer)

	// Cancel the SDK Run loop on SIGINT/SIGTERM so manual interruption
	// (Ctrl-C in a foreground invocation, or a parent process sending
	// SIGTERM) returns cleanly instead of being trapped inside the SDK
	// stdio loop until stdin closes.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return mcpServer.Run(ctx, &mcp.StdioTransport{})
}
