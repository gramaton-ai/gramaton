package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gramaton-ai/gramaton/server"
)

// serverURL returns the base URL for the running server, starting it
// if necessary. Returns an error if the server cannot be reached.
func serverURL() (string, error) {
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

	// No running server -- auto-start.
	if err := startBackground(); err != nil {
		return "", fmt.Errorf("auto-start server: %w", err)
	}

	info, err = server.ReadServerInfo(dir)
	if err != nil {
		return "", fmt.Errorf("server started but info not found")
	}

	return fmt.Sprintf("http://%s:%d", info.Bind, info.Port), nil
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
		return nil, fmt.Errorf("server error: HTTP %d", resp.StatusCode)
	}

	var envelope server.ResponseEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &envelope, nil
}

