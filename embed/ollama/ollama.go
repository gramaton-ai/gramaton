package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an embedding provider that calls Ollama's HTTP API.
type Client struct {
	endpoint      string
	model         string
	client        *http.Client
	contextWindow int // auto-detected from /api/show, 0 = not yet queried
}

// New creates an Ollama embedding client. The context window is
// auto-detected from the model metadata on first use.
func New(endpoint, model string) *Client {
	c := &Client{
		endpoint: endpoint,
		model:    model,
		client:   &http.Client{Timeout: 60 * time.Second},
	}
	c.detectContextWindow()
	return c
}

// detectContextWindow queries Ollama's /api/show endpoint to get the
// model's context window (num_ctx). Falls back to 0 (use default)
// if the query fails.
func (c *Client) detectContextWindow() {
	body, _ := json.Marshal(map[string]string{"name": c.model})
	resp, err := c.client.Post(c.endpoint+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var result struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}

	// Look for context length in model_info. The key varies by model
	// family but commonly appears as "*context_length" (e.g.,
	// "bert.context_length", "llama.context_length").
	for k, v := range result.ModelInfo {
		if len(k) > 15 && k[len(k)-14:] == "context_length" {
			if f, ok := v.(float64); ok && f > 0 {
				c.contextWindow = int(f)
				return
			}
		}
	}
}

// embedRequest is the request body for the Ollama embed API.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the response from the Ollama embed API.
type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// errorResponse is the error format from Ollama.
type errorResponse struct {
	Error string `json:"error"`
}

// Embed generates embeddings for the given texts via the Ollama API.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{
		Model: c.model,
		Input: texts,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	url := c.endpoint + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit response reads to prevent unbounded memory allocation.
	const maxResponseSize = 50 * 1024 * 1024 // 50 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("ollama: %s", errResp.Error)
		}
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var result embedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("ollama: unmarshal response: %w", err)
	}

	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}

	return result.Embeddings, nil
}

// ModelID returns the model identifier for embedding provenance tracking.
func (c *Client) ModelID() string {
	return c.model
}

// ContextWindow returns the model's context window in tokens,
// auto-detected from Ollama's /api/show endpoint. Returns 0 if
// detection failed (caller should use a default).
func (c *Client) ContextWindow() int {
	return c.contextWindow
}
