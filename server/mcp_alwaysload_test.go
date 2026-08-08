package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPHotPathToolsHaveAlwaysLoadMeta asserts every tool in
// HotPathToolsAlwaysLoad (the canonical pin list in mcp.go, shared
// with the CLI proxy's mirror test) ships the
// `anthropic/alwaysLoad: true` meta flag and that no other tool
// ships it.
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

	wantPinned := make(map[string]bool, len(HotPathToolsAlwaysLoad))
	for _, name := range HotPathToolsAlwaysLoad {
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
			t.Errorf("tool %q is pinned alwaysLoad but not in HotPathToolsAlwaysLoad -- either pin was added without weighing context-budget tradeoff, or this list is stale", tool.Name)
		}
	}
}
