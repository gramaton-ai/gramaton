package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPGuideTools(mcpServer *mcp.Server) {
	type guideInput struct {
		Topic string `json:"topic,omitempty" jsonschema:"topic name: metadata, capture, search, sessions, collections, curation. Omit for topic list."`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_guide",
		Description: `Get help on how to use Gramaton effectively. Returns topical guidance for agents. Call with no topic to see available topics. Topics: metadata (epistemic fields), capture (how to store knowledge), search (retrieval patterns), sessions (extraction flow), collections (structured data), curation (background processing).`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args guideInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_guide")
		defer done(nil)
		result, svcErr := s.serviceGuide(args.Topic)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
