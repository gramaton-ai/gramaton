package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/gramaton-ai/gramaton/server"
)

// serverURL returns the base URL for the running server, starting it
// if necessary. Returns an error if the server cannot be reached.
func serverURL() (string, error) {
	// Remote mode: dial the configured server on another machine.
	// No local server.json, no auto-start -- there is nothing local
	// to spawn. remoteMode also installs the auth + pinned-TLS
	// transport on the shared clients.
	if ep, err := remoteMode(); err != nil {
		return "", err
	} else if ep != nil {
		return ep.url, nil
	}

	dir := configDir()

	// Check for running server.
	info, err := server.ReadServerInfo(dir)
	if err == nil && server.IsProcessAlive(info.PID) {
		url := fmt.Sprintf("http://%s:%d", info.Bind, info.Port)
		// Verify it's actually gramaton (PID reuse protection).
		// Uses lock-free /v1/health endpoint so a busy server
		// still responds even when curation holds the write lock.
		if verifyServer(url) {
			return url, nil
		}
		// Process is alive but not responding at all.
		// Don't remove server info -- the process may just be
		// starting up. Only remove if the process is dead.
	} else if err == nil {
		// PID file exists but process is dead.
		server.RemoveServerInfo(dir)
	}

	// No running server. GRAMATON_NO_AUTOSTART=1 is a hard override
	// set by `gramaton uninstall` on every vendor-CLI invocation:
	// `claude mcp list` health-checks stdio entries by SPAWNING
	// them, and a spawned `gramaton mcp` inheriting that environment
	// must not resurrect the server uninstall just stopped (or, on a
	// dry run, start one that was never running). Checked before the
	// config-driven auto_start so no config state can re-enable it.
	if os.Getenv(setup.NoAutostartEnv) == "1" {
		return "", fmt.Errorf("no running server (%s=1 suppresses auto-start)", setup.NoAutostartEnv)
	}
	// The file sentinel closes the gap the env var cannot: an offline
	// destructive run (prune) must also stop CONCURRENT processes --
	// an MCP proxy fired from another terminal mid-run -- from
	// spawning a server against the store it is mutating. Env vars
	// don't cross process trees; a file in the config dir does.
	if pid, held := noAutostartSentinelHolder(dir); held {
		return "", fmt.Errorf("no running server (auto-start suspended by an offline maintenance run, pid %d)", pid)
	}
	// Respect server.auto_start (defaults true). A user running
	// gramaton under systemd / launchd flips it to false so the CLI
	// stops silently spawning a second server.
	if !shouldAutoStart(dir) {
		return "", fmt.Errorf("no running server (server.auto_start=false); run `gramaton serve` first")
	}
	if err := startBackground(); err != nil {
		return "", fmt.Errorf("auto-start server: %w", err)
	}

	info, err = server.ReadServerInfo(dir)
	if err != nil {
		return "", fmt.Errorf("server started but info not found")
	}

	return fmt.Sprintf("http://%s:%d", info.Bind, info.Port), nil
}

// shouldAutoStart resolves server.auto_start from the effective config.
// Config load errors fall open to the historical default (true) so a
// broken config doesn't wedge every CLI command before the user can
// fix it -- they'll see the startup error when the child fails to
// come up instead.
func shouldAutoStart(dir string) bool {
	cfgPath := filepath.Join(dir, "config.yaml")
	globalPath := filepath.Join(baseConfigDir(), "config.yaml")
	cfg, err := config.LoadWithFallback(cfgPath, globalPath)
	if err != nil {
		return true
	}
	return cfg.Server.AutoStart
}

// verifyServer checks that a URL responds to the gramaton health check.
// Uses the lock-free /v1/health endpoint which responds even when the
// server is busy with curation or bulk writes. Short timeout to avoid
// long hangs on dead servers.
func verifyServer(baseURL string) bool {
	resp, err := healthClient.Get(baseURL + "/v1/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// serverGet sends a GET request to the server and returns the parsed
// response envelope.
func serverGet(path string) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	resp, err := httpGet(base + path)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// serverPost sends a POST request to the server and returns the parsed
// response envelope.
func serverPost(path string, body any) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	var reqBody []byte
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	resp, err := httpPost(base+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// serverPostSlow is like serverPost but uses the slow client timeout.
// Use for I/O-heavy operations (backup, reembed).
func serverPostSlow(path string, body any) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	var reqBody []byte
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	resp, err := httpPostSlow(base+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// serverGetSlow is like serverGet but uses the slow client timeout.
func serverGetSlow(path string) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	resp, err := httpGetSlow(base + path)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// serverPatch sends a PATCH request to the server.
func serverPatch(path string, body any) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, base+path, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// serverDelete sends a DELETE request to the server.
func serverDelete(path string) (*server.ResponseEnvelope, error) {
	base, err := serverURL()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodDelete, base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	return parseResponse(resp)
}

// parseResponse reads an HTTP response and returns the envelope.
// If the response is an error, returns a formatted error.
func parseResponse(resp *http.Response) (*server.ResponseEnvelope, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp server.ErrorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Error.Code != "" {
			// Return the typed error so downstream consumers (notably
			// the MCP proxy) can preserve Code/Retryable instead of
			// collapsing to an opaque string.
			detail := errResp.Error
			return nil, &detail
		}
		// A 409 whose body is a normal data envelope is a structured
		// judgment call, not a transport error: a held save or a
		// version conflict. The payload IS the answer (the similar
		// record, the two exits, the current content) -- pass it
		// through so MCP callers see it instead of an opaque error.
		if resp.StatusCode == http.StatusConflict {
			var envelope server.ResponseEnvelope
			if json.Unmarshal(data, &envelope) == nil && envelope.Data != nil {
				return &envelope, nil
			}
		}
		return nil, fmt.Errorf("server error: HTTP %d", resp.StatusCode)
	}

	var envelope server.ResponseEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &envelope, nil
}

// noAutostartFile is the config-dir sentinel an offline maintenance
// command writes for its run: while it exists and its writer is
// alive, no CLI process auto-starts a server. A dead writer's
// sentinel is stale (a crashed run must not wedge auto-start
// forever) and is ignored.
const noAutostartFile = "no-autostart"

func noAutostartSentinelPath(cfgDir string) string {
	return filepath.Join(cfgDir, noAutostartFile)
}

// noAutostartSentinelHolder reports the live holder's pid, if any.
func noAutostartSentinelHolder(cfgDir string) (int, bool) {
	data, err := os.ReadFile(noAutostartSentinelPath(cfgDir))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	if !server.IsProcessAlive(pid) {
		return 0, false
	}
	return pid, true
}

// writeNoAutostartSentinel installs the sentinel for this process.
// Refuses over a live holder (a second concurrent maintenance run
// must not clobber the first's suppression), and the returned
// release removes the file only while it still names this process --
// so a racing run can never delete a suppression it does not own.
func writeNoAutostartSentinel(cfgDir string) (func(), error) {
	if pid, held := noAutostartSentinelHolder(cfgDir); held && pid != os.Getpid() {
		return nil, fmt.Errorf("another offline maintenance run holds the auto-start suspension (pid %d)", pid)
	}
	path := noAutostartSentinelPath(cfgDir)
	self := strconv.Itoa(os.Getpid())
	if err := core.AtomicWriteFile(path, []byte(self), 0o600); err != nil {
		return nil, err
	}
	return func() {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == self {
			_ = os.Remove(path)
		}
	}, nil
}
