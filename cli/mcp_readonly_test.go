package cli

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listProxyTools registers the proxy toolset in the given mode,
// connects an in-memory client, and returns the sorted tool names
// plus the session (for instructions assertions).
func listProxyTools(t *testing.T, readOnly bool) ([]string, *mcp.ClientSession) {
	t.Helper()

	mcpServer := newProxyMCPServer(readOnly)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{
		Name: "test-client", Version: "0.0.0",
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
	return names, session
}

// TestProxyToolAccessClassification is the MCP twin of the api
// layer's TestReadOnlyGuardCoversEveryAPIMethod: every tool name the
// proxy actually registers (full mode) must carry an explicit
// read/write classification in server/mcp_readonly.go. The
// classification is a single map, so "appears in exactly one of the
// two sets" is structural; the failure mode this catches is a new
// tool that was never classified -- which would stay VISIBLE on
// frozen stores if it is a write tool.
func TestProxyToolAccessClassification(t *testing.T) {
	names, _ := listProxyTools(t, false)
	if len(names) == 0 {
		t.Fatal("no proxy tools registered -- harness is broken")
	}

	var problems []string
	for _, name := range names {
		access, ok := server.MCPToolAccess(name)
		if !ok {
			problems = append(problems, name+": registered by the proxy but not classified -- decide whether ANY action it exposes reaches a guardWrite api operation (api/readonly_guard_test.go) and add it to mcpToolAccess in server/mcp_readonly.go as MCPToolWrite (mutates) or MCPToolRead (read-only)")
			continue
		}
		if access != server.MCPToolRead && access != server.MCPToolWrite {
			problems = append(problems, name+": unknown access classification "+access)
		}
	}

	if len(problems) > 0 {
		t.Errorf("MCP read-only classification tripwire hit:\n  %s\n\n"+
			"Every MCP tool must make an explicit read/write decision so frozen\n"+
			"stores hide exactly the tools they reject. See server/mcp_readonly.go.",
			strings.Join(problems, "\n  "))
	}
}

// TestProxyToolRegistryReadOnly pins the read-only surface: the
// frozen-store registration contains every read-classified tool the
// full registration has, and not a single write-classified one. The
// count is pinned so a silent shrink of the read surface (or a write
// tool slipping back in) fails loudly.
func TestProxyToolRegistryReadOnly(t *testing.T) {
	full, _ := listProxyTools(t, false)
	readOnly, _ := listProxyTools(t, true)

	// Snapshot of the read-only proxy surface (alphabetised).
	// Changing it: reclassify in server/mcp_readonly.go first, then
	// update this snapshot to match.
	want := []string{
		"gramaton_backup",
		"gramaton_collection_items",
		"gramaton_collection_list",
		"gramaton_collection_schema",
		"gramaton_diff",
		"gramaton_duplicates",
		"gramaton_explore",
		"gramaton_guide",
		"gramaton_history",
		"gramaton_history_search",
		"gramaton_inspect",
		"gramaton_jobs_list",
		"gramaton_log",
		"gramaton_pending",
		"gramaton_save_batch_cancel",
		"gramaton_save_batch_result",
		"gramaton_save_batch_status",
		"gramaton_search",
		"gramaton_session_get",
		"gramaton_stats",
		"gramaton_status",
	}
	if len(readOnly) != len(want) {
		t.Errorf("read-only proxy surface has %d tools, want %d", len(readOnly), len(want))
	}
	if missing := diffToolNames(want, readOnly); len(missing) > 0 {
		t.Errorf("read tools missing from the read-only registration: %v\n"+
			"Frozen stores must keep the full read surface.", missing)
	}
	if unexpected := diffToolNames(readOnly, want); len(unexpected) > 0 {
		t.Errorf("unexpected tools in the read-only registration: %v\n"+
			"If a tool was reclassified as read, update server/mcp_readonly.go's\n"+
			"doc comment and this snapshot together.", unexpected)
	}

	// Cross-check against the classification itself: no registered
	// write tool survives, and the read-only surface is exactly the
	// read-classified subset of the full surface.
	for _, name := range readOnly {
		if access, _ := server.MCPToolAccess(name); access == server.MCPToolWrite {
			t.Errorf("write tool %s is registered in read-only mode", name)
		}
	}
	var fullReads []string
	for _, name := range full {
		if access, _ := server.MCPToolAccess(name); access == server.MCPToolRead {
			fullReads = append(fullReads, name)
		}
	}
	if missing := diffToolNames(fullReads, readOnly); len(missing) > 0 {
		t.Errorf("read-classified tools dropped in read-only mode: %v", missing)
	}
}

// TestProxyReadOnlyInstructions pins the server-instructions
// contract: a frozen store's MCP handshake leads with the read-only
// notice; a writable store sends no such text.
func TestProxyReadOnlyInstructions(t *testing.T) {
	_, roSession := listProxyTools(t, true)
	instr := roSession.InitializeResult().Instructions
	if !strings.HasPrefix(instr, "This Gramaton store is read-only") {
		t.Errorf("read-only instructions = %q, want the read-only notice first", instr)
	}
	if !strings.Contains(instr, "writes are rejected") {
		t.Errorf("read-only instructions = %q, should say writes are unavailable", instr)
	}

	_, rwSession := listProxyTools(t, false)
	if instr := rwSession.InitializeResult().Instructions; strings.Contains(instr, "read-only") {
		t.Errorf("writable store instructions mention read-only: %q", instr)
	}
}

// TestResolveMCPReadOnly unit-tests the frozen-state resolution used
// by runMCP: the server's /v1/status envelope is authoritative in
// both directions; the local manifest is consulted only when the
// server cannot be read; and when both fail the process falls open
// to full registration (the api guards still reject writes).
func TestResolveMCPReadOnly(t *testing.T) {
	frozenEnv := func() (*server.ResponseEnvelope, error) {
		return &server.ResponseEnvelope{StoreReadonly: true}, nil
	}
	writableEnv := func() (*server.ResponseEnvelope, error) {
		return &server.ResponseEnvelope{}, nil
	}
	envErr := func() (*server.ResponseEnvelope, error) {
		return nil, errors.New("server unreachable")
	}
	frozenManifest := func() (core.StoreManifest, error) {
		return core.StoreManifest{ReadOnly: true}, nil
	}
	writableManifest := func() (core.StoreManifest, error) {
		return core.StoreManifest{}, nil
	}
	manifestErr := func() (core.StoreManifest, error) {
		return core.StoreManifest{}, errors.New("no manifest")
	}
	manifestMustNotBeRead := func(t *testing.T) func() (core.StoreManifest, error) {
		return func() (core.StoreManifest, error) {
			t.Error("manifest consulted although the server answered")
			return core.StoreManifest{}, nil
		}
	}

	t.Run("server says frozen", func(t *testing.T) {
		if !resolveMCPReadOnly(frozenEnv, manifestMustNotBeRead(t)) {
			t.Error("want frozen when the server envelope says store_readonly")
		}
	})
	t.Run("server says writable overrides a frozen manifest", func(t *testing.T) {
		// The server's engine is authoritative; a stale local manifest
		// read must not shadow it.
		if resolveMCPReadOnly(writableEnv, manifestMustNotBeRead(t)) {
			t.Error("want writable when the server envelope omits store_readonly")
		}
	})
	t.Run("server unreachable falls back to the manifest", func(t *testing.T) {
		if !resolveMCPReadOnly(envErr, frozenManifest) {
			t.Error("want frozen from the manifest fallback")
		}
		if resolveMCPReadOnly(envErr, writableManifest) {
			t.Error("want writable from the manifest fallback")
		}
	})
	t.Run("both unavailable fails open to full registration", func(t *testing.T) {
		if resolveMCPReadOnly(envErr, manifestErr) {
			t.Error("want full registration (false) when nothing can be read")
		}
	})
}
