package cli

import (
	"context"
	"encoding/json"
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
	data := callProxy(t, "gramaton_search", proxySearchInput{
		Top:         5,
		Temporality: "immutable",
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

func TestProxyCapture(t *testing.T) {
	data := callProxy(t, "gramaton_capture", proxyCaptureInput{
		Content:     "Test proxy capture: the sky is blue",
		Temporality: "immutable",
		Keywords:    []string{"test", "proxy"},
	})
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected non-empty id")
	}
}

func TestProxyInspect(t *testing.T) {
	data := callProxy(t, "gramaton_inspect", proxyInspectInput{
		ID: testStore.HealthAllergy,
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
	data := callProxy(t, "gramaton_update", proxyUpdateInput{
		ID:         testStore.WorkReorg,
		Confidence: &conf,
	})
	if data["updated"] != true {
		t.Fatal("expected updated=true")
	}
}

func TestProxyResolve(t *testing.T) {
	// First capture a record to resolve.
	cap := callProxy(t, "gramaton_capture", proxyCaptureInput{
		Content:  "TODO: test proxy resolve lifecycle",
		Keywords: []string{"test", "proxy", "todo"},
	})
	id := cap["id"].(string)

	data := callProxy(t, "gramaton_resolve", proxyResolveInput{
		ID:             id,
		Resolution:     "completed",
		ResolutionNote: "tested via proxy",
	})
	if data["resolved"] != true {
		t.Fatal("expected resolved=true")
	}
}

func TestProxyLink(t *testing.T) {
	w := 0.9
	data := callProxy(t, "gramaton_link", proxyLinkInput{
		ID:         testStore.WorkReorg,
		TargetID:   testStore.HealthExercise,
		EdgeType:   "relates_to",
		EdgeWeight: &w,
	})
	if data["edge_id"] == nil || data["edge_id"] == "" {
		t.Fatal("expected non-empty edge_id")
	}
}

func TestProxyExplore(t *testing.T) {
	data := callProxy(t, "gramaton_explore", proxyExploreInput{
		NodeID: testStore.WorkReorg,
		Depth:  2,
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
