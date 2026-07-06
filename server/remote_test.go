package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
)

const testRemoteToken = "s3cret-remote-token"

// setupRemoteServer builds a test server with remote access resolved
// (token + admin_ops). No certificate is needed: these tests drive
// the handler stack directly rather than a live TLS listener.
func setupRemoteServer(t *testing.T, adminOps bool) (*Server, *core.Engine) {
	t.Helper()
	srv, eng := setupTestServer(t)
	srv.cfg.Remote = RemoteRuntime{
		Enabled:  true,
		Token:    testRemoteToken,
		AdminOps: adminOps,
	}
	return srv, eng
}

// remoteReq drives the full handler stack from a non-loopback origin,
// optionally presenting a bearer token.
func remoteReq(t *testing.T, srv *Server, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		data, _ := json.Marshal(body)
		buf = bytes.NewBuffer(data)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:44444" // non-loopback
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// --- Auth middleware matrix ---

func TestAuthLoopbackNeedsNoToken(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	// A loopback caller reaches the handler with no Authorization.
	w := doRequest(t, srv, "GET", "/v1/status", nil)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("loopback must not be challenged for a token, got 401: %s", w.Body.String())
	}
}

func TestAuthRemoteNoTokenIs401(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	w := remoteReq(t, srv, "GET", "/v1/status", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if h := w.Header().Get("WWW-Authenticate"); h == "" {
		t.Fatal("401 must advertise WWW-Authenticate: Bearer")
	}
	var env ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Error.Code != "unauthorized" {
		t.Fatalf("body should be a structured unauthorized error, got %s", w.Body.String())
	}
}

func TestAuthRemoteWrongTokenIs401(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	w := remoteReq(t, srv, "GET", "/v1/status", nil, "not-the-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthRemoteGoodTokenReachesHandler(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	w := remoteReq(t, srv, "GET", "/v1/status", nil, testRemoteToken)
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("authenticated remote should reach a knowledge route, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Tier gates ---

// Tier 1 (pathless admin) is open to authenticated remotes. Curation
// without a runner returns 503 from the handler -- the point is it is
// NOT blocked by the gate (401/403).
func TestTier1CurationOpenToAuthenticatedRemote(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	w := remoteReq(t, srv, "POST", "/v1/curation/trigger", nil, testRemoteToken)
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("tier-1 curation must be reachable by an authenticated remote, got %d: %s", w.Code, w.Body.String())
	}
}

// Tier 2 (path-taking) is loopback-only unless admin_ops. An
// authenticated remote WITHOUT admin_ops gets 403.
func TestTier2ForbiddenWithoutAdminOps(t *testing.T) {
	srv, _ := setupRemoteServer(t, false)
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"restore", "POST", "/v1/restore", map[string]any{"path": "x", "force": true}},
		{"export", "POST", "/v1/export", map[string]any{}},
		{"import", "POST", "/v1/import", map[string]any{"records": []any{}}},
		{"carve", "POST", "/v1/store/carve", map[string]any{"ids": []string{"x"}}},
		{"add", "POST", "/v1/store/add", map[string]any{"ids": []string{"x"}, "dest_data_dir": "/tmp/x/data"}},
		{"ingest-path", "POST", "/v1/ingest", map[string]any{"path": "/etc/hostname"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := remoteReq(t, srv, tc.method, tc.path, tc.body, testRemoteToken)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 (admin gate), got %d: %s", w.Code, w.Body.String())
			}
			var env ErrorResponse
			_ = json.Unmarshal(w.Body.Bytes(), &env)
			if env.Error.Code != "forbidden" {
				t.Fatalf("expected forbidden code, got %q", env.Error.Code)
			}
		})
	}
}

