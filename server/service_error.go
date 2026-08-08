package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serviceError is returned by service methods to carry transport-agnostic
// error information. Both HTTP handlers and MCP tools convert this to
// their respective error formats.
type serviceError struct {
	Status    int    // HTTP status code (governs severity for MCP)
	Code      string // machine-readable: "not_found", "input_error", etc.
	Message   string // human-readable
	Retryable bool
}

func (e *serviceError) Error() string { return e.Message }

func errMissing(msg string) *serviceError {
	return &serviceError{Status: http.StatusBadRequest, Code: "missing_field", Message: msg, Retryable: true}
}

func errInvalid(msg string) *serviceError {
	return &serviceError{Status: http.StatusBadRequest, Code: "input_error", Message: msg, Retryable: true}
}

func errForbidden(msg string) *serviceError {
	return &serviceError{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}

func errInternal(msg string) *serviceError {
	return &serviceError{Status: http.StatusInternalServerError, Code: "internal_error", Message: msg}
}

// writeServiceError writes a serviceError as an HTTP JSON error response.
func (s *Server) writeServiceError(w http.ResponseWriter, err *serviceError) {
	s.writeError(w, err.Status, err.Code, err.Message, err.Retryable)
}

// mcpServiceErr converts a serviceError to an MCP error result.
func mcpServiceErr(err *serviceError) (*mcp.CallToolResult, any, error) {
	return mcpErr(err.Message)
}
