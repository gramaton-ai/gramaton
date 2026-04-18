package server

import (
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// writeAPIError writes an api.APIError as an HTTP JSON error response.
// Centralizes the (code, message, status, retryable) translation so
// binding closures can stay thin.
func (s *Server) writeAPIError(w http.ResponseWriter, err *api.APIError) {
	status := err.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	s.writeError(w, status, err.Code, err.Message, err.Retryable)
}

// mcpAPIErr converts an api.APIError to the MCP tool callback return
// type. The text content carries "code: message" for human readability
// in clients that only render the text block; the StructuredContent
// field carries the full APIError shape (code, message, retryable) so
// machine clients can branch without string-parsing.
func mcpAPIErr(err *api.APIError) (*mcp.CallToolResult, any, error) {
	text := err.Message
	if err.Code != "" {
		text = err.Code + ": " + err.Message
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: map[string]any{
			"code":      err.Code,
			"message":   err.Message,
			"retryable": err.Retryable,
		},
		IsError: true,
	}, nil, nil
}
