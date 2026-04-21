package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
	"github.com/gramaton-ai/gramaton/llm/httpretry"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

const defaultBaseURL = "https://api.openai.com"

// maxResponseSize limits response body reads to prevent unbounded
// memory allocation from a malicious or misconfigured server.
const maxResponseSize = 10 * 1024 * 1024 // 10 MB

// Client calls any OpenAI-compatible /v1/chat/completions endpoint.
// Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together,
// Fireworks, and any other compatible API.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// New creates an OpenAI-compatible LLM client.
func New(cfg config.LLMConfig) (*Client, error) {
	key := secret.ResolveKey(cfg.APIKeyFile, cfg.APIKeyEnv, cfg.APIKey)

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	model := cfg.Model
	if model == "" {
		return nil, fmt.Errorf("openai llm: model is required")
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  key,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatUsage is the OpenAI token accounting. OpenAI reports a cached
// portion of the input via prompt_tokens_details.cached_tokens (present
// since 2024 prompt caching); writes aren't separately reported, so
// CacheWriteTokens stays zero for this provider.
type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// CompleteWithModel ignores the model override (OpenAI client uses
// the configured model). Falls back to the configured model.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	return c.Complete(ctx, prompt)
}

// Complete sends a prompt via /v1/chat/completions and returns the text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai llm: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		return req, nil
	}

	resp, err := httpretry.DoWithRetry(ctx, c.client, httpretry.DefaultRetryConfig(), buildReq)
	if err != nil {
		return "", fmt.Errorf("openai llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("openai llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("openai llm: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return "", fmt.Errorf("openai llm: HTTP %d", resp.StatusCode)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("openai llm: unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai llm: no choices in response")
	}

	telemetry.Record(ctx, telemetry.CallUsage{
		InputTokens:     result.Usage.PromptTokens,
		OutputTokens:    result.Usage.CompletionTokens,
		CacheReadTokens: result.Usage.PromptTokensDetails.CachedTokens,
	})

	return result.Choices[0].Message.Content, nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "openai" }

