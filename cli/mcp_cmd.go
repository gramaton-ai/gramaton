package cli

import (
	"context"
	"fmt"
	"os"
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
see only the read surface (search, inspect, explore, ...).

Each proxy registers itself in the store's config dir so that
"gramaton stop" can stop it and "gramaton status" can list it.`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

// mcpBaseInstructions is the MCP server-instructions text handed to
// every client. Clients that defer tool schemas behind tool search
// (Claude Code's default) still load the server name and this string
// at session start, so it is the one channel guaranteed to reach the
// model when the per-tool descriptions do not. It therefore carries
// the load-bearing usage guidance -- above all the session-capture
// cadence -- in compressed form. Claude Code truncates instructions
// at 2KB; keep this comfortably under that with the critical content
// first.
const mcpBaseInstructions = "Gramaton is the user's persistent knowledge store: " +
	"semantic memory records, conversation session capture, and structured " +
	"collections (tasks, TODOs, checklists), plus graph links, history, and " +
	"curation. Search for and load its tools whenever the conversation " +
	"involves remembering, saving, or recalling anything beyond the current " +
	"session.\n\n" +
	"Automatic session capture is expected, not optional: call " +
	"gramaton_session_prepare then gramaton_session_save immediately when a " +
	"decision lands, a task completes, the user says done/ship it/that works, " +
	"the topic pivots, or context compaction is imminent -- and at least every " +
	"~10 substantive turns regardless. Do not batch saves at session end. If a " +
	"save response reports held promotions, resolve them promptly with " +
	"gramaton_session_resolve_held.\n\n" +
	"Retrieval: call gramaton_search before answering questions about past " +
	"decisions, project state, preferences, or prior sessions; use " +
	"gramaton_inspect for a specific record ID (ULID). Store new knowledge " +
	"with gramaton_save; revise existing records with gramaton_update. Tasks " +
	"and other must-not-miss-one items belong in collections " +
	"(gramaton_collection_add / gramaton_collection_items, which returns " +
	"every item). If unsure how any Gramaton workflow works, call " +
	"gramaton_guide."

// mcpReadOnlyInstructions is the MCP server-instructions text that
// leads the instructions string when the attached store is frozen --
// the read-only state must be the first thing a client learns about
// this server, ahead of mcpBaseInstructions.
const mcpReadOnlyInstructions = "This Gramaton store is read-only (frozen): " +
	"all writes are rejected and no write tools are registered. " +
	"Search and the other read tools work normally."

func runMCP(cmd *cobra.Command, args []string) error {
	// Ensure the HTTP server is running (auto-starts if needed). A
	// failure here is NOT fatal: exiting before the MCP handshake
	// shows the client a dead MCP server and buries the actual error
	// in a log. Proceed to serve MCP instead -- every tool call
	// re-runs serverURL(), so each call returns the real startup
	// error (config guidance included) inside the session, where the
	// agent can see it and relay the fix.
	if _, err := serverURL(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: server unavailable, serving MCP anyway (tool calls will carry the error): %v\n", err)
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
	instructions := mcpBaseInstructions
	if readOnly {
		instructions = mcpReadOnlyInstructions + "\n\n" + mcpBaseInstructions
	}
	opts := &mcp.ServerOptions{Instructions: instructions}
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
