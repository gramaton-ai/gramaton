package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIntakeDeliberateSave(t *testing.T) {
	srv, eng := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"content":                "We decided to use PostgreSQL for the main database",
		"context_capture_reason": "recording architecture decision",
		"context_source_type":    "team discussion",
		"keywords":               []string{"postgresql", "database", "architecture"},
		"summary_short":          "Chose PostgreSQL for main database",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	resp := parseResponse(t, w)
	data := resp["data"].(map[string]any)
	if data["route"] != "knowledge" {
		t.Fatalf("expected route=knowledge, got %v", data["route"])
	}
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected non-empty id in response")
	}

	// Verify the record exists with context signals stored.
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("record not found after intake")
	}
	if v, _ := n.Properties.GetString("context_capture_reason"); v != "recording architecture decision" {
		t.Fatalf("context_capture_reason not stored: %q", v)
	}
	if v, _ := n.Properties.GetString("context_source_type"); v != "team discussion" {
		t.Fatalf("context_source_type not stored: %q", v)
	}
}

func TestIntakeObservedModeRetired(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"mode":    "observed",
		"content": "anything",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf(`mode="observed" should be rejected after retirement, got %d: %s`, w.Code, w.Body.String())
	}
}

func TestIntakeRequiresContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty intake, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntakeSanitizesShortFields: the intake validator runs the same
// tool-use-leakage sanitizer as the api-layer save path. Dirty input
// in, clean values stored -- a length-only check would store the
// contaminated original verbatim.
func TestIntakeSanitizesShortFields(t *testing.T) {
	srv, eng := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"content":       "sanitize pin: intake must clean short fields",
		"summary_short": "clean summary text</summary_short>\n<parameter name=\"keywords\">[\"leak\"]",
		"context_about": "domain notes<|im_end|>trailing garbage",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseResponse(t, w)
	id := resp["data"].(map[string]any)["id"].(string)

	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatal("record not found after intake")
	}
	if v, _ := n.Properties.GetString("content_short"); v != "clean summary text" {
		t.Errorf("content_short = %q, want the sanitized value", v)
	}
	if v, _ := n.Properties.GetString("context_about"); v != "domain notes" {
		t.Errorf("context_about = %q, want the sanitized value", v)
	}
}

// TestIntakeRejectsMalformedDate: a malformed asserted_as_of is a 400,
// not a record quietly stored without the claim date the caller
// thought they attached (setOptionalProps drops unparseable
// timestamps on the floor).
func TestIntakeRejectsMalformedDate(t *testing.T) {
	srv, eng := setupTestServer(t)
	before := eng.NodeCount()
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"content":        "date pin: malformed asserted_as_of must reject",
		"asserted_as_of": "last tuesday",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed date, got %d: %s", w.Code, w.Body.String())
	}
	if got := eng.NodeCount(); got != before {
		t.Fatalf("record created despite rejection: node count %d, want %d", got, before)
	}
}

// TestIntakeIndexesKeywordsForBM25: intake shares save's indexing
// contract -- caller keywords are BM25 terms from the moment the
// record lands, not only after an index rebuild.
func TestIntakeIndexesKeywordsForBM25(t *testing.T) {
	srv, eng := setupTestServer(t)
	w := doRequest(t, srv, "POST", "/v1/intake", map[string]any{
		"content":  "a note about deployment cadence",
		"keywords": []string{"quartzgreen"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := parseResponse(t, w)["data"].(map[string]any)["id"].(string)

	hits := eng.BM25Full().Search([]string{"quartzgreen"}, 5, nil)
	if len(hits) != 1 || hits[0].NodeID != id {
		t.Fatalf("BM25 search on an intake keyword = %+v, want the record %s", hits, id)
	}
}

// intakeDedupEmbedder returns the same vector for identical input
// text, so a second intake of the same content deterministically
// triggers the save-guard hold (the server-package twin of the api
// test suite's dedupEmbedder).
type intakeDedupEmbedder struct{ dim int }

func (e *intakeDedupEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = make([]float32, e.dim)
		var h uint32 = 2166136261
		for _, c := range []byte(t) {
			h ^= uint32(c)
			h *= 16777619
		}
		for j := range out[i] {
			h = h*16777619 + uint32(j)
			out[i][j] = float32(h%101) / 100.0
		}
	}
	return out, nil
}

func (e *intakeDedupEmbedder) ModelID() string    { return "intake-dedup-embedder" }
func (e *intakeDedupEmbedder) ContextWindow() int { return 512 }

// TestIntakeMCPAllowSimilarExit: the hold response tells the caller
// to "re-send with allow_similar=[id]" -- so the MCP registration
// must actually carry allow_similar through to the service. It was
// present on the HTTP body but dropped in the MCP argument mapping,
// leaving MCP intake callers with no exit from a hold.
func TestIntakeMCPAllowSimilarExit(t *testing.T) {
	srv, _ := setupTestServer(t, core.WithEmbedder(&intakeDedupEmbedder{dim: 16}))

	mcpServer := srv.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{
		Name: "intake-test", Version: "0.0.0",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	call := func(args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "gramaton_intake", Arguments: args,
		})
		if err != nil {
			t.Fatalf("call gramaton_intake: %v", err)
		}
		if res.IsError {
			t.Fatalf("gramaton_intake tool error: %+v", res.Content)
		}
		text, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
			t.Fatalf("parse payload: %v\npayload: %s", err, text.Text)
		}
		return out
	}

	const text = "the intake hold exit must be reachable over MCP"
	first := call(map[string]any{"content": text})
	seedID, _ := first["id"].(string)
	if seedID == "" {
		t.Fatalf("seed intake returned no id: %v", first)
	}

	second := call(map[string]any{"content": text})
	holdBody, held := second["held"].(map[string]any)
	if !held {
		t.Fatalf("second intake of identical content should hold, got %v", second)
	}
	if holdBody["id"] != seedID {
		t.Fatalf("held against %v, want %s", holdBody["id"], seedID)
	}

	acked := call(map[string]any{
		"content":       text,
		"allow_similar": []string{seedID},
	})
	if _, stillHeld := acked["held"]; stillHeld {
		t.Fatal("allow_similar did not pass through the MCP mapping; the hold has no exit")
	}
	if id, _ := acked["id"].(string); id == "" {
		t.Fatalf("acknowledged intake created nothing: %v", acked)
	}
}
