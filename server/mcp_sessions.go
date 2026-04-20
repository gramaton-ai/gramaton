package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPSessionTools(mcpServer *mcp.Server) {
	// --- session_start ---

	type sessionStartInput struct {
		ClientSessionID string `json:"client_session_id" jsonschema:"unique session identifier from the client (e.g. Claude Code session ID)"`
		Source          string `json:"source,omitempty" jsonschema:"startup|resume -- controls session chaining. Omit for idempotent lookup."`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_session_start",
		Description: "Start or resume a knowledge capture session. On fresh start, creates a new session. On resume (--continue), creates a new session chained to the previous one. Returns the active session.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sessionStartInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_session_start")
		defer done(nil)
		result, svcErr := s.serviceSessionCreate(args.ClientSessionID, args.Source)
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
		Description: `Extract knowledge from the ongoing conversation. Returns extraction instructions and session state. Call this EAGERLY throughout a conversation, not just at the end: immediately after a decision lands, a rule or principle is articulated, a task completes, or the user pivots topics. Also call before context compaction, and at least every ~10 substantive turns even without an explicit trigger. Bundling captures at session end is an anti-pattern -- knowledge from early in the conversation becomes harder to reconstruct as context accumulates. You must follow the returned instructions before calling gramaton_session_commit.`,
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
