package server

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStatusMCPToolDuringRestore exercises gramaton_status against a
// concurrent BackupRestore, which closes and reopens the engine's
// file-backed resources (including the property index computeCuration
// reads for the pending count) under the engine write lock.
//
// Before the fix, the gramaton_status handler called computeCuration
// with no lock at all. Engine.PropIdx() dereferences e.indexes, and
// Restore's CloseFiles nils that field out before OpenFiles reassigns
// it -- an unlocked reader landing in that window doesn't just see a
// stale count, it can dereference a nil pointer. A concurrent status
// call must block behind the write lock (or run before/after it
// entirely), never observe the mid-swap state.
func TestStatusMCPToolDuringRestore(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "pre-restore record")

	w := doRequest(t, srv, "POST", "/v1/backup", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("backup: %d %s", w.Code, w.Body.String())
	}
	archivePath := parseResponse(t, w)["data"].(map[string]any)["path"].(string)

	mcpServer := srv.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{
		Name: "status-restore-race-test", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		w2 := doRequest(t, srv, "POST", "/v1/restore", map[string]any{
			"path":  archivePath,
			"force": true,
		})
		if w2.Code != http.StatusOK {
			t.Errorf("restore: %d %s", w2.Code, w2.Body.String())
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			// A transient tool-level error during the restore window is
			// tolerated -- what this test guards against is a crash (a
			// nil-pointer panic in the unlocked pre-fix handler takes
			// down the whole process rather than landing here as a
			// clean error).
			_, _ = session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "gramaton_status",
				Arguments: map[string]any{},
			})
		}
	}()

	wg.Wait()
}
