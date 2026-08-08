package server

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStatusMCPToolTakesTheEngineLock pins that the MCP status
// binding serializes behind the engine write lock -- the guard
// against a future fully lock-free rewrite of the handler. It
// cannot discriminate WHERE inside the handler the lock sits: the
// inner api.Status takes its own read lock, so this test passes as
// long as any part of the path locks. The RLock specifically around
// computeCuration rests on that function's documented caller-holds-
// the-lock contract (a concurrent restore nils the index set under
// the write lock), which has no black-box test seam.
func TestStatusMCPToolTakesTheEngineLock(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "status lock probe")

	mcpServer := srv.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{
		Name: "status-lock-test", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	eng.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{
			Name: "gramaton_status", Arguments: map[string]any{},
		})
	}()
	select {
	case <-done:
		eng.Unlock()
		t.Fatal("gramaton_status completed while the write lock was held; the status path no longer takes the engine lock")
	case <-time.After(150 * time.Millisecond):
	}
	eng.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gramaton_status never completed after the lock released")
	}
}
