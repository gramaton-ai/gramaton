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
func (s *Server) MCPHandler() http.Handler {
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: version.Version,
	}, nil)

	s.registerMCPTools(mcpServer)

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		// Allow JSON responses when client sends Accept: application/json.
		// SSE is still used when client requests text/event-stream.
		JSONResponse: true,
	})
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
	s.registerMCPGuideTools(mcpServer)
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
