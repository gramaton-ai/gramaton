package server

import (
	"context"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listServerMCPTools connects an in-memory client to srv's MCP
// surface and returns the sorted registered tool names.
func listServerMCPTools(t *testing.T, srv *Server) []string {
	t.Helper()

	mcpServer := srv.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{
		Name: "readonly-test", Version: "0.0.0",
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
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestMCPToolAccessCoversServerSurface pins the classification's
// completeness in both directions against the server's own MCP
// surface, which is the SUPERSET of the two surfaces (the CLI proxy
// registers the same set minus gramaton_intake): every registered
// tool is classified, and every classified name is actually
// registered (no stale entries). The proxy-side twin,
// TestProxyToolAccessClassification in cli/, covers the forward
// direction for the proxy.
func TestMCPToolAccessCoversServerSurface(t *testing.T) {
	srv, _ := setupTestServer(t)
	got := listServerMCPTools(t, srv)
	if len(got) == 0 {
		t.Fatal("no MCP tools registered -- harness is broken")
	}

	registered := make(map[string]bool, len(got))
	for _, name := range got {
		registered[name] = true
		access, ok := MCPToolAccess(name)
		if !ok {
			t.Errorf("%s: registered on the server MCP surface but not classified in server/mcp_readonly.go -- decide read/write (see the file's doc comment) and add it", name)
			continue
		}
		if access != MCPToolRead && access != MCPToolWrite {
			t.Errorf("%s: unknown access classification %q", name, access)
		}
	}
	for name := range mcpToolAccess {
		if !registered[name] {
			t.Errorf("%s: classified in server/mcp_readonly.go but not registered on the server MCP surface (stale entry?)", name)
		}
	}
}

// TestMCPToolRegistryReadOnly pins the frozen-store server MCP
// surface: no write-classified tool is registered, every
// read-classified tool still is, and the writable surface keeps the
// full set (so the filter cannot pass vacuously).
func TestMCPToolRegistryReadOnly(t *testing.T) {
	frozen, _ := setupReadOnlyTestServer(t)
	got := listServerMCPTools(t, frozen)

	for _, name := range got {
		if access, _ := MCPToolAccess(name); access == MCPToolWrite {
			t.Errorf("write tool %s is registered on a read-only store", name)
		}
	}

	var wantReads []string
	for name, access := range mcpToolAccess {
		if access == MCPToolRead {
			wantReads = append(wantReads, name)
		}
	}
	sort.Strings(wantReads)
	if missing := diffSlices(wantReads, got); len(missing) > 0 {
		t.Errorf("read tools missing on a read-only store: %v -- frozen stores must keep the full read surface", missing)
	}
	if len(got) != len(wantReads) {
		t.Errorf("read-only server surface has %d tools, want %d (the read-classified set)", len(got), len(wantReads))
	}

	// Control: a writable store registers the write tools too.
	writable, _ := setupTestServer(t)
	full := listServerMCPTools(t, writable)
	if len(full) != len(mcpToolAccess) {
		t.Errorf("writable server surface has %d tools, want the full classified set of %d", len(full), len(mcpToolAccess))
	}
}
