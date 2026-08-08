package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectProxy builds the proxy MCP server for the given frozen
// state and returns a connected in-memory client session.
func connectProxy(t *testing.T, readOnly bool) *mcp.ClientSession {
	t.Helper()
	mcpServer := newProxyMCPServer(readOnly)

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
	return session
}

// TestProxyHotPathToolsHaveAlwaysLoadMeta is the proxy-side twin of
// server.TestMCPHotPathToolsHaveAlwaysLoadMeta: every tool in the
// shared canonical list server.HotPathToolsAlwaysLoad must carry
// `_meta:{anthropic/alwaysLoad: true}` on the CLI proxy surface, and
// no other proxy tool may. The proxy is the surface every
// `gramaton init` install actually connects harnesses to, so a pin
// that exists only in server/bindings_*.go is invisible to real
// agent sessions -- the drift class this test exists to block.
func TestProxyHotPathToolsHaveAlwaysLoadMeta(t *testing.T) {
	session := connectProxy(t, false)

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	wantPinned := make(map[string]bool, len(server.HotPathToolsAlwaysLoad))
	for _, name := range server.HotPathToolsAlwaysLoad {
		wantPinned[name] = true
	}

	seen := 0
	for _, tool := range res.Tools {
		isPinned := false
		if v, ok := tool.Meta["anthropic/alwaysLoad"]; ok {
			if b, ok := v.(bool); ok && b {
				isPinned = true
			}
		}
		if wantPinned[tool.Name] {
			seen++
		}
		switch {
		case wantPinned[tool.Name] && !isPinned:
			t.Errorf("hot-path tool %q is missing _meta:{anthropic/alwaysLoad: true} on the proxy surface -- pin it in its cli/mcp_proxy_*.go registration", tool.Name)
		case !wantPinned[tool.Name] && isPinned:
			t.Errorf("proxy tool %q is pinned alwaysLoad but not in server.HotPathToolsAlwaysLoad -- either pin was added without weighing context-budget tradeoff, or the canonical list is stale", tool.Name)
		}
	}
	if seen != len(server.HotPathToolsAlwaysLoad) {
		t.Errorf("proxy registers %d of %d hot-path tools -- canonical list and proxy surface disagree", seen, len(server.HotPathToolsAlwaysLoad))
	}
}

// TestProxyInstructionsReachClients asserts the proxy's MCP server
// instructions -- the one channel guaranteed to reach the model when
// tool-search deferral hides per-tool descriptions -- are delivered
// in the initialize handshake, carry the session-capture cadence,
// and lead with the read-only notice on a frozen store.
func TestProxyInstructionsReachClients(t *testing.T) {
	writable := connectProxy(t, false).InitializeResult().Instructions
	if !strings.Contains(writable, "gramaton_session_prepare") {
		t.Errorf("writable instructions missing session-capture guidance: %q", writable)
	}
	if strings.Contains(writable, "read-only") {
		t.Errorf("writable instructions should not carry the frozen notice: %q", writable)
	}

	frozen := connectProxy(t, true).InitializeResult().Instructions
	if !strings.HasPrefix(frozen, "This Gramaton store is read-only") {
		t.Errorf("frozen instructions must lead with the read-only notice, got: %q", frozen)
	}
	if !strings.Contains(frozen, "gramaton_search") {
		t.Errorf("frozen instructions should retain the base retrieval guidance: %q", frozen)
	}
	// Claude Code truncates server instructions at 2KB; the longest
	// variant must fit or the tail guidance silently disappears.
	if len(frozen) > 2048 {
		t.Errorf("frozen instructions are %d bytes; must stay under 2048 to survive client truncation", len(frozen))
	}
}
