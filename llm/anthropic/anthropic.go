package anthropic

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
)

const defaultBaseURL = "https://api.anthropic.com"
const apiVersion = "2023-06-01"

// Client calls the Anthropic Messages API.
type Client struct {
	baseURL     string
	model       string
	apiKey      string
	client      *http.Client
	systemCache []systemBlock // cached system prompt, set via SetSystemPrompt
}

// New creates an Anthropic LLM client from config.
func New(cfg config.LLMConfig) (*Client, error) {
	key := secret.ResolveKey(cfg.APIKeyFile, cfg.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("anthropic: API key not configured (set api_key_file or api_key_env, or run 'gramaton configure')")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  key,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}


// messagesRequest is the Anthropic Messages API request body.
type messagesRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    []systemBlock  `json:"system,omitempty"`
	Messages  []message      `json:"messages"`
}

// systemBlock is a content block in the system message array.
// Using the array form (vs plain string) enables cache_control.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the Anthropic Messages API response.
type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Usage   usage          `json:"usage"`
	Error   *apiError      `json:"error,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// SetSystemPrompt configures a system prompt that will be included
// in all subsequent Complete/CompleteWithModel calls. The system
// prompt is marked with cache_control for Anthropic prompt caching.
// Pass empty string to clear.
func (c *Client) SetSystemPrompt(text string) {
	if text == "" {
		c.systemCache = nil
		return
	}
	c.systemCache = []systemBlock{{
		Type:         "text",
		Text:         text,
		CacheControl: &cacheControl{Type: "ephemeral"},
	}}
}

// CompleteWithModel sends a prompt using a specific model override.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if model == "" {
		model = c.model
	}
	return c.completeImpl(ctx, model, prompt)
}

// Complete sends a prompt and returns the completion text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.completeImpl(ctx, c.model, prompt)
}

func (c *Client) completeImpl(ctx context.Context, model, prompt string) (string, error) {
	req := messagesRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []message{
			{Role: "user", Content: prompt},
		},
	}
	if len(c.systemCache) > 0 {
		req.System = c.systemCache
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit response reads to prevent unbounded memory allocation.
	const maxResponseSize = 10 * 1024 * 1024 // 10 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("anthropic: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("anthropic: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return "", fmt.Errorf("anthropic: HTTP %d", resp.StatusCode)
	}

	var result messagesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("anthropic: unmarshal response: %w", err)
	}

	// Extract text from content blocks.
	var text string
	for _, block := range result.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return text, nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}
