package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gramaton-ai/gramaton/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHandler returns an HTTP handler that serves the MCP protocol
// via Streamable HTTP transport.
//
// Two servers are built once. Loopback callers (and authenticated
// remotes on an admin_ops server) get the full surface; other
// authenticated remotes get a trimmed surface with the path-taking
// tools removed, so a remote agent -- or a hijacked one -- cannot
// drive host-path operations even though it authenticated. The
// per-request selector picks between them; the global auth
// middleware has already rejected unauthenticated remotes before
// the request reaches here.
func (s *Server) MCPHandler() http.Handler {
	full := mcp.NewServer(&mcp.Implementation{Name: "gramaton", Version: version.Version}, nil)
	s.registerMCPTools(full)

	remote := mcp.NewServer(&mcp.Implementation{Name: "gramaton", Version: version.Version}, nil)
	s.registerMCPTools(remote)
	remote.RemoveTools(MCPRemoteExcludedToolNames()...)

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if s.mcpFullSurfaceAllowed(r) {
			return full
		}
		return remote
	}, &mcp.StreamableHTTPOptions{
		// Allow JSON responses when client sends Accept: application/json.
		// SSE is still used when client requests text/event-stream.
		JSONResponse: true,
	})
}

// mcpFullSurfaceAllowed decides which MCP tool surface a caller
// sees. It mirrors adminAllowed: loopback and admin_ops-authorized
// remotes get every tool; a plain authenticated remote gets the
// trimmed surface.
func (s *Server) mcpFullSurfaceAllowed(r *http.Request) bool {
	return s.adminAllowed(r)
}

// MCPServer returns a configured MCP server for use with stdio transport.
func (s *Server) MCPServer() *mcp.Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: version.Version,
	}, nil)

	s.registerMCPTools(mcpServer)
	return mcpServer
}

func (s *Server) registerMCPTools(mcpServer *mcp.Server) {
	// Records cluster: bindings_records.go (api-typed).
	s.registerRecordsMCPTools(mcpServer)
	// Search + ops cluster: bindings_search.go (api-typed).
	// Covers gramaton_search, explore, duplicates, pending, stats, status.
	s.registerSearchMCPTools(mcpServer)
	s.registerMCPIntakeTools(mcpServer)
	s.registerMaintenanceMCPTools(mcpServer)
	s.registerHistoryMCPTools(mcpServer)
	s.registerAdminMCPTools(mcpServer)
	// Collections cluster: bindings_collections.go (api-typed).
	s.registerCollectionsMCPTools(mcpServer)
	// Sessions cluster: bindings_sessions.go (api-typed).
	s.registerSessionsMCPTools(mcpServer)
	// Guide cluster: bindings_guide.go (api-typed).
	s.registerGuideMCPTools(mcpServer)

	// Store-level read-only: strip every write-classified tool (see
	// mcp_readonly.go) so an agent attached to a frozen store never
	// sees a tool it cannot use. Registration-time state is stable
	// because freeze/thaw refuse to run while a server is up; the one
	// runtime flip (restoring a frozen backup into a live writable
	// server) leaves stale write tools registered, which the
	// api-layer guards still reject with code "forbidden".
	if s.engine.ReadOnly() {
		mcpServer.RemoveTools(MCPWriteToolNames()...)
	}
}

// MCPAlwaysLoadMeta returns the `_meta` payload that pins an MCP
// tool as always-loaded for clients that implement tool-search
// deferral (Claude Code v2.1.121+). With tool search on (the default
// in current Claude Code), tool schemas are deferred until the agent
// calls ToolSearch to fetch them; pinning saves that round-trip on
// hot-path tools the agent uses on most substantive sessions.
//
// Forward-compatible: clients that don't implement tool search (or
// haven't shipped the per-tool alwaysLoad feature) ignore the
// metadata and behave as before.
//
// Exported because the pin must ride BOTH registration surfaces: the
// server's own MCP endpoints here and the CLI stdio proxy
// (cli/mcp_proxy_*.go) that `gramaton init` wires every harness to.
// A pin that exists only server-side never reaches a real agent
// session.
//
// We pin a curated subset (search/inspect/save/session_*/
// collection_{add,items,update,list}/resolve/link/curation) rather
// than every tool to avoid bloating the system prompt with infrequent
// administrative or diagnostic tools (branch, backup, reembed,
// classify, log, etc.); those stay deferred and load on demand.
func MCPAlwaysLoadMeta() mcp.Meta {
	return mcp.Meta{"anthropic/alwaysLoad": true}
}

// HotPathToolsAlwaysLoad returns the canonical list of MCP tools
// pinned via MCPAlwaysLoadMeta. Both registration surfaces (server
// bindings and CLI proxy) must pin exactly this set; the alwaysload
// tests in server/ and cli/ each assert their surface against this
// one list, which is what keeps the two from drifting. A function
// returning a fresh slice (the MCPWriteToolNames pattern) rather
// than an exported var, so no importer can mutate the invariant the
// tests check.
//
// Adding to this list: pin the tool via MCPAlwaysLoadMeta() in BOTH
// registrations. Removing: drop both helper calls and the entry here.
func HotPathToolsAlwaysLoad() []string {
	return []string{
		"gramaton_save",
		"gramaton_collection_add",
		"gramaton_collection_items",
		"gramaton_collection_list",
		"gramaton_collection_update",
		"gramaton_curation",
		"gramaton_inspect",
		"gramaton_link",
		"gramaton_resolve",
		"gramaton_search",
		"gramaton_session_save",
		"gramaton_session_prepare",
	}
}

// mcpToolStart records the start of an MCP tool call and returns a
// function that logs the completion. Usage:
//
//	done := s.mcpToolStart("gramaton_search")
//	defer done(err)
func (s *Server) mcpToolStart(tool string) func(error) {
	start := time.Now()
	return func(err error) {
		dur := time.Since(start)
		if err != nil {
			s.log.Warn("mcp tool error",
				"component", "mcp",
				"tool", tool,
				"duration_ms", dur.Milliseconds(),
				"err", err)
		} else {
			s.log.Info("mcp tool",
				"component", "mcp",
				"tool", tool,
				"duration_ms", dur.Milliseconds())
		}
	}
}

// mcpErr returns an MCP tool result indicating an error.
func mcpErr(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

// mcpJSONResult converts a value to a TextContent MCP result.
func mcpJSONResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpErr("failed to marshal result")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil, nil
}
