package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/gramaton-ai/gramaton/core"
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

When the store is read-only (frozen via 'gramaton store freeze'),
write tools are not registered: agents attached to a frozen store
see only the read surface (search, inspect, explore, ...).`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

// mcpReadOnlyInstructions is the MCP server-instructions text handed
// to clients when the attached store is frozen. It leads the
// instructions string -- the read-only state must be the first thing
// a client learns about this server. There is currently no base
// instructions text to append after it; if one is ever added, this
// sentence stays first.
const mcpReadOnlyInstructions = "This Gramaton store is read-only (frozen): " +
	"all writes are rejected and no write tools are registered. " +
	"Search and the other read tools work normally."

func runMCP(cmd *cobra.Command, args []string) error {
	// Ensure the HTTP server is running (auto-starts if needed).
	if _, err := serverURL(); err != nil {
		return fmt.Errorf("server: %w", err)
	}

	mcpServer := newProxyMCPServer(mcpStoreReadOnly())

	// Cancel the SDK Run loop on SIGINT/SIGTERM so manual interruption
	// (Ctrl-C in a foreground invocation, or a parent process sending
	// SIGTERM) returns cleanly instead of being trapped inside the SDK
	// stdio loop until stdin closes.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return mcpServer.Run(ctx, &mcp.StdioTransport{})
}

// newProxyMCPServer builds the proxy MCP server with the tool
// surface and instructions matching the store's frozen state: full
// registration for a writable store, the read-only surface (plus a
// leading read-only notice in the server instructions) for a frozen
// one.
func newProxyMCPServer(readOnly bool) *mcp.Server {
	var opts *mcp.ServerOptions
	if readOnly {
		opts = &mcp.ServerOptions{Instructions: mcpReadOnlyInstructions}
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: version.Version,
	}, opts)
	if readOnly {
		registerProxyToolsReadOnly(mcpServer)
	} else {
		registerProxyTools(mcpServer)
	}
	return mcpServer
}

// mcpStoreReadOnly resolves the frozen state of the store this MCP
// process serves. Production wiring for resolveMCPReadOnly: the
// running server's /v1/status envelope first, the local STORE
// manifest second.
func mcpStoreReadOnly() bool {
	return resolveMCPReadOnly(
		func() (*server.ResponseEnvelope, error) {
			return serverGet("/v1/status")
		},
		func() (core.StoreManifest, error) {
			_, dataDir, err := storeEffectiveConfig(configDir())
			if err != nil {
				return core.StoreManifest{}, err
			}
			return core.ReadStoreManifest(dataDir)
		},
	)
}

// resolveMCPReadOnly decides whether the MCP process should register
// the read-only tool surface. Resolution order:
//
//  1. The server's /v1/status envelope (store_readonly). The
//     server's engine is authoritative -- it read the manifest at
//     open time and freeze/thaw refuse to run underneath it. Any
//     answer here, frozen or writable, wins.
//  2. On a fetch error only: the local STORE manifest.
//  3. If both fail: full registration. Fail open -- a wrongly hidden
//     toolset strands the agent, while a wrongly visible write tool
//     just returns "forbidden" from the api-layer guards.
func resolveMCPReadOnly(fetchEnvelope func() (*server.ResponseEnvelope, error), readManifest func() (core.StoreManifest, error)) bool {
	if env, err := fetchEnvelope(); err == nil {
		return env.StoreReadonly
	}
	if m, err := readManifest(); err == nil {
		return m.ReadOnly
	}
	return false
}
