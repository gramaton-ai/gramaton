package server

import (
	"context"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPIntakeTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_intake",
		Description: api.IntakeDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args api.IntakeRequest) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_intake")
		defer done(nil)
		result, svcErr := s.serviceIntake(ctx, &intakeRequest{
			Content:                args.Content,
			ContextSourceType:      args.ContextSourceType,
			ContextTimeSensitivity: args.ContextTimeSensitivity,
			ContextReliability:     args.ContextReliability,
			ContextCaptureReason:   args.ContextCaptureReason,
			ContextAbout:           args.ContextAbout,
			ContextWho:             args.ContextWho,
			ContextFindable:        args.ContextFindable,
			Keywords:               args.Keywords,
			SummaryShort:           args.SummaryShort,
			SourceRef:              args.SourceRef,
			AssertedAsOf:           args.AssertedAsOf,
			AllowSimilar:           args.AllowSimilar,
			Meta:                   args.Meta,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