// With admin_ops enabled the gate no longer blocks; the request
// reaches the handler (and fails for its own reasons, not 403 from
// the gate).
func TestTier2ReachableWithAdminOps(t *testing.T) {
	srv, _ := setupRemoteServer(t, true)
	w := remoteReq(t, srv, "POST", "/v1/store/carve",
		map[string]any{"ids": []string{"nonexistent"}}, testRemoteToken)
	// Whatever the op decides, it must NOT be the admin-gate 403.
	if w.Code == http.StatusForbidden {
		var env ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if bytes.Contains(w.Body.Bytes(), []byte("not available to remote callers")) {
			t.Fatalf("admin_ops should have opened the gate, still blocked: %s", w.Body.String())
		}
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("authenticated remote should not be 401: %s", w.Body.String())
	}
}

// Shutdown and debug are process control: loopback-only even for an
// authenticated remote WITH admin_ops.
func TestShutdownDebugStayLoopbackEvenWithAdminOps(t *testing.T) {
	srv, _ := setupRemoteServer(t, true)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/shutdown"},
		{"GET", "/debug/goroutines"},
	} {
		w := remoteReq(t, srv, tc.method, tc.path, nil, testRemoteToken)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s must stay loopback-only (403) even with admin_ops, got %d", tc.path, w.Code)
		}
	}
}

// --- token compare + header parsing ---

func TestTokenMatches(t *testing.T) {
	if !tokenMatches("abc", "abc") {
		t.Fatal("equal tokens must match")
	}
	if tokenMatches("abc", "abd") {
		t.Fatal("different tokens must not match")
	}
	if tokenMatches("", "abc") || tokenMatches("abc", "") {
		t.Fatal("empty token must never match")
	}
	// Length difference must not short-circuit to a match.
	if tokenMatches("short", "a-much-longer-token-value") {
		t.Fatal("unequal-length tokens must not match")
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer xyz":  "xyz",
		"bearer xyz":  "xyz", // scheme is case-insensitive
		"Bearer  xyz": "xyz", // trimmed
		"Basic xyz":   "",
		"xyz":         "",
		"":            "",
		"Bearer ":     "",
	}
	for header, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if got := bearerToken(r); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

// --- MCP surface trimming ---

// The trimmed remote surface must exclude every path-taking tool, and
// each excluded name must be a real registered tool (else the strip
// is a silent no-op that would drift).
func TestMCPRemoteExcludedToolsAreRegistered(t *testing.T) {
	excluded := MCPRemoteExcludedToolNames()
	if !slices.Contains(excluded, "gramaton_intake") {
		t.Fatalf("gramaton_intake (path-taking) must be excluded from the remote surface, got %v", excluded)
	}
	for _, name := range excluded {
		if _, ok := mcpToolAccess[name]; !ok {
			t.Errorf("remote-excluded tool %q is not a classified/registered tool -- the strip would be a silent no-op", name)
		}
	}
}

func TestMCPFullSurfaceSelector(t *testing.T) {
	// Loopback always gets the full surface.
	loopback, _ := setupRemoteServer(t, false)
	rl := httptest.NewRequest("POST", "/mcp", nil)
	rl.RemoteAddr = "127.0.0.1:5000"
	if !loopback.mcpFullSurfaceAllowed(rl) {
		t.Fatal("loopback must get the full MCP surface")
	}

	// Authenticated remote WITHOUT admin_ops gets the trimmed surface.
	rr := httptest.NewRequest("POST", "/mcp", nil)
	rr.RemoteAddr = "203.0.113.9:5000"
	authed := requestWithAuth(rr)
	if loopback.mcpFullSurfaceAllowed(authed) {
		t.Fatal("plain authenticated remote must get the trimmed surface")
	}

	// With admin_ops, the authenticated remote gets the full surface.
	adminSrv, _ := setupRemoteServer(t, true)
	if !adminSrv.mcpFullSurfaceAllowed(requestWithAuth(rr)) {
		t.Fatal("authenticated remote with admin_ops must get the full surface")
	}
}

// requestWithAuth stamps the authenticated-context marker the auth
// middleware would have set, for handler-level tests that bypass it.
func requestWithAuth(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authContextKey{}, true))
}
