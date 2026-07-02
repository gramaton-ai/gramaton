package server

import (
	"context"
	"net/http"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGuideRoutes wires the guide HTTP endpoint. Read-only and
// informational, so no loopback gate.
func (s *Server) registerGuideRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/guide", func(w http.ResponseWriter, r *http.Request) {
		resp, apiErr := s.api.Guide(r.Context(), api.GuideRequest{
			Topic: r.URL.Query().Get("topic"),
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, resp)
	})
}

// registerGuideMCPTools wires the guide MCP tool.
func (s *Server) registerGuideMCPTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_guide",
		Description: api.GuideDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.GuideRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_guide")
		defer done(nil)
		resp, apiErr := s.api.Guide(ctx, args)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(resp)
	})
}
