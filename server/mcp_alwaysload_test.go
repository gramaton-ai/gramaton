package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hotPathToolsAlwaysLoad enumerates the MCP tools we expect to be
// pinned via `_meta: {"anthropic/alwaysLoad": true}` so Claude Code's
// tool-search deferral doesn't trip up first-call latency on the
// agent's most-used surface.
//
// Adding to this list: pin a new tool via mcpAlwaysLoadMeta() in its
// registration. Removing: drop both the helper call and the entry
// here. The list pairs with the deferred-by-default rest of the
// surface (~24 tools) which stays unpinned.
var hotPathToolsAlwaysLoad = []string{
	"gramaton_capture",
	"gramaton_collection_add",
	"gramaton_collection_items",
	"gramaton_collection_list",
	"gramaton_collection_update",
	"gramaton_curation",
	"gramaton_inspect",
	"gramaton_link",
	"gramaton_resolve",
	"gramaton_search",
	"gramaton_session_commit",
	"gramaton_session_prepare",
}

// TestMCPHotPathToolsHaveAlwaysLoadMeta asserts every tool in
// hotPathToolsAlwaysLoad ships the `anthropic/alwaysLoad: true` meta
// flag and that no other tool ships it.
//
// The asymmetric check (must-have-it for hot path; must-NOT-have-it
// for the rest) catches both dropped pins (e.g., a refactor that
// nukes Meta on a hot-path tool) and creep (e.g., a new tool quietly
// pins itself without weighing the context-budget tradeoff).
func TestMCPHotPathToolsHaveAlwaysLoadMeta(t *testing.T) {
	srv, _ := setupTestServer(t)

	mcpServer := srv.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{
		Name: "harness-test", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	wantPinned := make(map[string]bool, len(hotPathToolsAlwaysLoad))
	for _, name := range hotPathToolsAlwaysLoad {
		wantPinned[name] = true
	}

	for _, tool := range res.Tools {
		isPinned := false
		if v, ok := tool.Meta["anthropic/alwaysLoad"]; ok {
			if b, ok := v.(bool); ok && b {
				isPinned = true
			}
		}

		shouldBePinned := wantPinned[tool.Name]
		switch {
		case shouldBePinned && !isPinned:
			t.Errorf("hot-path tool %q is missing _meta:{anthropic/alwaysLoad: true} -- did its registration drop the Meta field?", tool.Name)
		case !shouldBePinned && isPinned:
			t.Errorf("tool %q is pinned alwaysLoad but not in hotPathToolsAlwaysLoad -- either pin was added without weighing context-budget tradeoff, or this list is stale", tool.Name)
		}
	}
}
