package cli

import (
	"context"
	"fmt"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as an MCP server (stdio transport)",
	Long: `Starts a Gramaton MCP server communicating over stdin/stdout.
This is used by MCP clients like Claude Code that spawn the server
as a child process.

The MCP server loads the knowledge graph directly (no HTTP daemon
needed for stdio mode). For Streamable HTTP transport, use
'gramaton serve' instead -- the daemon serves MCP at /mcp.`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	dir := configDir()

	eng, err := core.LoadEngine(dir)
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	cfg := server.DefaultConfig()
	cfg.ConfigDir = dir

	srv := server.New(eng, cfg)
	mcpServer := srv.MCPServer()

	ctx := context.Background()
	if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	return nil
}
