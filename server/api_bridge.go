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

// mcpAPIErr converts an api.APIError to the three-tuple MCP tool
// callback return type. Keeps the message but drops the structured
// fields -- MCP clients read the text content, not the status code.
func mcpAPIErr(err *api.APIError) (*mcp.CallToolResult, any, error) {
	return mcpErr(err.Message)
}
