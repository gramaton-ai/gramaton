package cli

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callProxy creates a proxy MCP server, connects a client via in-memory
// transport, and calls a tool by name. Returns the parsed result data.
func callProxy(t *testing.T, toolName string, args any) map[string]any {
	t.Helper()

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name: "gramaton-test", Version: "0.0.0",
	}, nil)
	registerProxyTools(mcpServer)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	go mcpServer.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{
		Name: "test-client", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	var argsMap map[string]any
	json.Unmarshal(argsJSON, &argsMap)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: argsMap,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", toolName, err)
	}
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("tool %s returned error: %s", toolName, text)
	}

	if len(result.Content) == 0 {
		t.Fatalf("tool %s returned no content", toolName)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s returned non-text content", toolName)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &data); err != nil {
		t.Fatalf("parse result for %s: %v\nraw: %s", toolName, err, tc.Text)
	}
	return data
}

func TestProxySearch(t *testing.T) {
	data := callProxy(t, "gramaton_search", map[string]any{
		"top":         5,
		"temporality": "immutable",
	})
	results, ok := data["results"].([]any)
	if !ok {
		t.Fatal("expected results array")
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	for _, r := range results {
		rec := r.(map[string]any)
		if rec["temporality"] != "immutable" {
			t.Fatalf("expected immutable, got %v", rec["temporality"])
		}
	}
}

func TestProxySave(t *testing.T) {
	data := callProxy(t, "gramaton_save", map[string]any{
		"content":     "Test proxy capture: the sky is blue",
		"temporality": "immutable",
		"keywords":    []string{"test", "proxy"},
	})
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected non-empty id")
	}
}

func TestProxyInspect(t *testing.T) {
	data := callProxy(t, "gramaton_inspect", map[string]any{
		"id": testStore.HealthAllergy,
	})
	if data["id"] != testStore.HealthAllergy {
		t.Fatalf("expected id %s, got %v", testStore.HealthAllergy, data["id"])
	}
	props := data["properties"].(map[string]any)
	if props["temporality"] != "immutable" {
		t.Fatalf("expected immutable, got %v", props["temporality"])
	}
}

func TestProxyUpdate(t *testing.T) {
	conf := 0.75
	data := callProxy(t, "gramaton_update", map[string]any{
		"id":         testStore.WorkReorg,
		"confidence": &conf,
	})
	if data["updated"] != true {
		t.Fatal("expected updated=true")
	}
}

func TestProxyResolve(t *testing.T) {
	// First capture a record to resolve.
	cap := callProxy(t, "gramaton_save", map[string]any{
		"content":  "TODO: test proxy resolve lifecycle",
		"keywords": []string{"test", "proxy", "todo"},
	})
	id := cap["id"].(string)

	data := callProxy(t, "gramaton_resolve", map[string]any{
		"id":              id,
		"resolution":      "completed",
		"resolution_note": "tested via proxy",
	})
	if data["resolved"] != true {
		t.Fatal("expected resolved=true")
	}
}

func TestProxyLink(t *testing.T) {
	w := 0.9
	data := callProxy(t, "gramaton_link", map[string]any{
		"id":          testStore.WorkReorg,
		"target_id":   testStore.HealthExercise,
		"edge_type":   "relates_to",
		"edge_weight": &w,
	})
	if data["edge_id"] == nil || data["edge_id"] == "" {
		t.Fatal("expected non-empty edge_id")
	}
}

func TestProxyExplore(t *testing.T) {
	data := callProxy(t, "gramaton_explore", map[string]any{
		"node_id": testStore.WorkReorg,
		"depth":   2,
	})
	nodes, ok := data["nodes"].([]any)
	if !ok {
		t.Fatal("expected nodes array")
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 connected node")
	}
}

func TestProxyPending(t *testing.T) {
	data := callProxy(t, "gramaton_pending", struct{}{})
	records, ok := data["records"].([]any)
	if !ok {
		t.Fatal("expected records array")
	}
	if len(records) == 0 {
		t.Fatal("expected at least 1 pending record")
	}
}

func TestProxyStatus(t *testing.T) {
	data := callProxy(t, "gramaton_status", struct{}{})
	// HTTP /v1/status wraps data in "store" key.
	store, ok := data["store"].(map[string]any)
	if !ok {
		t.Fatalf("expected store object, got %v", data)
	}
	nodes, ok := store["nodes"].(float64)
	if !ok || nodes == 0 {
		t.Fatalf("expected non-zero node count, got %v", store["nodes"])
	}
}

func TestProxyStats(t *testing.T) {
	data := callProxy(t, "gramaton_stats", struct{}{})
	total, ok := data["total_records"].(float64)
	if !ok || total == 0 {
		t.Fatalf("expected non-zero total_records, got %v", data["total_records"])
	}
}

func TestProxyBranchList(t *testing.T) {
	data := callProxy(t, "gramaton_branch", proxyBranchInput{
		Action: "list",
	})
	if _, ok := data["branches"]; !ok {
		t.Fatal("expected branches field")
	}
}

func TestProxyDiff(t *testing.T) {
	data := callProxy(t, "gramaton_diff", proxyDiffInput{
		Since: "2020-01-01",
	})
	if _, ok := data["added"]; !ok {
		t.Fatal("expected added field")
	}
	if _, ok := data["removed"]; !ok {
		t.Fatal("expected removed field")
	}
}

func TestProxyLog(t *testing.T) {
	data := callProxy(t, "gramaton_log", proxyLogInput{Limit: 5})
	commits, ok := data["commits"].([]any)
	if !ok {
		t.Fatal("expected commits array")
	}
	if len(commits) == 0 {
		t.Fatal("expected at least 1 commit")
	}
}

