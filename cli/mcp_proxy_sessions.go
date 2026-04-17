package cli

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSessionProxyTools(mcpServer *mcp.Server) {
	registerSessionStartProxy(mcpServer)
	registerSessionGetProxy(mcpServer)
	registerSessionPrepareProxy(mcpServer)
	registerSessionCommitProxy(mcpServer)
}

// --- session_start ---

type proxySessionStartInput struct {
	ClientSessionID string `json:"client_session_id" jsonschema:"unique session identifier from the client"`
}

func registerSessionStartProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_session_start",
		Description: "Start or resume a knowledge capture session. Creates a fresh session or returns the existing one for the same client_session_id (idempotent for --continue). No lookback to previous sessions.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxySessionStartInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/sessions", args)
	})
}

// --- session_get ---

type proxySessionGetInput struct {
	SessionID string `json:"session_id" jsonschema:"session ID to retrieve"`
}

func registerSessionGetProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_session_get",
		Description: "Get the current session state including all topics and segments.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxySessionGetInput) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyGet(fmt.Sprintf("/v1/sessions/%s", args.SessionID))
	})
}

// --- session_prepare ---

type proxySessionPrepareInput struct {
	SessionID string `json:"session_id" jsonschema:"session ID to prepare extraction for"`
}

func registerSessionPrepareProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_session_prepare",
		Description: "Prepare to extract knowledge from the current conversation. Returns extraction instructions and current session state. Call this BEFORE context compaction -- when the user mentions compacting, running low on context, or needing to compress, extract knowledge first so nothing is lost. Also call at natural breakpoints (topic changes, decisions made, task completion) or when the user asks you to capture. You must follow the returned instructions before calling gramaton_session_commit.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxySessionPrepareInput) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/prepare", args.SessionID), nil)
	})
}

// --- session_commit ---

type proxySessionCommitSegment struct {
	Content         string   `json:"content" jsonschema:"extracted knowledge (paragraph text)"`
	TopicName       string   `json:"topic" jsonschema:"topic name (existing or new)"`
	Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
	KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Keywords        []string `json:"keywords,omitempty" jsonschema:"search keywords"`
	SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"~750 chars (semantic anchor for embedding)"`
}

type proxySessionCommitInput struct {
	SessionID string                      `json:"session_id" jsonschema:"session ID to commit segments to"`
	Segments  []proxySessionCommitSegment `json:"segments" jsonschema:"array of extracted knowledge segments"`
}

func registerSessionCommitProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_session_commit",
		Description: "Submit extracted knowledge segments to the session. IMPORTANT: You must call gramaton_session_prepare first and follow its instructions. Do not call this tool directly -- the preparation step provides required context for high-quality extraction.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxySessionCommitInput) (*mcp.CallToolResult, any, error) {
		if args.SessionID == "" {
			return proxyErr("session_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/sessions/%s/commit", args.SessionID), args)
	})
}
