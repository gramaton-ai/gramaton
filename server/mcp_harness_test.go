package server

import (
	"context"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPToolRegistry is the foundation of the MCP test harness (T-03).
// It boots a server, registers every MCP tool, lists them via an
// in-memory client, and asserts the registered set matches the expected
// snapshot. Adding or removing a tool intentionally requires updating
// the snapshot here -- which makes accidental drops visible in PR
// review and forces conscious additions.
//
// Until T-02 (shared api/ package) lands, drift between MCP input
// schemas and HTTP service inputs is still possible at the field level.
// This test catches drift at the tool level (presence/absence) only.
func TestMCPToolRegistry(t *testing.T) {
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

	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
	}
	sort.Strings(got)

	// Snapshot of registered tools as of the T-03 baseline.
	// Adding a new MCP tool: append to this list AND ensure the tool
	// is registered in server/mcp.go's registerMCPTools.
	// Removing a tool: drop from this list AND remove the registration.
	// Drift caught: a registration that was deleted but expected here
	// fires "missing"; a registration added without updating this list
	// fires "unexpected".
	want := []string{
		"gramaton_backup",
		"gramaton_branch",
		"gramaton_capture",
		"gramaton_classify",
		"gramaton_collection_add",
		"gramaton_collection_add_batch",
		"gramaton_collection_create",
		"gramaton_collection_delete",
		"gramaton_collection_items",
		"gramaton_collection_list",
		"gramaton_collection_migrate",
		"gramaton_collection_move",
		"gramaton_collection_remove",
		"gramaton_collection_rename",
		"gramaton_collection_schema",
		"gramaton_collection_update",
		"gramaton_curation",
		"gramaton_diff",
		"gramaton_duplicates",
		"gramaton_explore",
		"gramaton_guide",
		"gramaton_history",
		"gramaton_inspect",
		"gramaton_intake",
		"gramaton_link",
		"gramaton_log",
		"gramaton_pending",
		"gramaton_reembed",
		"gramaton_resolve",
		"gramaton_search",
		"gramaton_session_commit",
		"gramaton_session_get",
		"gramaton_session_prepare",
		"gramaton_session_start",
		"gramaton_stats",
		"gramaton_status",
		"gramaton_unlink",
		"gramaton_update",
	}

	missing := diffSlices(want, got)
	unexpected := diffSlices(got, want)

	if len(missing) > 0 {
		t.Errorf("MCP tools missing from registration (expected but not found): %v\n"+
			"If you intentionally removed a tool, drop it from the want list above.",
			missing)
	}
	if len(unexpected) > 0 {
		t.Errorf("MCP tools found that are not in the snapshot: %v\n"+
			"If you intentionally added a tool, add it to the want list above (alphabetised).\n"+
			"Also verify: shared input struct vs server/mcp_*.go vs cli/mcp_proxy*.go.",
			unexpected)
	}
}

// TestMCPToolNamesAreSnakeCase asserts every registered MCP tool name
// matches the gramaton_<verb> convention. Catches typos and accidental
// camelCase that would break agent ergonomics.
func TestMCPToolNamesAreSnakeCase(t *testing.T) {
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

	for _, tool := range res.Tools {
		name := tool.Name
		if len(name) < len("gramaton_") || name[:len("gramaton_")] != "gramaton_" {
			t.Errorf("tool %q does not start with gramaton_ prefix", name)
			continue
		}
		for _, r := range name {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				t.Errorf("tool %q contains non-snake-case character %q", name, r)
				break
			}
		}
	}
}

// diffSlices returns elements present in a but not b.
func diffSlices(a, b []string) []string {
	bset := make(map[string]struct{}, len(b))
	for _, s := range b {
		bset[s] = struct{}{}
	}
	var missing []string
	for _, s := range a {
		if _, ok := bset[s]; !ok {
			missing = append(missing, s)
		}
	}
	return missing
}