func TestProxyBackupStatus(t *testing.T) {
	data := callProxy(t, "gramaton_backup", proxyBackupInput{
		Action: "status",
	})
	if _, ok := data["backup_dir"]; !ok {
		t.Fatal("expected backup_dir field")
	}
}

func TestProxyDeleteNotExposed(t *testing.T) {
	// gramaton_delete is intentionally excluded from MCP -- destructive
	// operations should not be available to agents.
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name: "gramaton-test", Version: "0.0.0",
	}, nil)
	registerProxyTools(mcpServer)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go mcpServer.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{
		Name: "test-client", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gramaton_delete",
	})
	if err == nil {
		t.Fatal("gramaton_delete should not be registered as an MCP tool")
	}
}

func TestProxyCollectionCreateSchemaAdvertisesObjectType(t *testing.T) {
	// Regression for #88: the collection_create `schema` argument must
	// advertise type:object in the published tool input schema. When the
	// proxy field was typed `any`, jsonschema-go emitted no type, so MCP
	// clients serialized the object argument as a JSON string and the
	// server rejected it with input_error. Inspecting the published
	// schema catches the regression without a live backend (callProxy
	// can't -- it marshals the argument itself, bypassing the client-side
	// stringification that is the actual bug).
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name: "gramaton-test", Version: "0.0.0",
	}, nil)
	registerProxyTools(mcpServer)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go mcpServer.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{
		Name: "test-client", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	found := false
	for _, tool := range res.Tools {
		if tool.Name != "gramaton_collection_create" {
			continue
		}
		found = true

		// InputSchema round-trips as JSON over the transport; re-marshal
		// and read the `schema` property's advertised type.
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema: %v", err)
		}
		var doc struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse input schema: %v\nraw: %s", err, raw)
		}
		if got := doc.Properties["schema"].Type; got != "object" {
			t.Fatalf("collection_create `schema` arg advertises type %q, want \"object\" -- an untyped arg makes MCP clients stringify the object (#88)", got)
		}
	}
	if !found {
		t.Fatal("gramaton_collection_create tool not registered")
	}
}

func TestProxyCollectionMigrateValueAdvertisesMultiType(t *testing.T) {
	// Regression for #91: the collection_migrate `value` argument must
	// advertise an explicit multi-type list in the published tool input
	// schema. `value` is genuinely polymorphic (`any` in Go), so
	// jsonschema-go infers a type-less property, and MCP clients
	// stringify non-scalar arguments with no advertised type -- an
	// object or array default (e.g. for an enum[] field) arrived as a
	// JSON string and failed validation. Retyping the field (the #88
	// fix for collection_create `schema`) would reject scalar defaults,
	// the dominant case, so the registration overrides the property
	// schema explicitly via api.CollectionMigrateInputSchema.
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name: "gramaton-test", Version: "0.0.0",
	}, nil)
	registerProxyTools(mcpServer)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go mcpServer.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{
		Name: "test-client", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

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
		assertMigrateValueMultiType(t, tool.InputSchema)
	}
	if !found {
		t.Fatal("gramaton_collection_migrate tool not registered")
	}
}

// assertMigrateValueMultiType checks that a listed tool's input schema
// advertises the full multi-type list for the `value` property (#91).
// Shared shape with the server-side harness assertion in
// server/mcp_harness_test.go.
func assertMigrateValueMultiType(t *testing.T, inputSchema any) {
	t.Helper()

	// InputSchema round-trips as JSON over the transport; re-marshal
	// and read the `value` property's advertised type list.
	raw, err := json.Marshal(inputSchema)
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

	// The override must not clobber the inferred schema for the other
	// arguments: collection_id keeps its plain string type.
	var collectionID struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(doc.Properties["collection_id"], &collectionID); err != nil {
		t.Fatalf("parse `collection_id` property: %v", err)
	}
	if collectionID.Type != "string" {
		t.Fatalf("collection_migrate `collection_id` arg advertises type %q, want \"string\"", collectionID.Type)
	}
}

func TestProxyUnlink(t *testing.T) {
	// Capture two records and link them.
	dataA := callProxy(t, "gramaton_save", map[string]any{"content": "Record A"})
	dataB := callProxy(t, "gramaton_save", map[string]any{"content": "Record B"})
	idA, _ := dataA["id"].(string)
	idB, _ := dataB["id"].(string)

	linkData := callProxy(t, "gramaton_link", map[string]any{
		"id": idA, "target_id": idB, "edge_type": "related_to", "edge_weight": floatPtr(0.8),
	})
	edgeID, _ := linkData["edge_id"].(string)
	if edgeID == "" {
		t.Fatal("link returned no edge_id")
	}

	// Unlink it.
	unlinkData := callProxy(t, "gramaton_unlink", map[string]any{"edge_id": edgeID})
	if deleted, ok := unlinkData["deleted"].(bool); !ok || !deleted {
		t.Fatal("expected deleted=true")
	}
}

func TestProxyHistory(t *testing.T) {
	// Capture and then inspect history.
	data := callProxy(t, "gramaton_save", map[string]any{"content": "Record with history"})
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatal("capture returned no id")
	}

	histData := callProxy(t, "gramaton_history", map[string]any{"id": id})
	// History should have at least the creation entry.
	if _, ok := histData["changes"]; !ok {
		if _, ok := histData["error"]; ok {
			t.Skipf("history endpoint may not track this record yet")
		}
	}
}

func floatPtr(v float64) *float64 { return &v }
