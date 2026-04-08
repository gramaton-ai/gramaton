package cli

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/internal/version"
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
they all share the same server and engine instance.`,
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

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: version.Version,
	}, nil)

	registerProxyTools(mcpServer)

	return mcpServer.Run(context.Background(), &mcp.StdioTransport{})
}
