package cli

import (
	"context"
	"fmt"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/logging"
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

The MCP process also starts the HTTP server in the background so
that curation, observe, and auto-backup all function. The HTTP
server shares the same engine instance (single process, single
graph). It shuts down automatically when the MCP transport closes.`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	dir := configDir()

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	// MCP uses stdio -- log to file only, never stderr.
	engineCfg := eng.Config()
	logger, logWriter, err := logging.New(engineCfg.Logging, dir, false)
	if err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	defer logWriter.Close()

	cfg := server.DefaultConfig()
	cfg.ConfigDir = dir
	cfg.StoreName = activeStoreName()

	srv := server.New(eng, cfg, logger)

	// Start HTTP server + curation runner in background goroutines.
	// This ensures curation, observe, auto-backup, and CLI commands
	// all work while the MCP stdio transport is active. Same engine
	// instance -- no dual-process coordination needed.
	if err := srv.StartHTTP(); err != nil {
		// Non-fatal: log and continue with MCP-only mode.
		// The port might be in use by another gramaton process.
		logger.Warn("HTTP server failed to start (MCP will work without it)",
			"err", err)
	} else {
		defer srv.Shutdown()
	}

	mcpServer := srv.MCPServer()

	ctx := context.Background()
	if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	return nil
}
