package server

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPToolRegistry is the foundation of the MCP test harness.
// It boots a server, registers every MCP tool, lists them via an
// in-memory client, and asserts the registered set matches the expected
// snapshot. Adding or removing a tool intentionally requires updating
// the snapshot here -- which makes accidental drops visible in PR
// review and forces conscious additions.
//
// Drift between MCP input schemas and HTTP service inputs is still
// possible at the field level. This test catches drift at the tool
// level (presence/absence) only.
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

	// Snapshot of registered tools.
	// Adding a new MCP tool: append to this list AND ensure the tool
	// is registered in server/mcp.go's registerMCPTools.
	// Removing a tool: drop from this list AND remove the registration.
	// Drift caught: a registration that was deleted but expected here
	// fires "missing"; a registration added without updating this list
	// fires "unexpected".
	want := []string{
		"gramaton_backup",
		"gramaton_branch",
		"gramaton_save",
		"gramaton_save_batch",
		"gramaton_save_batch_cancel",
		"gramaton_save_batch_result",
		"gramaton_save_batch_status",
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
		"gramaton_history_search",
		"gramaton_inspect",
		"gramaton_intake",
		"gramaton_jobs_list",
		"gramaton_link",
		"gramaton_log",
		"gramaton_pending",
		"gramaton_reembed",
		"gramaton_resolve",
		"gramaton_search",
		"gramaton_session_save",
		"gramaton_session_resolve_held",
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

// TestMCPCollectionMigrateValueAdvertisesMultiType is the server-side
// twin of the CLI proxy assertion in cli/mcp_proxy_test.go. Regression
// for #91: the collection_migrate `value` argument is `any` in Go
// (a migration default is genuinely polymorphic), jsonschema inference
// leaves it type-less, and MCP clients stringify arguments with no
// advertised type -- so object and array defaults (e.g. an enum[]
// field default) arrived as JSON strings and failed validation. Both
// transports build the tool's input schema via
// api.CollectionMigrateInputSchema, which overrides `value` with an
// explicit multi-type list; this asserts the override survives to the
// published schema.
func TestMCPCollectionMigrateValueAdvertisesMultiType(t *testing.T) {
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

	found := false
	for _, tool := range res.Tools {
		if tool.Name != "gramaton_collection_migrate" {
			continue
		}
		found = true

		// InputSchema round-trips as JSON over the transport;
		// re-marshal and read the `value` property's advertised types.
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema: %v", err)
		}
		var doc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse input schema: %v\nraw: %s", err, raw)
		}
		var value struct {
			Types       []string `json:"type"`
			Description string   `json:"description"`
		}
		if err := json.Unmarshal(doc.Properties["value"], &value); err != nil {
			t.Fatalf("parse `value` property: %v\nraw: %s", err, doc.Properties["value"])
		}
		want := []string{"string", "number", "boolean", "array", "object", "null"}
		if !slices.Equal(value.Types, want) {
			t.Fatalf("collection_migrate `value` arg advertises type %v, want %v -- a type-less arg makes MCP clients stringify non-scalars (#91)", value.Types, want)
		}
		if value.Description == "" {
			t.Fatal("collection_migrate `value` arg has empty description")
		}
	}
	if !found {
		t.Fatal("gramaton_collection_migrate tool not registered")
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
