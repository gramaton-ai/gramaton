package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

const defaultBaseURL = "https://api.openai.com"

// maxResponseSize limits response body reads to prevent unbounded
// memory allocation from a malicious or misconfigured server.
const maxResponseSize = 50 * 1024 * 1024 // 50 MB

// Client is an embedding provider that calls any OpenAI-compatible
// /v1/embeddings endpoint. Works with OpenAI, Azure OpenAI, vLLM,
// LiteLLM, Together, Fireworks, and any other compatible API.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// New creates an OpenAI-compatible embedding client.
func New(cfg config.EmbeddingConfig) (*Client, error) {
	key := resolveKey(cfg.APIKeyEnv)
	// Key is optional -- some local servers (vLLM, LiteLLM) don't need one.

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	model := cfg.Model
	if model == "" {
		return nil, fmt.Errorf("openai embed: model is required")
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  key,
		client:  &http.Client{Timeout: 60 * time.Second},
	}, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Error *apiError       `json:"error,omitempty"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Embed generates embeddings via the /v1/embeddings endpoint. Sends
// all texts in a single batch (the API handles batching internally).
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{
		Model: c.model,
		Input: texts,
	})
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("openai embed: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("openai embed: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("openai embed: HTTP %d", resp.StatusCode)
	}

	var result embeddingResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("openai embed: unmarshal response: %w", err)
	}

	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("openai embed: expected %d embeddings, got %d", len(texts), len(result.Data))
	}

	// Response data may not be in input order -- sort by index.
	embeddings := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai embed: index %d out of range", d.Index)
		}
		embeddings[d.Index] = d.Embedding
	}

	return embeddings, nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ContextWindow returns 0 (unknown). OpenAI-compatible APIs don't
// expose model context limits. Callers should use config or default.
func (c *Client) ContextWindow() int {
	return 0
}

func resolveKey(val string) string {
	if val == "" {
		return ""
	}
	if env := os.Getenv(val); env != "" {
		return env
	}
	// If it looks like a key, use directly.
	if strings.HasPrefix(val, "sk-") {
		return val
	}
	return ""
}
