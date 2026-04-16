package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPSessionTools(mcpServer *mcp.Server) {
	// --- session_start ---

	type sessionStartInput struct {
		ClientSessionID string `json:"client_session_id" jsonschema:"unique session identifier from the client (e.g. Claude Code session ID)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_start",
		Description: "Start or resume a knowledge capture session. Creates a fresh session or returns the existing one for the same client_session_id (idempotent for --continue). No lookback to previous sessions.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sessionStartInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_start")
		defer done(nil)
		result, svcErr := s.serviceSessionCreate(args.ClientSessionID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- session_get ---

	type sessionGetInput struct {
		SessionID string `json:"session_id" jsonschema:"session ID to retrieve"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_get",
		Description: "Get the current session state including all topics and segments. Use to review what has been captured so far.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sessionGetInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_get")
		defer done(nil)
		result, svcErr := s.serviceSessionGet(args.SessionID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- session_prepare ---

	type sessionPrepareInput struct {
		SessionID string `json:"session_id" jsonschema:"session ID to prepare extraction for"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_session_prepare",
		Description: `Prepare to extract knowledge from the current conversation. Returns extraction instructions and current session state. Call this BEFORE context compaction -- when the user mentions compacting, running low on context, or needing to compress, extract knowledge first so nothing is lost. Also call at natural breakpoints (topic changes, decisions made, task completion) or when the user asks you to capture. You must follow the returned instructions before calling gramaton_session_commit.`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sessionPrepareInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_prepare")
		defer done(nil)
		result, svcErr := s.serviceSessionPrepare(args.SessionID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- session_commit ---

	type sessionCommitInput struct {
		SessionID string          `json:"session_id" jsonschema:"session ID to commit segments to"`
		Segments  []commitSegment `json:"segments" jsonschema:"array of extracted knowledge segments"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_session_commit",
		Description: `Submit extracted knowledge segments to the session. IMPORTANT: You must call gramaton_session_prepare first and follow its instructions. Do not call this tool directly -- the preparation step provides required context for high-quality extraction.`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sessionCommitInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_commit")
		defer done(nil)
		result, svcErr := s.serviceSessionCommit(args.SessionID, args.Segments)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
