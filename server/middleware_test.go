package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityHeadersRecoversPanic verifies that a panic in a downstream
// handler is converted into a structured 500 ErrorResponse when no
// response has been started. Pre-fix: panic propagated to net/http's
// recover which closed the connection mid-response, leaving the client
// with broken-pipe and no parseable error envelope.
func TestSecurityHeadersRecoversPanic(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Capture log output to verify the Warn line fires with stack.
	var logBuf bytes.Buffer
	srv.log = slog.New(slog.NewJSONHandler(&logBuf, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("synthetic panic for test")
	})

	ts := httptest.NewServer(srv.securityHeaders(mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/panic")
	if err != nil {
		t.Fatalf("GET /panic: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}

	body, _ := io.ReadAll(resp.Body)
	var er ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("body is not parseable JSON: %v\nbody: %s", err, body)
	}
	if er.Error.Code != "internal" {
		t.Errorf("error code: got %q, want %q", er.Error.Code, "internal")
	}
	if er.Error.Message != "internal error" {
		t.Errorf("error message: got %q, want %q", er.Error.Message, "internal error")
	}
	if er.Error.Retryable {
		t.Error("retryable: got true, want false (panics are not retryable)")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "panic in handler") {
		t.Errorf("expected 'panic in handler' Warn line; got:\n%s", logs)
	}
	if !strings.Contains(logs, "synthetic panic for test") {
		t.Errorf("expected panic value to appear in log; got:\n%s", logs)
	}
	if !strings.Contains(logs, "stack") {
		t.Errorf("expected stack trace key in log; got:\n%s", logs)
	}
}

// TestSecurityHeadersPanicAfterWriteHeaderDoesNotDoubleWrite verifies
// that when downstream writes the status before panicking, the
// recover defer logs the panic but does NOT call writeError (which
// would emit "superfluous WriteHeader" and corrupt the response). The
// client gets the partial body that was already in flight.
func TestSecurityHeadersPanicAfterWriteHeaderDoesNotDoubleWrite(t *testing.T) {
	srv, _ := setupTestServer(t)

	var logBuf bytes.Buffer
	srv.log = slog.New(slog.NewJSONHandler(&logBuf, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/panic-after", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"partial":true}`))
		panic("panic after partial write")
	})

	ts := httptest.NewServer(srv.securityHeaders(mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/panic-after")
	if err != nil {
		t.Fatalf("GET /panic-after: %v", err)
	}
	defer resp.Body.Close()

	// Status was already written as 200 before the panic.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200 (header had already been written)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"partial":true`) {
		t.Errorf("expected partial body to be preserved; got: %s", body)
	}
	// Panic must still be logged for observability.
	if !strings.Contains(logBuf.String(), "panic in handler") {
		t.Errorf("expected 'panic in handler' Warn line even after partial write; got:\n%s", logBuf.String())
	}
}

// TestSecurityHeadersErrAbortHandlerPropagates verifies that
// http.ErrAbortHandler is re-panicked rather than swallowed, so
// net/http's intentional-abort semantics still close the connection
// silently without our 500 envelope.
func TestSecurityHeadersErrAbortHandlerPropagates(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Discard panic logs from net/http's own recover; we just care
	// about whether the abort propagates past our middleware.
	srv.log = slog.New(slog.NewJSONHandler(io.Discard, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/abort", func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})

	// Wrap our middleware in a defer-recover so we can observe the
	// re-panic. In production this is what net/http's serverHandler
	// does -- catches ErrAbortHandler and silently closes the conn.
	var caught any
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	func() {
		defer func() { caught = recover() }()
		srv.securityHeaders(mux).ServeHTTP(rec, req)
	}()

	if caught != http.ErrAbortHandler {
		t.Fatalf("expected ErrAbortHandler to propagate; got: %v", caught)
	}
	// And no body should have been written (we never wrote a 500).
	if rec.Body.Len() != 0 {
		t.Errorf("expected no body for ErrAbortHandler; got: %s", rec.Body.String())
	}
}

// TestSecurityHeadersPanicOnMCPPathSetsContentType verifies that a
// panic recovered on the /mcp path emits a 500 with Content-Type
// application/json. /mcp deliberately skips Content-Type in
// securityHeaders (MCP negotiates its own), so writeError must set
// it itself or clients cannot parse the structured error envelope.
func TestSecurityHeadersPanicOnMCPPathSetsContentType(t *testing.T) {
	srv, _ := setupTestServer(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		panic("synthetic mcp panic")
	})

	ts := httptest.NewServer(srv.securityHeaders(mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	var er ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("body is not parseable JSON: %v\nbody: %s", err, body)
	}
	if er.Error.Code != "internal" {
		t.Errorf("error code: got %q, want internal", er.Error.Code)
	}
}

// TestSecurityHeadersNormalRequestStillWorks is a smoke test that the
// panic-recover defers do not interfere with normal request flow.
func TestSecurityHeadersNormalRequestStillWorks(t *testing.T) {
	srv, _ := setupTestServer(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	ts := httptest.NewServer(srv.securityHeaders(mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/ok")
	if err != nil {
		t.Fatalf("GET /ok: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("body: got %s, want contains \"ok\":true", body)
	}
}
